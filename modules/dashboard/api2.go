package main

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/custombot/bot/modules"
	"gopkg.in/yaml.v3"
)

// webCfg returns the WebConfigurable for a loaded module name.
func (m *DashboardModule) webCfg(name string) (modules.WebConfigurable, bool) {
	mgr, ok := m.ctx.Bot.GetModuleManager().(*modules.Manager)
	if !ok {
		return nil, false
	}
	mod, ok := mgr.Get(name)
	if !ok {
		return nil, false
	}
	return modules.IsWebConfigurable(mod)
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
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := readJSON(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := m.ctx.Bot.SetPresence(body.Type, body.Text); err != nil {
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
	lines, err := tailLines(path, n)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "lines": lines})
}

// logFilePath resolves logging.file_path from config.yml (repo-relative) back
// to an absolute path, falling back to <configDir>/logs/bot.log.
func (m *DashboardModule) logFilePath() string {
	path := filepath.Join(m.ctx.Bot.GetConfigDir(), "config.yml")
	data, err := os.ReadFile(path)
	if err == nil {
		var c struct {
			Logging struct {
				FilePath string `yaml:"file_path"`
			} `yaml:"logging"`
		}
		if yaml.Unmarshal(data, &c) == nil && c.Logging.FilePath != "" {
			fp := c.Logging.FilePath
			if filepath.IsAbs(fp) {
				return fp
			}
			return filepath.Join(m.ctx.Bot.GetConfigDir(), fp)
		}
	}
	return filepath.Join(m.ctx.Bot.GetConfigDir(), "logs", "bot.log")
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
				if meth == "GET" {
					m.apiModuleConfigGet(w, r, parts[2])
					return
				}
				if meth == "POST" {
					m.apiModuleConfigSet(w, r, parts[2])
					return
				}
			}
		}
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
