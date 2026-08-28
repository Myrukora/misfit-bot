package dashboard

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/disgoorg/snowflake/v2"
	"github.com/misfit/bot/modules"
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
	// GuildID is the guild context for THIS field's value: set for
	// guild-scoped fields when a server is selected, empty for global
	// fields (their entity pickers use the selected server only as a
	// lookup context — see populateEntities).
	GuildID   string
	OwnerOnly bool // true: only the bot owner may edit (secrets)
	Locked    bool // true: rendered read-only for this viewer (OwnerOnly && viewer is not the owner)
}

type moduleConfigView struct {
	Name   string
	Fields []fieldRender
}

// settingsSection groups core settings fields under a titled card.
type settingsSection struct {
	Title  string
	Help   string
	Fields []fieldRender
}

type settingsPageData struct {
	GuildID       string
	GuildName     string
	Sections      []settingsSection // core/global settings, grouped (nil for guild view)
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
	d.ShowSidebar = false // login is a standalone, centered card — no nav
	m.tmpl.render(w, "login", d)
}

// renderSetup shows the OAuth bootstrap instructions page.
func (m *DashboardModule) renderSetup(w http.ResponseWriter, r *http.Request) {
	d := m.baseData(sessionOf(r))
	d.ShowSidebar = false // setup is a standalone, centered card — no nav
	d.Content = map[string]string{
		"public_url":   m.effectivePublicURL(),
		"lan_url":      m.lanURL(),
		"client_id":    m.cfg.ClientID,
		"redirect_uri": m.redirectBaseURL(r) + "/callback", // must match what handleLoginStart sends
		"listen":       m.effectiveListen(),
		"prefix":       m.bot.GetPrefix(),
	}
	m.tmpl.render(w, "setup", d)
}

// ── /commands?guild=&raw= ──────────────────────────────────────────────────

func (m *DashboardModule) handleCommandsPage(w http.ResponseWriter, r *http.Request) {
	us := sessionOf(r)
	raw := r.URL.Query().Get("raw") == "true"
	guildID := r.URL.Query().Get("guild")
	level := m.resolveLevel(us)
	var views []cmdView
	if guildID != "" {
		views = m.filterCatalog(us, raw, true, guildID)
	} else {
		views = m.filterCatalog(us, raw, false, "")
	}
	d := m.baseData(us)
	d.Raw = raw
	content := map[string]any{
		"groups": groupCommands(views),
		"guild":  guildID,
		// Active category tab (module name); JS switches it client-side,
		// the server just picks the default so SSR renders the Core grid.
		"selectedTab": "core",
		"count":       len(views),
		"mode":        m.execMode(),
		"canRaw":      d.IsOwner || d.IsElevated,
		// canManage gates the staff-facing per-command gear modal: a staff member
		// can edit overrides only for guilds they manage.
		"canManage": levelGEQ(level, lvlStaff),
		// level feeds the gear modal so it can show the right scope controls
		// (owner sees global disable + mod-only; staff sees local-only).
		"level": level,
		// guilds feeds the per-command guild selector in the gear modal.
		"guilds": m.manageableGuildList(us),
	}
	// Picker entity lists for the modal's allowed-channel / allowed-role
	// multi-selects — populated only when a guild is selected AND the user
	// shares it with the bot, so cached entity names don't leak to members.
	if guildID != "" && m.canViewGuildEntities(us, guildID) {
		if detail, err := m.buildGuildDetail(guildID); err == nil {
			content["channels"] = detail.Channels
			roles := make([]entityOpt, 0, len(detail.Roles))
			for _, r := range detail.Roles {
				roles = append(roles, entityOpt{ID: r.ID, Name: r.Name})
			}
			content["roles"] = roles
		}
	}
	d.Content = content
	m.tmpl.render(w, "commands", d)
}

// manageableGuildList returns the guilds the user can manage as guildOpt rows,
// for rendering in the per-command gear modal's guild selector.
func (m *DashboardModule) manageableGuildList(us *userSession) []guildOpt {
	var guilds []guildOpt
	for _, id := range m.manageableGuildIDs(us) {
		if g := m.guildSummary(id, us); g != nil {
			guilds = append(guilds, *g)
		}
	}
	return guilds
}

// canViewGuildEntities reports whether the session may see a guild's cached
// entities (channels/roles/members) on the commands page: the user must share
// the guild with the bot.
func (m *DashboardModule) canViewGuildEntities(us *userSession, guildID string) bool {
	if us == nil {
		return false
	}
	for _, g := range m.mutualGuildIDs(us) {
		if g == guildID {
			return true
		}
	}
	return false
}

// maxMemberPicker caps the member dropdown rendered for a guild; beyond this
// the Run forms fall back to typing a user ID (a select with tens of
// thousands of options is worse than a text field).
const maxMemberPicker = 1000

// memberOpts lists a guild's cached members as picker options (sorted by name).
func (m *DashboardModule) memberOpts(guildID string) []entityOpt {
	if m.client == nil {
		return nil
	}
	gid, err := snowflake.Parse(guildID)
	if err != nil {
		return nil
	}
	var out []entityOpt
	for member := range m.client.Caches.Members(gid) {
		out = append(out, entityOpt{ID: member.User.ID.String(), Name: member.User.Username})
		if len(out) >= maxMemberPicker {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ── / (server picker) ─────────────────────────────────────────────────────

// guildPickerRow is one server card on the picker page.
type guildPickerRow struct {
	ID       string
	Name     string
	Icon     string
	Owner    bool
	IsOwner  bool // bot-level owner/elevated badge
	Commands bool // guild has any scoped pages worth showing
}

// handleServersPage renders the post-login landing: pick a server to manage.
// The bot-level admin panel link renders only for the bot owner (super owner).
func (m *DashboardModule) handleServersPage(w http.ResponseWriter, r *http.Request) {
	us := sessionOf(r)
	if us == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	level := m.resolveLevel(us)
	var rows []guildPickerRow
	for _, id := range m.mutualGuildIDs(us) {
		g := m.guildSummary(id, us)
		if g == nil {
			continue
		}
		rows = append(rows, guildPickerRow{
			ID:      g.ID,
			Name:    g.Name,
			Icon:    g.Icon,
			Owner:   g.Owner,
			IsOwner: level == lvlOwner || level == lvlElevated,
		})
	}
	d := m.baseData(us)
	d.Page = "servers"
	d.Content = map[string]any{
		"guilds":  rows,
		"level":   level,
		"isSuper": level == lvlOwner,
		"isElev":  level == lvlOwner || level == lvlElevated,
	}
	m.tmpl.render(w, "servers", d)
}

// ── /admin (super owner only) ─────────────────────────────────────────────

// handleAdminPage renders the bot-wide admin panel: every core-config surface
// that is NOT per-server — identity, logging, dashboard infra, updater,
// secrets and backups. Gated to lvlOwner (the configured owner_id) upstream.
func (m *DashboardModule) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	us := sessionOf(r)
	if us == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	data := adminPageData{}
	// Owner-only: full core sections (secrets unlocked), updater panel,
	// backups panel.
	data.Sections = m.coreSettingsFields(true, "", us)
	d := m.baseData(us)
	d.Page = "admin"
	d.Content = data
	m.tmpl.render(w, "admin", d)
}

// ── /g/<id>/{commands,tickets,guild,settings} (server-scoped) ─────────────

// handleGuildScopedPage dispatches one guild's dashboard page. Guild is fixed
// by the URL — no cross-guild selector on these pages.
func (m *DashboardModule) handleGuildScopedPage(w http.ResponseWriter, r *http.Request, guildID, sub string) {
	us := sessionOf(r)
	if us == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	switch sub {
	case "commands":
		m.renderGuildCommands(w, r, us, guildID)
	case "tickets":
		m.renderGuildTickets(w, r, us, guildID)
	case "modules":
		m.renderGuildModules(w, r, us, guildID)
	default:
		http.NotFound(w, r)
	}
}

// renderGuildCommands renders the commands grid pinned to one guild.
func (m *DashboardModule) renderGuildCommands(w http.ResponseWriter, r *http.Request, us *userSession, guildID string) {
	level := m.resolveLevel(us)
	views := m.filterCatalog(us, false, true, guildID)
	d := m.baseData(us)
	d.Page = "commands"
	content := map[string]any{
		"groups":      groupCommands(views),
		"guild":       guildID,
		"selectedTab": "core",
		"count":       len(views),
		"mode":        m.execMode(),
		"canRaw":      false,
		"canManage":   levelGEQ(level, lvlStaff),
		"level":       level,
		"guilds":      m.manageableGuildList(us),
	}
	if m.canViewGuildEntities(us, guildID) {
		if detail, err := m.buildGuildDetail(guildID); err == nil {
			content["channels"] = detail.Channels
			roles := make([]entityOpt, 0, len(detail.Roles))
			for _, ro := range detail.Roles {
				roles = append(roles, entityOpt{ID: ro.ID, Name: ro.Name})
			}
			content["roles"] = roles
		}
	}
	d.Content = content
	m.tmpl.render(w, "commands", d)
}

// renderGuildTickets renders the tickets list pinned to one guild (reuses the
// tickets page renderer with the guild preselected).
func (m *DashboardModule) renderGuildTickets(w http.ResponseWriter, r *http.Request, us *userSession, guildID string) {
	m.handleTicketsInGuild(w, r, us, guildID)
}

// renderGuildModules renders this guild's module settings panels.
func (m *DashboardModule) renderGuildModules(w http.ResponseWriter, r *http.Request, us *userSession, guildID string) {
	level := m.resolveLevel(us)
	data := settingsPageData{GuildID: guildID}
	if detail, err := m.buildGuildDetail(guildID); err == nil {
		data.GuildName = detail.Name
	}
	for _, name := range m.bot.GetLoadedModuleNames() {
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
	d.Page = "guildmodules"
	d.Content = data
	m.tmpl.render(w, "settings", d)
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
	if m.client == nil {
		return nil, fmt.Errorf("gateway client unavailable")
	}
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
	for _, n := range m.bot.GetLoadedModuleNames() {
		loaded[n] = true
	}
	var views []moduleView
	for _, n := range m.bot.GetAvailableModuleNames() {
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

	// "all" is the explicit opt-out sentinel: the toolbar's "All servers"
	// option navigates to ?guild=all, which means "no server selected"
	// (raw-ID fields). Without any param, auto-select the first manageable
	// server so the channel/role/user pickers have a context out of the box.
	if guildID == "all" {
		guildID = ""
	} else if guildID == "" {
		if mg := m.manageableGuildIDs(us); len(mg) > 0 {
			guildID = mg[0]
		}
	}

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
	}
	// Core/global sections render for owner/elevated on every view; the
	// selected server (if any) only powers the channel/role/user pickers.
	if level == lvlOwner || level == lvlElevated {
		data.Sections = m.coreSettingsFields(level == lvlOwner, guildID, us)
	}

	// Dashboard self-config + module configs: global fields always (owner/
	// elevated), guild-scoped fields merged in when a server is selected.
	if wc, ok := m.webCfg("dashboard"); ok {
		data.DashboardSelf = m.buildModuleView(wc, "dashboard", us, level, guildID)
	}

	for _, name := range m.bot.GetLoadedModuleNames() {
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

// ── /configuration ────────────────────────────────────────────────────────

// adminPageData is the payload for the super-owner admin panel: core bot
// config sections only (identity/logging/dashboard/updater/secrets) — no
// per-server pickers, no module panels.
type adminPageData struct {
	Sections []settingsSection
}

// ── core settings (schema-driven, grouped into sections) ────────────────

// coreSettingsFields renders every core bot setting as a typed, labeled field
// through the same schema-driven "field" partial the WebConfigurable module
// fields use. Fields are grouped into titled sections (bot / logging /
// dashboard / updater / secrets).
//
// Permission model (mirrors the Discord commands):
//   - owner + elevated: every field except the owner-only secrets
//   - owner only: bot token, updater token, OAuth client secret (locked for
//     elevated viewers — rendered disabled and skipped by the JS save)
//   - staff/regular: no core settings at all (the page is gated upstream)
//
// Values come from coreSettingsGet() so the JSON API and the page always
// agree. Deliberately NOT exposed (dead or misleading in the UI):
//   - log_channel: Discord channel logging was never implemented (file-only)
//   - name: the header shows the live Discord name; config bot.name is only a
//     bridge/fallback value (streaming presence URL, module contexts)
//   - status: bot.status is never read by the bot — presence is live
//   - elevated_ids: managed on the dedicated /permissions page
func (m *DashboardModule) coreSettingsFields(owner bool, guildID string, us *userSession) []settingsSection {
	vals := m.coreSettingsGet()
	lock := func(f fieldRender) fieldRender {
		f.Locked = f.OwnerOnly && !owner
		return f
	}
	sec := func(title, help string, fs ...fieldRender) settingsSection {
		for i := range fs {
			fs[i] = lock(fs[i])
			// Pickers (channel/role/user) populate from the selected server
			// (gated on shared-guild membership in populateEntities).
			m.populateEntities(&fs[i], guildID, us)
		}
		return settingsSection{Title: title, Help: help, Fields: fs}
	}
	return []settingsSection{
		sec("Bot", "Identity, ownership and the links shown by the info command.",
			fieldRender{Key: "prefix", Label: "Command prefix", Help: "Prefix for text commands. Cannot be empty.", Type: "text", Value: vals["prefix"], Placeholder: "?"},
			fieldRender{Key: "owner_id", Label: "Owner ID", Help: "Discord user ID of the bot owner. The owner bypasses every permission check. Pick from the selected server's members or type the ID. Owner only — elevated users cannot transfer ownership.", Type: "user", Value: vals["owner_id"], Placeholder: "123456789012345678", OwnerOnly: true},
			fieldRender{Key: "tos_url", Label: "Terms of Service URL", Help: "Shown by the info command.", Type: "text", Value: vals["tos_url"], Placeholder: "https://example.com/tos"},
			fieldRender{Key: "privacy_url", Label: "Privacy Policy URL", Help: "Shown by the info command.", Type: "text", Value: vals["privacy_url"], Placeholder: "https://example.com/privacy"},
			fieldRender{Key: "status", Label: "Presence status", Help: "Status shown alongside the activity (online, idle, dnd, invisible). Persisted and applied on every restart. The live activity type/text is set from the Configuration tab.", Type: "select", Value: vals["status"], Options: []string{"", "online", "idle", "dnd", "invisible"}},
		),
		sec("Logging", "File logging (JSON, daily rotation).",
			fieldRender{Key: "log_level", Label: "Log level", Help: "Verbosity of the log file: filters which levels get written. debug = everything, error = only failures. Restart required.", Type: "select", Value: vals["log_level"], Options: []string{"debug", "info", "warn", "error"}},
			fieldRender{Key: "log_enabled", Label: "File logging", Help: "Whether the bot writes logs to disk. Restart required.", Type: "toggle", Value: vals["log_enabled"]},
			fieldRender{Key: "log_file_path", Label: "Log file path", Help: "Where logs are written; the rotating writer appends a -YYYY-MM-DD suffix. Restart required.", Type: "text", Value: vals["log_file_path"], Placeholder: "logs/bot.log"},
		),
		sec("Dashboard", "The web dashboard itself — this page. Changes apply immediately.",
			fieldRender{Key: "dashboard_listen", Label: "Listen address", Help: "HTTP bind address. Empty = default 127.0.0.1:8080. Use 0.0.0.0:9090 to accept LAN connections.", Type: "text", Value: vals["dashboard_listen"], Placeholder: "127.0.0.1:8080"},
			fieldRender{Key: "dashboard_public_url", Label: "Public URL", Help: "Public base URL for the OAuth redirect (tunnel/reverse proxy). Empty = derived from the browser origin.", Type: "text", Value: vals["dashboard_public_url"], Placeholder: "https://dashboard.example.com"},
		),
		sec("Updater", "Self-update from the bot's GitHub repository. Actions (check / update now / test embeds) are owner-only and live under the status panel.",
			fieldRender{Key: "updater_enabled", Label: "Enabled", Help: "Master switch for the self-updater.", Type: "toggle", Value: vals["updater_enabled"]},
			fieldRender{Key: "updater_repo", Label: "Repository", Help: "GitHub repo in owner/name form. Empty = updater off.", Type: "text", Value: vals["updater_repo"], Placeholder: "Myrukora/misfit-bot"},
			fieldRender{Key: "updater_branch", Label: "Branch", Help: "Branch to track.", Type: "text", Value: vals["updater_branch"], Placeholder: "main"},
			fieldRender{Key: "updater_interval", Label: "Check interval (seconds)", Help: "How often the updater polls. Minimum 30.", Type: "number", Value: vals["updater_interval"], Min: "30", Step: "1", Placeholder: "300"},
			fieldRender{Key: "updater_auto_pull", Label: "Auto pull", Help: "Automatically pull, rebuild and restart on new commits.", Type: "toggle", Value: vals["updater_auto_pull"]},
			fieldRender{Key: "updater_notify_channel", Label: "Notify channel", Help: "Discord channel for PR/commit embeds. Pick from the selected server; empty = no notifications.", Type: "channel", Value: vals["updater_notify_channel"], Placeholder: "channel ID or empty"},
			fieldRender{Key: "updater_token", Label: "GitHub token", Help: "PAT used to fetch from GitHub. Never displayed — leave blank to keep the current value.", Type: "secret", Value: vals["updater_token"], OwnerOnly: true},
		),
		sec("Secrets", "Credentials. Owner only — elevated users see them locked.",
			fieldRender{Key: "token", Label: "Bot token", Help: "Discord bot token from the Developer Portal. Never displayed — leave blank to keep the current value. Restart required.", Type: "secret", Value: vals["token"], OwnerOnly: true},
			fieldRender{Key: "oauth_client_secret", Label: "OAuth client secret", Help: "Discord app OAuth2 secret (shared with the dashboard login). Never displayed — leave blank to keep the current value.", Type: "secret", Value: vals["oauth_client_secret"], OwnerOnly: true},
		),
	}
}

// buildModuleView produces the filtered, redacted field renders for a module.
// Global fields always render for owner/elevated (per-field GuildID = "");
// guild-scoped fields render when a server is selected. This keeps every
// configurable field visible on one page regardless of the picker context.
func (m *DashboardModule) buildModuleView(wc modules.WebConfigurable, name string, us *userSession, level, guildID string) moduleConfigView {
	mv := moduleConfigView{Name: name}
	// Global fields: owner/elevated only (mirrors moduleConfigRead's gate).
	if level == lvlOwner || level == lvlElevated {
		gvals, err := m.moduleConfigRead(wc, us, "", level)
		if err != nil {
			m.logger.Warn("dashboard: read global config of module %s failed: %v", name, err)
			gvals = map[string]string{}
		}
		for _, f := range wc.WebConfigSchema() {
			if f.GuildScoped {
				continue
			}
			fr := m.buildFieldRender(f, gvals[f.Key], "")
			// Global picker types populate from the selected server as a
			// lookup context only — the submitted GuildID stays empty.
			m.populateEntities(&fr, guildID, us)
			mv.Fields = append(mv.Fields, fr)
		}
	}
	// Guild-scoped fields: rendered with the selected server as context.
	if guildID != "" {
		gvals, err := m.moduleConfigRead(wc, us, guildID, level)
		if err != nil {
			m.logger.Warn("dashboard: read guild config of module %s failed: %v", name, err)
			gvals = map[string]string{}
		}
		for _, f := range wc.WebConfigSchema() {
			if !f.GuildScoped {
				continue
			}
			fr := m.buildFieldRender(f, gvals[f.Key], guildID)
			m.populateEntities(&fr, guildID, us)
			mv.Fields = append(mv.Fields, fr)
		}
	}
	return mv
}

// buildFieldRender turns a ConfigField + current value into a render-friendly
// field. NOTE: entity pickers are NOT populated here — callers populate them
// explicitly with the picker context (global fields render with the selected
// server's entities as a lookup context while submitting an empty GuildID).
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
	if f.GuildScoped {
		fr.GuildID = guildID
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
	return fr
}

// populateEntities fills channel/role/user pickers from the guild cache when a
// guild context is set; without one the field falls back to a text input.
// Entity lists are only enumerated for guilds the session SHARES with the bot
// (canViewGuildEntities) — management rights alone must not leak cached
// channel/role/member names of servers the user isn't in.
func (m *DashboardModule) populateEntities(fr *fieldRender, guildID string, us *userSession) {
	if guildID == "" || m.client == nil || us == nil {
		return
	}
	if !m.canViewGuildEntities(us, guildID) {
		return
	}
	gid, err := snowflake.Parse(guildID)
	if err != nil {
		return
	}
	switch fr.Type {
	case modules.FieldTypeChannel:
		for ch := range m.client.Caches.ChannelsForGuild(gid) {
			fr.Entities = append(fr.Entities, entityOpt{ID: ch.ID().String(), Name: ch.Name()})
		}
	case modules.FieldTypeRole:
		for role := range m.client.Caches.Roles(gid) {
			fr.Entities = append(fr.Entities, entityOpt{ID: role.ID.String(), Name: role.Name})
		}
	case modules.FieldTypeUser:
		for member := range m.client.Caches.Members(gid) {
			fr.Entities = append(fr.Entities, entityOpt{ID: member.User.ID.String(), Name: member.User.Username})
			if len(fr.Entities) >= maxMemberPicker {
				break
			}
		}
	}
	sort.Slice(fr.Entities, func(i, j int) bool { return fr.Entities[i].Name < fr.Entities[j].Name })
}

// ── /permissions ──────────────────────────────────────────────────────────

// resolveUsernames maps user IDs to display names, cache-first. The member
// cache is enumerated once and reused for the whole request (per-request memo,
// no per-ID scans). IDs absent from the cache stay raw — the REST fallback the
// plan mentions is skipped: a private bot's elevated list is always in cache.
func (m *DashboardModule) resolveUsernames(ids []string) map[string]string {
	out := make(map[string]string, len(ids))
	if m.client == nil || len(ids) == 0 {
		return out
	}
	byID := make(map[string]string, 64)
	for g := range m.client.Caches.Guilds() {
		for member := range m.client.Caches.Members(g.ID) {
			byID[member.User.ID.String()] = member.User.Username
		}
	}
	for _, id := range ids {
		if name, ok := byID[id]; ok {
			out[id] = name
		}
	}
	return out
}

func (m *DashboardModule) handlePermissionsPage(w http.ResponseWriter, r *http.Request) {
	elevated := m.permMgr().GetElevated()
	names := m.resolveUsernames(append([]string{m.bot.GetOwnerID()}, elevated...))
	d := m.baseData(sessionOf(r))
	d.Content = map[string]any{
		"elevated": elevated,
		"owner_id": m.bot.GetOwnerID(),
		"names":    names,
	}
	m.tmpl.render(w, "permissions", d)
}

// ── /logs ──────────────────────────────────────────────────────────────────

func (m *DashboardModule) handleLogsPage(w http.ResponseWriter, r *http.Request) {
	path := m.logFilePath()
	lines, err := tailLines(path, 200)
	note := ""
	if err != nil {
		lines, note = nil, "no log file yet — is file logging enabled? (logging.enabled)"
	}
	d := m.baseData(sessionOf(r))
	d.Content = map[string]any{"path": path, "lines": lines, "note": note}
	m.tmpl.render(w, "logs", d)
}
