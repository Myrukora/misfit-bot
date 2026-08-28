package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
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
	gid, err := snowflake.Parse(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid guild id")
		return
	}
	if m.client == nil {
		writeError(w, http.StatusServiceUnavailable, "bot client not ready")
		return
	}
	g, ok := m.client.Caches.Guild(gid)
	if !ok {
		writeError(w, http.StatusNotFound, "guild not in cache")
		return
	}
	d := guildDetail{
		ID:          g.ID.String(),
		Name:        g.Name,
		OwnerID:     g.OwnerID.String(),
		MemberCount: g.MemberCount,
	}
	if u := g.IconURL(); u != nil {
		d.Icon = *u
	}
	for ch := range m.client.Caches.ChannelsForGuild(gid) {
		d.Channels = append(d.Channels, entityOpt{
			ID: ch.ID().String(), Name: ch.Name(), Type: channelTypeName(ch.Type()),
		})
	}
	sort.Slice(d.Channels, func(i, j int) bool { return d.Channels[i].Name < d.Channels[j].Name })
	for role := range m.client.Caches.Roles(gid) {
		d.Roles = append(d.Roles, roleOpt{
			ID: role.ID.String(), Name: role.Name, Color: role.Color, Position: role.Position,
		})
	}
	sort.Slice(d.Roles, func(i, j int) bool { return d.Roles[i].Position > d.Roles[j].Position })
	if botMember, ok := m.client.Caches.SelfMember(gid); ok {
		d.BotPerms = m.client.Caches.MemberPermissions(botMember).String()
	}
	writeJSON(w, http.StatusOK, d)
}

// channelTypeName renders a channel type as a short human label.
func channelTypeName(t discord.ChannelType) string {
	switch t {
	case discord.ChannelTypeGuildText:
		return "Text"
	case discord.ChannelTypeGuildVoice:
		return "Voice"
	case discord.ChannelTypeGuildCategory:
		return "Category"
	case discord.ChannelTypeGuildNews:
		return "Announcement"
	case discord.ChannelTypeGuildStageVoice:
		return "Stage"
	case discord.ChannelTypeGuildForum:
		return "Forum"
	default:
		return "Other"
	}
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
	return map[string]string{
		"prefix":                 m.bot.GetPrefix(),
		"owner_id":               m.bot.GetOwnerID(),
		"tos_url":                m.bot.GetToS(),
		"privacy_url":            m.bot.GetPrivacy(),
		"status":                 m.cfgValue(cfg, "bot", "status"),
		"log_level":              m.cfgValue(cfg, "logging", "level"),
		"log_enabled":            m.cfgValue(cfg, "logging", "enabled"),
		"log_file_path":          m.cfgValue(cfg, "logging", "file_path"),
		"modules_auto_load":      m.cfgValue(cfg, "modules", "auto_load"),
		"dashboard_listen":       m.cfgValue(cfg, "dashboard", "listen"),
		"dashboard_public_url":   m.cfgValue(cfg, "dashboard", "public_url"),
		"token":                  redactedIfSet(m.cfgValue(cfg, "bot", "token")),
		"oauth_client_secret":    redactedIfSet(m.cfgValue(cfg, "oauth", "client_secret")),
		"updater_enabled":        m.cfgValue(cfg, "updater", "enabled"),
		"updater_repo":           m.cfgValue(cfg, "updater", "repo"),
		"updater_branch":         m.cfgValue(cfg, "updater", "branch"),
		"updater_token":          redactedIfSet(m.cfgValue(cfg, "updater", "token")),
		"updater_interval":       m.cfgValue(cfg, "updater", "check_interval"),
		"updater_auto_pull":      m.cfgValue(cfg, "updater", "auto_pull"),
		"updater_notify_channel": m.cfgValue(cfg, "updater", "notify_channel"),
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
	allowed := map[string]bool{
		"prefix": true, "owner_id": true,
		"tos_url": true, "privacy_url": true, "status": true,
		"log_level": true, "log_enabled": true, "log_file_path": true, "modules_auto_load": true,
		"dashboard_listen": true, "dashboard_public_url": true,
		"updater_enabled": true, "updater_repo": true, "updater_branch": true,
		"updater_token": true, "updater_interval": true, "updater_auto_pull": true,
		"updater_notify_channel": true,
		"token":                  true, "oauth_client_secret": true,
	}
	// Secrets and ownership are owner-only. Elevated users may still save their
	// allowed keys in the same batch; owner-only keys are simply never written
	// for them (the UI renders them disabled, this is the server-side
	// guarantee). owner_id is included so an elevated user can never transfer
	// ownership to themselves and escalate.
	ownerOnly := map[string]bool{
		"token": true, "updater_token": true, "oauth_client_secret": true, "owner_id": true,
	}
	owner := m.resolveLevel(sessionOf(r)) == lvlOwner
	results := map[string]string{}
	anyErr := false
	for k, v := range body {
		if !allowed[k] {
			results[k] = "unknown key"
			anyErr = true
			continue
		}
		if ownerOnly[k] && !owner {
			results[k] = "skipped (owner only)"
			continue
		}
		// Secrets come back redacted as "••••••••" and the UI leaves blank
		// secrets untouched — never persist the redaction marker or an empty
		// value as the real secret.
		if (k == "token" || k == "updater_token" || k == "oauth_client_secret") &&
			(v == "" || v == redactedIfSet(v)) {
			results[k] = "unchanged"
			continue
		}
		if err := m.bot.SetConfig(k, v); err != nil {
			results[k] = err.Error()
			anyErr = true
			continue
		}
		results[k] = "ok"
		// Live side-effects for dashboard-affecting keys: rebind the listener
		// when the bind address / public URL change, rebuild the OAuth client
		// when the shared client secret changes.
		switch k {
		case "dashboard_listen", "dashboard_public_url":
			m.rebindSoon(k)
		case "oauth_client_secret":
			m.refreshOAuth()
			m.sessions.clear() // invalidate existing sessions (secret changed)
		case "status":
			// Apply the new presence status live so it takes effect immediately
			// rather than waiting for the next restart.
			m.applyPresenceFromConfig()
		}
	}
	if anyErr {
		writeJSON(w, http.StatusUnprocessableEntity, results)
		return
	}
	writeJSON(w, http.StatusOK, results)
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
