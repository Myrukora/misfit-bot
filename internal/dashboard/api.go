package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/misfit/bot/internal/discordutil"
	"github.com/misfit/bot/modules"
	"gopkg.in/yaml.v3"
)

// ── shared payload types ───────────────────────────────────────────────────

type meResponse struct {
	User            userJSON   `json:"user"`
	Level           string     `json:"level"`
	Configured      bool       `json:"configured"`
	ManageableGuild []guildOpt `json:"manageable_guilds"`
	CSRFToken       string     `json:"csrf_token"`
}

type userJSON struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Avatar   string `json:"avatar"`
}

type guildOpt struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Icon  string `json:"icon"`
	Owner bool   `json:"is_owner"`
}

type moduleConfigSchema struct {
	Name   string                `json:"name"`
	Loaded bool                  `json:"loaded"`
	Fields []modules.ConfigField `json:"fields"`
}

// maxJSONBody bounds every dashboard JSON request body (config writes, exec,
// presence). An authenticated client must not be able to force unbounded
// allocations in the bot process.
const maxJSONBody = 1 << 20 // 1 MiB

// readJSON decodes a JSON body with a size guard.
func readJSON(r io.Reader, v any) error {
	return json.NewDecoder(io.LimitReader(r, maxJSONBody)).Decode(v)
}

// ── /api/me ───────────────────────────────────────────────────────────────

func (m *DashboardModule) apiMe(w http.ResponseWriter, r *http.Request) {
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	level := m.resolveLevel(us)
	var guilds []guildOpt
	for _, id := range m.manageableGuildIDs(us) {
		g := m.guildSummary(id, us)
		if g != nil {
			guilds = append(guilds, *g)
		}
	}
	writeJSON(w, http.StatusOK, meResponse{
		User:            userJSON{ID: us.userID.String(), Username: us.username, Avatar: us.avatar},
		Level:           level,
		Configured:      m.configured(),
		ManageableGuild: guilds,
		CSRFToken:       us.csrfToken,
	})
}

// guildSummary builds a lightweight guild row for the nav and API responses.
func (m *DashboardModule) guildSummary(id string, us *userSession) *guildOpt {
	gid, err := snowflake.Parse(id)
	if err != nil || m.client == nil {
		return nil
	}
	g, ok := m.client.Caches.Guild(gid)
	if !ok {
		return &guildOpt{ID: id, Name: "(uncached) " + id}
	}
	icon := ""
	if u := g.IconURL(); u != nil {
		icon = *u
	}
	owner := us != nil && g.OwnerID == us.userID
	return &guildOpt{ID: g.ID.String(), Name: g.Name, Icon: icon, Owner: owner}
}

// ── /api/metrics ──────────────────────────────────────────────────────────

func (m *DashboardModule) apiMetrics(w http.ResponseWriter, r *http.Request) {
	if sessionOf(r) == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	writeJSON(w, http.StatusOK, m.metrics())
}

// ── /api/commands?guild=&raw= ─────────────────────────────────────────────

func (m *DashboardModule) apiCommands(w http.ResponseWriter, r *http.Request) {
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	raw := r.URL.Query().Get("raw") == "true" || r.URL.Query().Get("raw") == "1"
	guildID := r.URL.Query().Get("guild")
	var views []cmdView
	if guildID != "" {
		views = m.filterCatalog(us, raw, true, guildID)
	} else {
		views = m.filterCatalog(us, raw, false, "")
	}
	writeJSON(w, http.StatusOK, views)
}

// ── /api/guilds ───────────────────────────────────────────────────────────

func (m *DashboardModule) apiGuilds(w http.ResponseWriter, r *http.Request) {
	us := sessionOf(r)
	if us == nil {
		writeJSON(w, http.StatusOK, []guildOpt{})
		return
	}
	var guilds []guildOpt
	for _, id := range m.manageableGuildIDs(us) {
		if g := m.guildSummary(id, us); g != nil {
			guilds = append(guilds, *g)
		}
	}
	writeJSON(w, http.StatusOK, guilds)
}

// ── /api/guild/{id} ───────────────────────────────────────────────────────

type guildDetail struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Icon        string      `json:"icon"`
	OwnerID     string      `json:"owner_id"`
	MemberCount int         `json:"member_count"`
	Channels    []entityOpt `json:"channels"`
	Roles       []roleOpt   `json:"roles"`
	BotPerms    string      `json:"bot_perms"`
}

type entityOpt struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type roleOpt struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Color    int    `json:"color"`
	Position int    `json:"position"`
	Managed  bool   `json:"managed"`
}

// apiGuild serves the detail of one guild to a user who manages it.
func (m *DashboardModule) apiGuild(w http.ResponseWriter, r *http.Request, id string) {
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	if !m.canManageGuild(us, id) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	d, err := m.buildGuildDetail(id)
	if err != nil {
		code := http.StatusInternalServerError
		switch {
		case errors.Is(err, errInvalidGuildID):
			code = http.StatusBadRequest
		case errors.Is(err, errGuildNotCached):
			code = http.StatusNotFound
		case errors.Is(err, errClientUnavailable):
			code = http.StatusServiceUnavailable
		}
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// channelTypeName renders a channel type as a short human label. The switch
// lives in internal/discordutil so the dashboard and MCP server cannot drift.
func channelTypeName(t discord.ChannelType) string {
	return discordutil.ChannelTypeName(t)
}

// ── /api/modules ──────────────────────────────────────────────────────────

type moduleView struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Loaded      bool   `json:"loaded"`
	Available   bool   `json:"available"`
}

// apiModules serves the module catalog with load state and metadata.
func (m *DashboardModule) apiModules(w http.ResponseWriter, r *http.Request) {
	loaded := map[string]bool{}
	for _, n := range m.bot.GetLoadedModuleNames() {
		loaded[n] = true
	}
	var views []moduleView
	mgr, _ := m.bot.GetModuleManager().(*modules.Manager)
	for _, n := range m.bot.GetAvailableModuleNames() {
		v := moduleView{Name: n, Available: true, Loaded: loaded[n]}
		if mgr != nil {
			if info, ok := mgr.GetInfo(n); ok {
				v.Version, v.Description, v.Author = info.Version, info.Description, info.Author
			}
		}
		views = append(views, v)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	writeJSON(w, http.StatusOK, views)
}

// apiModuleAction loads, unloads or reloads one module for the owner.
func (m *DashboardModule) apiModuleAction(w http.ResponseWriter, r *http.Request, name, action string) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	// Compiled-in builtins cannot be load/unload/reloaded at runtime — they
	// come up via enabled_modules at startup.
	if modules.IsBuiltin(name) {
		writeError(w, http.StatusBadRequest,
			"module '"+name+"' is a compiled-in builtin — enable/disable it via `modules enable|disable "+name+"`, then restart")
		return
	}
	var err error
	switch action {
	case "load":
		err = m.bot.LoadModule(name)
	case "unload":
		err = m.bot.UnloadModule(name)
	case "reload":
		err = m.bot.ReloadModule(name)
	default:
		writeError(w, http.StatusBadRequest, "unknown action: "+action)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── /api/settings ─────────────────────────────────────────────────────────

type settingsResponse struct {
	Core    map[string]string    `json:"core"`
	Modules []moduleConfigSchema `json:"modules"`
}

// apiSettings serves the core values and configurable-module schemas.
func (m *DashboardModule) apiSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsResponse{
		Core:    m.coreSettingsGet(),
		Modules: m.configurableModulesSchema(),
	})
}

// coreSettingsGet reads the current core setting values from the bot.
func (m *DashboardModule) coreSettingsGet() map[string]string {
	cfg := m.rawConfig()
	// A config.yml without the intents section still runs with the defaults
	// (both on); show that instead of rendering the toggles off.
	intentsMC := m.cfgValue(cfg, "intents", "message_content")
	if intentsMC == "" {
		intentsMC = "true"
	}
	intentsGM := m.cfgValue(cfg, "intents", "guild_members")
	if intentsGM == "" {
		intentsGM = "true"
	}
	return map[string]string{
		"status":                  m.cfgValue(cfg, "bot", "status"),
		"log_level":               m.cfgValue(cfg, "logging", "level"),
		"log_enabled":             m.cfgValue(cfg, "logging", "enabled"),
		"log_file_path":           m.cfgValue(cfg, "logging", "file_path"),
		"modules_auto_load":       m.cfgValue(cfg, "modules", "auto_load"),
		"updater_enabled":         m.cfgValue(cfg, "updater", "enabled"),
		"updater_repo":            m.cfgValue(cfg, "updater", "repo"),
		"updater_branch":          m.cfgValue(cfg, "updater", "branch"),
		"updater_interval":        m.cfgValue(cfg, "updater", "check_interval"),
		"updater_auto_pull":       m.cfgValue(cfg, "updater", "auto_pull"),
		"mcp_enabled":             m.cfgValue(cfg, "mcp", "enabled"),
		"intents_message_content": intentsMC,
		"intents_guild_members":   intentsGM,
	}
}

// rawConfig reads config.yml into a generic map (once per call — the file is
// small). Returns nil on any read/parse error; callers fall back to "".
func (m *DashboardModule) rawConfig() map[string]any {
	path := filepath.Join(m.bot.GetConfigDir(), "config.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c map[string]any
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil
	}
	return c
}

// cfgValue reads a scalar from a raw config map at top.field (e.g.
// "updater"."check_interval"). Coerces the yaml.v3 scalars (string, bool,
// int) to their string form; missing or non-scalar values return "".
func (m *DashboardModule) cfgValue(cfg map[string]any, top, field string) string {
	if cfg == nil {
		return ""
	}
	block, ok := cfg[top].(map[string]any)
	if !ok {
		return ""
	}
	return scalarStr(block[field])
}

// scalarStr coerces a yaml.v3 scalar value to its string form.
func scalarStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return ""
	}
}

// apiSettingsCore applies validated core setting writes from the settings page.
func (m *DashboardModule) apiSettingsCore(w http.ResponseWriter, r *http.Request) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	var body map[string]string
	if err := readJSON(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	us := sessionOf(r)
	results := map[string]string{}
	anyErr := false
	for k, v := range body {
		// Per-server update notify channel (updater_notify_channel:<guild>)
		// is guild-scoped even though it lives in the core config: require
		// management rights on that specific server.
		if gid, ok := strings.CutPrefix(k, "updater_notify_channel:"); ok {
			gid = strings.TrimSpace(gid)
			if !m.canManageGuild(us, gid) {
				results[k] = "you may not manage this guild"
				anyErr = true
				continue
			}
			if err := m.checkChannelInGuild(gid, v); err != nil {
				results[k] = err.Error()
				anyErr = true
				continue
			}
		}
		status, err := m.setCoreSetting(k, v)
		if err != nil {
			results[k] = err.Error()
			anyErr = true
			continue
		}
		results[k] = status
		if status == "unknown key" {
			anyErr = true
		}
	}
	if anyErr {
		writeJSON(w, http.StatusUnprocessableEntity, results)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

// setCoreSetting writes one of the dashboard-managed core config keys with
// the web page's allowlist and live side-effects. File-only values (secrets,
// owner ownership, legal URLs, dashboard listen/public URL) are intentionally
// absent: edit config.yml/.env on the host instead.
func (m *DashboardModule) setCoreSetting(key, value string) (string, error) {
	allowed := map[string]bool{
		"status":    true,
		"log_level": true, "log_enabled": true, "log_file_path": true, "modules_auto_load": true,
		"updater_enabled": true, "updater_repo": true, "updater_branch": true,
		"updater_interval": true, "updater_auto_pull": true,
		"mcp_enabled":             true,
		"intents_message_content": true, "intents_guild_members": true,
	}
	// The per-server update notify channel is addressed as
	// "updater_notify_channel:<guild_id>" and stored in the updater section's
	// notify_channels map (config.Set parses the composite key).
	notifyGuild := ""
	if gid, ok := strings.CutPrefix(key, "updater_notify_channel:"); ok {
		notifyGuild = strings.TrimSpace(gid)
		if notifyGuild == "" {
			return "", fmt.Errorf("updater_notify_channel requires a guild id")
		}
	}
	if !allowed[key] && notifyGuild == "" {
		return "unknown key", nil
	}
	if err := m.bot.SetConfig(key, value); err != nil {
		return "", err
	}
	// Live side-effect for the one dashboard-managed value that is applied
	// immediately.
	if key == "status" {
		m.applyPresenceFromConfig()
	}
	return "ok", nil
}

// ApplyCoreSetting writes one dashboard-managed core config key with full
// owner trust (the MCP server authenticates clients by bearer token): the
// non-secret allowlisted keys, same live side-effects as the web settings page.
func (m *DashboardModule) ApplyCoreSetting(key, value string) error {
	status, err := m.setCoreSetting(key, value)
	if err != nil {
		return err
	}
	if status == "unknown key" {
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

// applyPresenceFromConfig reads the persisted bot.status from config.yml and
// applies it to the live presence so a status change from the Configuration tab
// takes effect immediately (without a restart). It is a best-effort fire-and-
// forget call: a nil client or a transient gateway error is logged, never
// surfaced to the request that triggered it.
func (m *DashboardModule) applyPresenceFromConfig() {
	if m.bot == nil {
		return
	}
	cfg := m.rawConfig()
	status := m.cfgValue(cfg, "bot", "status")
	if status == "" {
		return // no status set; leave the current presence untouched
	}
	// Empty activity type means "only set the status, keep the current activity".
	if err := m.bot.SetPresence("", status, ""); err != nil {
		m.logger.Warn("dashboard: failed to apply persisted presence status %q: %v", status, err)
	}
}
