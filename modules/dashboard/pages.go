package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/custombot/bot/modules"
	"github.com/disgoorg/snowflake/v2"
)

// fieldRender is a render-friendly ConfigField (pointers de-pointered, numeric
// bounds formatted as strings) used by the shared "field" template partial.
type fieldRender struct {
	Key         string
	Label       string
	Help        string
	Type        string
	Value       string
	Placeholder string
	Min         string
	Max         string
	Step        string
	Options     []string
	Entities    []entityOpt // for channel/role pickers (id+name) from the cache
	Scope       string
	GuildScoped bool
}

type moduleConfigView struct {
	Name   string
	Fields []fieldRender
}

type settingsPageData struct {
	GuildID       string
	GuildName     string
	Core          map[string]string // global core settings (nil for guild view)
	DashboardSelf moduleConfigView
	Modules       []moduleConfigView
}

// ── / (overview) ──────────────────────────────────────────────────────────

func (m *DashboardModule) handleIndex(w http.ResponseWriter, r *http.Request) {
	d := m.baseData(sessionOf(r))
	d.Content = m.metrics()
	m.tmpl.render(w, "index", d)
}

// ── /login ─────────────────────────────────────────────────────────────────

func (m *DashboardModule) renderLogin(w http.ResponseWriter, r *http.Request) {
	d := m.baseData(sessionOf(r))
	m.tmpl.render(w, "login", d)
}

func (m *DashboardModule) renderSetup(w http.ResponseWriter, r *http.Request) {
	d := m.baseData(sessionOf(r))
	d.Content = map[string]string{
		"public_url":   m.effectivePublicURL(),
		"client_id":    m.cfg.ClientID,
		"redirect_uri": m.effectivePublicURL() + "/callback",
		"listen":       m.effectiveListen(),
		"prefix":       m.ctx.Bot.GetPrefix(),
	}
	m.tmpl.render(w, "setup", d)
}

// ── /commands?guild=&raw= ──────────────────────────────────────────────────

func (m *DashboardModule) handleCommandsPage(w http.ResponseWriter, r *http.Request) {
	us := sessionOf(r)
	raw := r.URL.Query().Get("raw") == "true"
	guildID := r.URL.Query().Get("guild")
	var views []cmdView
	if guildID != "" {
		views = m.filterCatalog(us, raw, true, guildID)
	} else {
		views = m.filterCatalog(us, raw, false, "")
	}
	d := m.baseData(us)
	d.Raw = raw
	d.Content = map[string]any{
		"groups": groupCommands(views),
		"guild":  guildID,
		"count":  len(views),
		"canRaw": d.IsOwner || d.IsElevated,
	}
	m.tmpl.render(w, "commands", d)
}

// ── /guild/{id} ───────────────────────────────────────────────────────────

func (m *DashboardModule) handleGuildPage(w http.ResponseWriter, r *http.Request, id string) {
	detail, err := m.buildGuildDetail(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	d := m.baseData(sessionOf(r))
	d.Content = detail
	m.tmpl.render(w, "guild", d)
}

// buildGuildDetail assembles a guild view from the cache. Shared by the page
// and the /api/guild/{id} endpoint.
func (m *DashboardModule) buildGuildDetail(id string) (*guildDetail, error) {
	gid, err := snowflake.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid guild id")
	}
	g, ok := m.client.Caches.Guild(gid)
	if !ok {
		return nil, fmt.Errorf("guild not in cache")
	}
	d := &guildDetail{
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
	return d, nil
}

// ── /modules ──────────────────────────────────────────────────────────────

func (m *DashboardModule) handleModulesPage(w http.ResponseWriter, r *http.Request) {
	loaded := map[string]bool{}
	for _, n := range m.ctx.Bot.GetLoadedModuleNames() {
		loaded[n] = true
	}
	var views []moduleView
	for _, n := range m.ctx.Bot.GetAvailableModuleNames() {
		views = append(views, moduleView{Name: n, Available: true, Loaded: loaded[n]})
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	d := m.baseData(sessionOf(r))
	d.Content = views
	m.tmpl.render(w, "modules", d)
}

// ── /settings?guild= ─────────────────────────────────────────────────────

func (m *DashboardModule) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	us := sessionOf(r)
	if us == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	level := m.resolveLevel(us)
	guildID := r.URL.Query().Get("guild")

	if guildID != "" {
		if !m.canManageGuild(us, guildID) {
			http.Error(w, "403 Forbidden — you may not manage this guild", http.StatusForbidden)
			return
		}
	} else if level != lvlOwner && level != lvlElevated {
		// Regular (and even staff with no guild context) cannot see global config.
		http.Error(w, "403 Forbidden", http.StatusForbidden)
		return
	}

	data := settingsPageData{GuildID: guildID}
	if guildID != "" {
		if detail, err := m.buildGuildDetail(guildID); err == nil {
			data.GuildName = detail.Name
		}
	} else {
		data.Core = m.coreSettingsGet()
	}

	// Dashboard self-config (global only).
	if guildID == "" {
		if wc, ok := m.webCfg("dashboard"); ok {
			data.DashboardSelf = m.buildModuleView(wc, "dashboard", us, level, "")
		}
	}

	for _, name := range m.ctx.Bot.GetLoadedModuleNames() {
		if name == "dashboard" {
			continue // handled separately above
		}
		wc, ok := m.webCfg(name)
		if !ok {
			continue
		}
		mv := m.buildModuleView(wc, name, us, level, guildID)
		if len(mv.Fields) > 0 {
			data.Modules = append(data.Modules, mv)
		}
	}

	d := m.baseData(us)
	d.Content = data
	m.tmpl.render(w, "settings", d)
}

// buildModuleView produces the filtered, redacted field renders for a module.
func (m *DashboardModule) buildModuleView(wc modules.WebConfigurable, name string, us *userSession, level, guildID string) moduleConfigView {
	vals, _ := m.moduleConfigRead(wc, us, guildID, level)
	mv := moduleConfigView{Name: name}
	for _, f := range wc.WebConfigSchema() {
		if guildID != "" && !f.GuildScoped {
			continue
		}
		if guildID == "" && f.GuildScoped {
			continue
		}
		mv.Fields = append(mv.Fields, m.buildFieldRender(f, vals[f.Key], guildID))
	}
	return mv
}

// buildFieldRender turns a ConfigField + current value into a render-friendly
// field, populating channel/role entity lists from the cache when a guild is set.
func (m *DashboardModule) buildFieldRender(f modules.ConfigField, value, guildID string) fieldRender {
	fr := fieldRender{
		Key:         f.Key,
		Label:       f.Label,
		Help:        f.Help,
		Type:        f.Type,
		Value:       value,
		Placeholder: f.Placeholder,
		Options:     append([]string{}, f.Options...),
		Scope:       f.Scope,
		GuildScoped: f.GuildScoped,
	}
	if f.Min != nil {
		fr.Min = strconv.FormatFloat(*f.Min, 'f', -1, 64)
	}
	if f.Max != nil {
		fr.Max = strconv.FormatFloat(*f.Max, 'f', -1, 64)
	}
	if f.Step != nil {
		fr.Step = strconv.FormatFloat(*f.Step, 'f', -1, 64)
	}
	if guildID != "" && (f.Type == modules.FieldTypeChannel || f.Type == modules.FieldTypeRole) {
		if gid, err := snowflake.Parse(guildID); err == nil {
			if f.Type == modules.FieldTypeChannel {
				for ch := range m.client.Caches.ChannelsForGuild(gid) {
					fr.Entities = append(fr.Entities, entityOpt{ID: ch.ID().String(), Name: ch.Name()})
				}
			} else {
				for role := range m.client.Caches.Roles(gid) {
					fr.Entities = append(fr.Entities, entityOpt{ID: role.ID.String(), Name: role.Name})
				}
			}
			sort.Slice(fr.Entities, func(i, j int) bool { return fr.Entities[i].Name < fr.Entities[j].Name })
		}
	}
	return fr
}

// ── /permissions ──────────────────────────────────────────────────────────

func (m *DashboardModule) handlePermissionsPage(w http.ResponseWriter, r *http.Request) {
	d := m.baseData(sessionOf(r))
	d.Content = map[string]any{
		"elevated": m.permMgr().GetElevated(),
		"owner_id": m.ctx.Bot.GetOwnerID(),
	}
	m.tmpl.render(w, "permissions", d)
}

// ── /logs ──────────────────────────────────────────────────────────────────

func (m *DashboardModule) handleLogsPage(w http.ResponseWriter, r *http.Request) {
	lines, _ := tailLines(m.logFilePath(), 200)
	d := m.baseData(sessionOf(r))
	d.Content = map[string]any{
		"path":  m.logFilePath(),
		"lines": lines,
	}
	m.tmpl.render(w, "logs", d)
}
