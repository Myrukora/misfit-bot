package main

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
	"github.com/misfit/bot/updater"
	"gopkg.in/yaml.v3"
)

// webCfg returns the WebConfigurable for a loaded module name. Wrapper types
// that always satisfy the interface (LuaModule/PythonModule) are filtered by
// their HasWebConfig marker so a module without a dashboard integration file
// is treated as NOT configurable (no panel, API writes refused).
func (m *DashboardModule) webCfg(name string) (modules.WebConfigurable, bool) {
	mgr, ok := m.ctx.Bot.GetModuleManager().(*modules.Manager)
	if !ok {
		return nil, false
	}
	mod, ok := mgr.Get(name)
	if !ok {
		return nil, false
	}
	wc, ok := modules.IsWebConfigurable(mod)
	if ok {
		if hw, has := mod.(modules.HasWebConfig); has && !hw.HasWebConfig() {
			return nil, false
		}
	}
	return wc, ok
}

// configurableModulesSchema lists every loaded module that opted into
// WebConfigurable, returning its full field schema.
func (m *DashboardModule) configurableModulesSchema() []moduleConfigSchema {
	mgr, ok := m.ctx.Bot.GetModuleManager().(*modules.Manager)
	if !ok {
		return []moduleConfigSchema{}
	}
	var out []moduleConfigSchema
	for _, name := range m.ctx.Bot.GetLoadedModuleNames() {
		mod, ok := mgr.Get(name)
		if !ok {
			continue
		}
		if wc, ok := modules.IsWebConfigurable(mod); ok {
			out = append(out, moduleConfigSchema{Name: name, Loaded: true, Fields: wc.WebConfigSchema()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ── GET /api/settings/module/{name}?guild= ────────────────────────────────

func (m *DashboardModule) apiModuleConfigGet(w http.ResponseWriter, r *http.Request, name string) {
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	wc, ok := m.webCfg(name)
	if !ok {
		writeError(w, http.StatusNotFound, "module not configurable")
		return
	}
	guildID := r.URL.Query().Get("guild")
	level := m.resolveLevel(us)
	// Authorization happens per-field inside moduleConfigRead.
	vals, err := m.moduleConfigRead(wc, us, guildID, level)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vals)
}

// moduleConfigRead returns a field-filtered, secret-redacted snapshot.
func (m *DashboardModule) moduleConfigRead(wc modules.WebConfigurable, us *userSession, guildID, level string) (map[string]string, error) {
	vals, err := wc.WebGetConfig(guildID)
	if err != nil {
		return nil, err
	}
	if vals == nil {
		vals = map[string]string{}
	}
	out := map[string]string{}
	for _, f := range wc.WebConfigSchema() {
		// Global reads are owner/elevated only.
		if guildID == "" && level != lvlOwner && level != lvlElevated {
			continue
		}
		// Guild-scoped reads require managing that guild.
		if guildID != "" && !m.canManageGuild(us, guildID) {
			continue
		}
		// A guild view only shows guild-scoped fields.
		if guildID != "" && !f.GuildScoped {
			continue
		}
		v := vals[f.Key]
		if f.Type == modules.FieldTypeSecret && v != "" && level != lvlOwner {
			v = redactedIfSet(v)
		}
		out[f.Key] = v
	}
	return out, nil
}

// ── POST /api/settings/module/{name} ──────────────────────────────────────

type moduleConfigWrite struct {
	GuildID string `json:"guildID"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// apiModuleConfigSet validates and writes one WebConfigurable field.
func (m *DashboardModule) apiModuleConfigSet(w http.ResponseWriter, r *http.Request, name string) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	wc, ok := m.webCfg(name)
	if !ok {
		writeError(w, http.StatusNotFound, "module not configurable")
		return
	}
	var body moduleConfigWrite
	if err := readJSON(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	level := m.resolveLevel(us)
	// Authorize against the field's declared scope.
	var field *modules.ConfigField
	schema := wc.WebConfigSchema()
	for i := range schema {
		if schema[i].Key == body.Key {
			field = &schema[i]
			break
		}
	}
	if field == nil {
		writeError(w, http.StatusBadRequest, "unknown config key: "+body.Key)
		return
	}
	if field.GuildScoped {
		if body.GuildID == "" {
			writeError(w, http.StatusBadRequest, "this setting is per-guild; select a guild")
			return
		}
		if !m.canManageGuild(us, body.GuildID) {
			writeError(w, http.StatusForbidden, "you may not manage this guild")
			return
		}
	} else {
		// global setting
		if body.GuildID != "" {
			writeError(w, http.StatusBadRequest, "this is a global setting")
			return
		}
		if level != lvlOwner && level != lvlElevated {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}
	}
	if err := wc.WebSetConfig(body.GuildID, body.Key, body.Value); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	updated, _ := m.moduleConfigRead(wc, us, body.GuildID, level)
	writeJSON(w, http.StatusOK, updated)
}

// ── /api/presence ──────────────────────────────────────────────────────────

func (m *DashboardModule) apiPresence(w http.ResponseWriter, r *http.Request) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	var body struct {
		Type   string `json:"type"`
		Status string `json:"status"`
		Text   string `json:"text"`
	}
	if err := readJSON(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := m.ctx.Bot.SetPresence(body.Type, body.Status, body.Text); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── /api/permissions/elevated ─────────────────────────────────────────────

func (m *DashboardModule) apiElevatedList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, m.permMgr().GetElevated())
}

// apiElevatedAction adds or removes an elevated user.
func (m *DashboardModule) apiElevatedAction(w http.ResponseWriter, r *http.Request, action string) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r.Body, &body); err != nil || body.ID == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	switch action {
	case "add":
		m.permMgr().AddElevated(body.ID)
	case "remove":
		m.permMgr().RemoveElevated(body.ID)
	default:
		writeError(w, http.StatusBadRequest, "unknown action: "+action)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"elevated": m.permMgr().GetElevated()})
}

// ── /api/logs?tail=N ──────────────────────────────────────────────────────

func (m *DashboardModule) apiLogs(w http.ResponseWriter, r *http.Request) {
	n := 200
	if s := r.URL.Query().Get("tail"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			n = v
		}
	}
	if n < 1 {
		n = 200
	}
	if n > 1000 {
		n = 1000
	}
	path := m.logFilePath()
	if _, err := os.Stat(path); err != nil {
		// No log file (file logging disabled or nothing written yet) — respond
		// 200 with a hint instead of a 500 so the page stays usable.
		writeJSON(w, http.StatusOK, map[string]any{
			"path":  path,
			"lines": []string{},
			"note":  "no log file yet — is file logging enabled? (logging.enabled)",
		})
		return
	}
	lines, err := tailLines(path, n)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "lines": lines})
}

// logFilePath resolves the current daily-rotated log file:
// <dir>/<basename>-YYYY-MM-DD.log. The logger writes date-suffixed files via
// DailyRotatingWriter; the plain <basename>.log file only exists on
// pre-rotation installs. The newest non-empty daily file wins so the log page
// never tails a stale file or an empty file created at rotation.
func (m *DashboardModule) logFilePath() string {
	dir, base := m.logFileBase()
	return resolveLogFilePath(dir, base)
}

// resolveLogFilePath picks the newest non-empty daily log file for a
// directory + basename pair, falling back to the legacy <base>.log file.
// Pure and testable: the glob is relative to dir, so callers may point it at
// any directory.
func resolveLogFilePath(dir, base string) string {
	matches, err := filepath.Glob(filepath.Join(dir, base+"-*.log"))
	if err == nil && len(matches) > 0 {
		sort.Strings(matches) // ISO date suffixes sort chronologically
		for i := len(matches) - 1; i >= 0; i-- {
			if st, err := os.Stat(matches[i]); err == nil && st.Size() > 0 {
				return matches[i]
			}
		}
		return matches[len(matches)-1]
	}
	return filepath.Join(dir, base+".log")
}

// logFileBase resolves logging.file_path from config.yml into a directory and
// a file basename (e.g. "logs/bot.log" → "logs", "bot"), defaulting to
// <configDir>/logs/bot.log.
func (m *DashboardModule) logFileBase() (string, string) {
	fp := "logs/bot.log"
	path := filepath.Join(m.ctx.Bot.GetConfigDir(), "config.yml")
	if data, err := os.ReadFile(path); err == nil {
		var c struct {
			Logging struct {
				FilePath string `yaml:"file_path"`
			} `yaml:"logging"`
		}
		if yaml.Unmarshal(data, &c) == nil && c.Logging.FilePath != "" {
			fp = c.Logging.FilePath
		}
	}
	if !filepath.IsAbs(fp) {
		fp = filepath.Join(m.ctx.Bot.GetConfigDir(), fp)
	}
	return filepath.Dir(fp), strings.TrimSuffix(filepath.Base(fp), ".log")
}

// tailLines returns the last n lines of a file efficiently.
func tailLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	ring := make([]string, 0, n)
	for sc.Scan() {
		ring = append(ring, sc.Text())
		if len(ring) > n {
			ring = ring[len(ring)-n:]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return ring, nil
}

// ── /api/exec — universal command execution ─────────────────────────────

// apiExec runs any command the requesting dashboard user may use, exactly as
// the Discord dispatcher would gate it: SuperOwnerOnly is always blocked
// (server-side, in ExecuteCommand), OwnerOnly requires owner/elevated, and
// RequiredPerm commands check the user's cached perms for the given guild.
// The response is the captured embed/text, never raw process output.
//
// The exec allowlist (DashboardConfig.ExecAllowlist) is the security
// boundary: when non-empty, any command not in the list is refused before
// ExecuteCommand is called, so an owner can lock the dashboard to a safe
// subset even if it's reachable on the network.
func (m *DashboardModule) apiExec(w http.ResponseWriter, r *http.Request, us *userSession) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	var body struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Guild   string   `json:"guild"`
		Channel string   `json:"channel"`
	}
	if err := readJSON(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Command == "" {
		writeError(w, http.StatusBadRequest, "command required")
		return
	}
	if !m.execAllowed(body.Command) {
		writeError(w, http.StatusForbidden, "this command is not enabled for the dashboard")
		return
	}
	res, err := m.ctx.Bot.ExecuteCommand(body.Command, body.Args, body.Guild, body.Channel, us.userID.String(), m.execMode())
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, commands.ErrWebForbidden) || errors.Is(err, commands.ErrInsufficientPerm) {
			code = http.StatusForbidden
		}
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"title": res.Title, "description": res.Description, "color": res.Color, "text": res.Text,
	})
}

// execAllowed reports whether a command name may be run via the dashboard's
// Run button. The allowlist is the sole security boundary for /api/exec: an
// EMPTY allowlist means NOTHING is runnable (opt-in only, per the plan), and a
// non-empty allowlist permits only its entries.
func (m *DashboardModule) execAllowed(name string) bool {
	allowlist := m.execAllowlist()
	if len(allowlist) == 0 {
		return false
	}
	for _, allowed := range allowlist {
		if allowed == name {
			return true
		}
	}
	return false
}

// execAllowlist returns a copy of the configured exec allowlist, read under
// the config lock so it stays consistent with cfg swaps.
func (m *DashboardModule) execAllowlist() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		return nil
	}
	out := make([]string, len(m.cfg.ExecAllowlist))
	copy(out, m.cfg.ExecAllowlist)
	return out
}

// ── /api/cmdcfg/toggle ──────────────────────────────────────────────────────
// Global (owner/elevated, guildID empty) toggles a bot-owner override; local
// (staff, guildID set) narrows a command for one guild. A staff toggle can only
// DISABLE a command that is not globally disabled — it can never re-enable a
// globally-disabled command (Carl semantics). Owner/elevated toggling is
// unrestricted.

type cmdCfgToggle struct {
	Name     string   `json:"name"`
	Disabled bool     `json:"disabled"`
	GuildID  string   `json:"guildID"`
	ModOnly  *bool    `json:"modOnly"`
	Channels []string `json:"channels"`
	Roles    []string `json:"roles"`
}

// toggleCmdCfg enforces the per-command enable/disable override.
func (m *DashboardModule) toggleCmdCfg(w http.ResponseWriter, r *http.Request) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	us := sessionOf(r)
	if us == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	var body cmdCfgToggle
	if err := readJSON(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "command name required")
		return
	}
	level := m.resolveLevel(us)
	ov := m.commandOverrides()
	if ov == nil {
		writeError(w, http.StatusServiceUnavailable, "command overrides unavailable")
		return
	}

	// Global scope: owner/elevated, no guild.
	if body.GuildID == "" {
		if level != lvlOwner && level != lvlElevated {
			writeError(w, http.StatusForbidden, "owner or elevated only")
			return
		}
		cfg := commands.GlobalCmdCfg{
			AllowedChannels: dedupeStrings(body.Channels),
			AllowedRoles:    dedupeStrings(body.Roles),
		}
		dis := body.Disabled
		cfg.Disabled = &dis
		if body.ModOnly != nil {
			cfg.ModOnly = body.ModOnly
		}
		if err := ov.SetGlobal(body.Name, cfg); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := ov.Save(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": body.Name, "disabled": body.Disabled, "scope": "global"})
		return
	}

	// Local scope: staff managing the guild.
	if level != lvlStaff && level != lvlOwner && level != lvlElevated {
		writeError(w, http.StatusForbidden, "you may not manage this guild")
		return
	}
	if !m.canManageGuild(us, body.GuildID) {
		writeError(w, http.StatusForbidden, "you may not manage this guild")
		return
	}
	// Local can only narrow: a staff toggle can never re-enable a globally
	// disabled command, and its channel/role allowlists can only shrink.
	if body.Disabled && ov.GlobalDisabled(body.Name) {
		writeError(w, http.StatusForbidden, "this command is disabled globally and cannot be re-enabled here")
		return
	}
	if !body.Disabled && ov.GlobalDisabled(body.Name) {
		writeError(w, http.StatusForbidden, "this command is disabled globally")
		return
	}
	cfg := commands.GuildCmdCfg{
		AllowedChannels: dedupeStrings(body.Channels),
		AllowedRoles:    dedupeStrings(body.Roles),
	}
	dis := body.Disabled
	cfg.Disabled = &dis
	if body.ModOnly != nil {
		cfg.ModOnly = body.ModOnly
	}
	if err := ov.SetGuild(body.GuildID, body.Name, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := ov.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": body.Name, "disabled": body.Disabled, "guildID": body.GuildID, "scope": "guild"})
}

// dedupeStrings returns a de-duplicated copy of s preserving first-seen order,
// dropping empties.
func dedupeStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(s))
	out := make([]string, 0, len(s))
	for _, x := range s {
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

// commandOverrides returns the per-command override store via the bot adapter
// Interface, or nil when the feature is disabled. The dashboard reads it to
// render enable/disable toggles and to persist new overrides.
func (m *DashboardModule) commandOverrides() *commands.CommandOverrides {
	if m.ctx == nil || m.ctx.Bot == nil {
		return nil
	}
	return m.ctx.Bot.CommandOverrides()
}

// ── /api/updater/* (owner only) ──────────────────────────────────────────

// updaterMgr returns the self-update manager (nil when unavailable).
func (m *DashboardModule) updaterMgr() *updater.Manager {
	upd, _ := m.ctx.Bot.GetUpdater().(*updater.Manager)
	return upd
}

// apiUpdaterStatus returns the live updater state (repo/branch/last check/…).
func (m *DashboardModule) apiUpdaterStatus(w http.ResponseWriter, r *http.Request) {
	upd := m.updaterMgr()
	if upd == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not available")
		return
	}
	writeJSON(w, http.StatusOK, upd.Status())
}

// apiUpdaterCheck fetches from GitHub and reports how far behind the bot is.
func (m *DashboardModule) apiUpdaterCheck(w http.ResponseWriter, r *http.Request) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	upd := m.updaterMgr()
	if upd == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not available")
		return
	}
	// Bound the git fetch: propagate client cancellation and never let a
	// stalled network hold the handler goroutine.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := upd.Check(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// apiUpdaterApply runs the full update pipeline (pull → rebuild → swap). The
// bot keeps running the old code until the restart fires via OnApplied, so the
// client gets a clean 200 before the process re-executes.
func (m *DashboardModule) apiUpdaterApply(w http.ResponseWriter, r *http.Request) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	upd := m.updaterMgr()
	if upd == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not available")
		return
	}
	// Apply rebuilds the binary and every Go plugin — far too long for an HTTP
	// round trip. Run it on a server-side deadline that survives the request;
	// failures surface through the updater's status last_error (Apply sets it
	// internally), which the settings panel displays.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := upd.Apply(ctx); err != nil {
			m.logger.Error("Dashboard: updater apply failed: %v", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// apiUpdaterTest posts one sample PR + one sample commit embed to the notify
// channel so the owner can preview both markdown-rich formats.
func (m *DashboardModule) apiUpdaterTest(w http.ResponseWriter, r *http.Request) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	upd := m.updaterMgr()
	if upd == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not available")
		return
	}
	if err := upd.NotifyTest(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── /api/shutdown, /api/restart ───────────────────────────────────────────

func (m *DashboardModule) apiShutdown(w http.ResponseWriter, r *http.Request) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go func() { m.ctx.Bot.Shutdown() }()
}

// apiRestart restarts the bot process after acknowledging the request.
func (m *DashboardModule) apiRestart(w http.ResponseWriter, r *http.Request) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go func() { m.ctx.Bot.Restart() }()
}

// routeAPI dispatches /api/* with method + tier + CSRF enforcement.
func (m *DashboardModule) routeAPI(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	meth := strings.ToUpper(r.Method)
	switch parts[0] {
	case "me":
		if meth == "GET" {
			m.apiMe(w, r)
			return
		}
	case "metrics":
		if meth == "GET" {
			m.apiMetrics(w, r)
			return
		}
	case "commands":
		if meth == "GET" {
			m.apiCommands(w, r)
			return
		}
	case "guilds":
		if meth == "GET" {
			m.apiGuilds(w, r)
			return
		}
	case "guild":
		if meth == "GET" && len(parts) >= 2 {
			m.apiGuild(w, r, parts[1])
			return
		}
	case "modules":
		if meth == "GET" {
			// Module listing exposes installed module names/versions/load
			// state — never for unauthenticated clients. Match the POST
			// action gate (owner/elevated).
			us := sessionOf(r)
			if us == nil || !levelGEQ(m.resolveLevel(us), lvlElevated) {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			m.apiModules(w, r)
			return
		}
		if meth == "POST" && len(parts) >= 3 {
			// owner/elevated only
			us := sessionOf(r)
			if us == nil || !levelGEQ(m.resolveLevel(us), lvlElevated) {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			m.apiModuleAction(w, r, parts[1], parts[2])
			return
		}
	case "settings":
		if meth == "GET" {
			if len(parts) >= 3 && parts[1] == "module" {
				// apiModuleConfigGet authorizes per-field scope internally
				// (401 without a session, per-field gates inside
				// moduleConfigRead). Must live OUTSIDE the POST guard —
				// nesting it there made this route unreachable.
				m.apiModuleConfigGet(w, r, parts[2])
				return
			}
			// owner/elevated only
			us := sessionOf(r)
			if us == nil || !levelGEQ(m.resolveLevel(us), lvlElevated) {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			m.apiSettings(w, r)
			return
		}
		if meth == "POST" && len(parts) >= 2 {
			switch parts[1] {
			case "core":
				us := sessionOf(r)
				if us == nil || !levelGEQ(m.resolveLevel(us), lvlElevated) {
					writeError(w, http.StatusForbidden, "insufficient permissions")
					return
				}
				m.apiSettingsCore(w, r)
				return
			case "module":
				if len(parts) < 3 {
					writeError(w, http.StatusNotFound, "module name required")
					return
				}
				m.apiModuleConfigSet(w, r, parts[2])
				return
			}
		}
	case "tickets":
		m.routeTicketsAPI(w, r, meth, parts)
		return
	case "ticketfiles":
		// /api/ticketfiles/<guild>/<ticket>/<filename> — mirrored attachments.
		if meth == "GET" && len(parts) == 4 {
			m.serveTicketFile(w, r, parts[1], parts[2], parts[3])
			return
		}
		writeError(w, http.StatusNotFound, "not found")
		return
	case "presence":
		if meth == "POST" {
			us := sessionOf(r)
			if us == nil || !levelGEQ(m.resolveLevel(us), lvlElevated) {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			m.apiPresence(w, r)
			return
		}
	case "permissions":
		if len(parts) >= 2 && parts[1] == "elevated" {
			us := sessionOf(r)
			if us == nil || !levelGEQ(m.resolveLevel(us), lvlElevated) {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			if meth == "GET" {
				m.apiElevatedList(w, r)
				return
			}
			if meth == "POST" && len(parts) >= 3 {
				m.apiElevatedAction(w, r, parts[2])
				return
			}
		}
	case "logs":
		if meth == "GET" {
			us := sessionOf(r)
			if us == nil || !levelGEQ(m.resolveLevel(us), lvlElevated) {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			m.apiLogs(w, r)
			return
		}
	case "exec":
		if meth == "POST" {
			us := sessionOf(r)
			if us == nil {
				writeError(w, http.StatusForbidden, "not logged in")
				return
			}
			m.apiExec(w, r, us)
			return
		}
	case "cmdcfg":
		if meth == "POST" && len(parts) >= 2 && parts[1] == "toggle" {
			m.toggleCmdCfg(w, r)
			return
		}
	case "updater":
		// Owner only — mirrors the [p]update command's OwnerOnly flag. Actions
		// rebuild and restart the bot, so elevated users never get them.
		if us := sessionOf(r); us == nil || m.resolveLevel(us) != lvlOwner {
			writeError(w, http.StatusForbidden, "owner only")
			return
		}
		switch {
		case meth == "GET" && len(parts) == 2 && parts[1] == "status":
			m.apiUpdaterStatus(w, r)
		case meth == "POST" && len(parts) == 2 && parts[1] == "check":
			m.apiUpdaterCheck(w, r)
		case meth == "POST" && len(parts) == 2 && parts[1] == "apply":
			m.apiUpdaterApply(w, r)
		case meth == "POST" && len(parts) == 2 && parts[1] == "test":
			m.apiUpdaterTest(w, r)
		default:
			http.NotFound(w, r)
		}
		return
	case "shutdown":
		if meth == "POST" {
			us := sessionOf(r)
			if us == nil || !levelGEQ(m.resolveLevel(us), lvlElevated) {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			m.apiShutdown(w, r)
			return
		}
	case "restart":
		if meth == "POST" {
			us := sessionOf(r)
			if us == nil || !levelGEQ(m.resolveLevel(us), lvlElevated) {
				writeError(w, http.StatusForbidden, "insufficient permissions")
				return
			}
			m.apiRestart(w, r)
			return
		}
	}
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}
