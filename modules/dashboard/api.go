package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/custombot/bot/modules"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
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

// readJSON decodes a JSON body with a sane size guard.
func readJSON(r io.Reader, v any) error {
	return json.NewDecoder(r).Decode(v)
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

func (m *DashboardModule) apiModules(w http.ResponseWriter, r *http.Request) {
	loaded := map[string]bool{}
	for _, n := range m.ctx.Bot.GetLoadedModuleNames() {
		loaded[n] = true
	}
	var views []moduleView
	mgr, _ := m.ctx.Bot.GetModuleManager().(*modules.Manager)
	for _, n := range m.ctx.Bot.GetAvailableModuleNames() {
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

func (m *DashboardModule) apiModuleAction(w http.ResponseWriter, r *http.Request, name, action string) {
	if !m.checkCSRF(r) {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return
	}
	var err error
	switch action {
	case "load":
		err = m.ctx.Bot.LoadModule(name)
	case "unload":
		err = m.ctx.Bot.UnloadModule(name)
	case "reload":
		err = m.ctx.Bot.ReloadModule(name)
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

func (m *DashboardModule) apiSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsResponse{
		Core:    m.coreSettingsGet(),
		Modules: m.configurableModulesSchema(),
	})
}

func (m *DashboardModule) coreSettingsGet() map[string]string {
	return map[string]string{
		"prefix":      m.ctx.Bot.GetPrefix(),
		"name":        m.ctx.Bot.GetName(),
		"owner_id":    m.ctx.Bot.GetOwnerID(),
		"status":      m.coreStatusRead(),
		"tos_url":     m.ctx.Bot.GetToS(),
		"privacy_url": m.ctx.Bot.GetPrivacy(),
		"log_level":   m.logSetting("level"),
		"log_enabled": m.logSetting("enabled"),
	}
}

// logSetting reads a single field from config.yml's logging block.
func (m *DashboardModule) logSetting(field string) string {
	path := filepath.Join(m.ctx.Bot.GetConfigDir(), "config.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var c struct {
		Logging map[string]any `yaml:"logging"`
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return ""
	}
	v, ok := c.Logging[field].(string)
	if !ok {
		switch x := c.Logging[field].(type) {
		case bool:
			if x {
				return "true"
			}
			return "false"
		}
		return ""
	}
	return v
}

// coreStatusRead reads the bot "status" field from config.yml (not exposed by Interface).
func (m *DashboardModule) coreStatusRead() string {
	path := filepath.Join(m.ctx.Bot.GetConfigDir(), "config.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var c struct {
		Bot struct {
			Status string `yaml:"status"`
		} `yaml:"bot"`
	}
	_ = yaml.Unmarshal(data, &c)
	return c.Bot.Status
}

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
		"prefix": true, "name": true, "owner_id": true, "status": true,
		"tos_url": true, "privacy_url": true, "log_level": true, "log_enabled": true,
	}
	results := map[string]string{}
	anyErr := false
	for k, v := range body {
		if !allowed[k] {
			results[k] = "unknown key"
			anyErr = true
			continue
		}
		if err := m.ctx.Bot.SetConfig(k, v); err != nil {
			results[k] = err.Error()
			anyErr = true
		} else {
			results[k] = "ok"
		}
	}
	if anyErr {
		writeJSON(w, http.StatusUnprocessableEntity, results)
		return
	}
	writeJSON(w, http.StatusOK, results)
}
