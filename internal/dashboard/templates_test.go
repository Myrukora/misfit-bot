package dashboard

import (
	"strings"
	"testing"
)

func mkData(level string) renderData {
	return renderData{
		Bot:        "TestBot",
		Level:      level,
		Guilds:     []guildOpt{{ID: "1", Name: "Guild", Owner: true}},
		CSRF:       "csrf-test",
		User:       &userJSON{ID: "u", Username: "owner", Avatar: "https://cdn/x.png"},
		IsOwner:    level == lvlOwner,
		IsElevated: level == lvlElevated,
		IsStaff:    level == lvlStaff,
		IsRegular:  level == lvlRegular,
		ShowConfig: level == lvlOwner || level == lvlElevated,
		ShowStaff:  level == lvlOwner || level == lvlElevated || level == lvlStaff,
	}
}

func TestTemplatesParseAndRender(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}

	commandsContent := map[string]any{}
	commandsGroups := []moduleGroup{}
	cat := catGroup{Name: "general", Commands: []cmdView{
		{Name: "ping", Description: "pong", Category: "general", ModuleOwner: "core", Kind: "prefix", Usable: true, UsableIn: []string{"1"}, Aliases: []string{"p"}},
		{Name: "secret", Description: "owner-only", Category: "core", ModuleOwner: "core", Kind: "prefix", OwnerOnly: true, SuperOwnerOnly: true, Usable: true},
		{Name: "settings", Description: "x", Category: "core", ModuleOwner: "core", Kind: "slash"},
		{Name: "cleanup", Description: "clean", Category: "cleanup", ModuleOwner: "cleanup", Kind: "prefix", Usable: true, Options: []argOpt{
			{Name: "subcommand", Type: "string", Required: true, Choices: []string{"messages", "user"}},
			{Name: "count", Type: "int"},
			{Name: "target", Type: "user"},
		}},
	}}
	commandsGroups = append(commandsGroups, moduleGroup{Module: "core", Categories: []catGroup{cat}})
	commandsContent["groups"] = commandsGroups
	commandsContent["guild"] = "1"
	commandsContent["count"] = 4
	commandsContent["mode"] = "prefix"
	commandsContent["canRaw"] = true
	commandsContent["channels"] = []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "staff"}}
	commandsContent["roles"] = []entityOpt{{ID: "r1", Name: "@everyone"}}
	commandsContent["members"] = []entityOpt{{ID: "u1", Name: "sam"}}

	guildContent := &guildDetail{
		ID: "1", Name: "G", OwnerID: "2", MemberCount: 9,
		Channels: []entityOpt{{ID: "c", Name: "general", Type: "Text"}},
		Roles:    []roleOpt{{ID: "r", Name: "@everyone", Color: 0, Position: 0}}, BotPerms: "Administrator",
	}

	cases := []struct {
		page    string
		content any
	}{
		{"login", nil},
		{"setup", map[string]string{
			"public_url": "https://x.com", "lan_url": "http://192.168.1.5:8080", "client_id": "111", "redirect_uri": "https://x.com/callback",
			"listen": "127.0.0.1:8080", "prefix": "?",
		}},
		{"index", metricsSnapshot{Runtime: map[string]any{"alloc_mb": uint64(1), "goroutines": 5}, Modules: []string{}}},
		{"commands", map[string]any{"groups": []moduleGroup{}, "guild": "", "count": 0, "canRaw": true}},
		{"commands", commandsContent},
		{"guild", guildContent},
		{"modules", []moduleView{{Name: "cleanup", Loaded: true, Description: "d"}}},
		{"permissions", map[string]any{"elevated": []string{"123"}, "owner_id": "9", "names": map[string]string{"123": "sam", "9": "owner"}}},
		{"logs", map[string]any{"path": "logs/bot.log", "lines": []string{"line1", "line2"}}},
		// Server picker: guild cards + super-owner admin links.
		{"servers", map[string]any{"guilds": []guildPickerRow{{ID: "1", Name: "G", Owner: true}}, "level": lvlOwner, "isSuper": true, "isElev": true}},
		{"servers", map[string]any{"guilds": []guildPickerRow{}, "level": lvlStaff, "isSuper": false, "isElev": false}},
		// Admin panel: core sections incl. presence status + secrets.
		{"admin", adminPageData{Sections: []settingsSection{
			{Title: "Bot", Help: "h", Fields: []fieldRender{
				{Key: "prefix", Label: "Prefix", Type: "text", Value: "[p]"},
				{Key: "status", Label: "Presence status", Type: "select", Value: "online", Options: []string{"", "online", "idle", "dnd", "invisible"}},
			}},
			{Title: "Secrets", Fields: []fieldRender{{Key: "token", Label: "Bot token", Type: "secret", OwnerOnly: true}}},
		}}},
		// Guild-scoped module settings reuse the settings template.
		{"settings", settingsPageData{GuildID: "1", GuildName: "G", Modules: []moduleConfigView{{Name: "tickets", Fields: []fieldRender{{Key: "t1", Label: "T", Type: "toggle", Value: "true"}}}}}},
	}

	for i, c := range cases {
		d := mkData(lvlOwner)
		d.Content = c.content
		var sb strings.Builder
		if err := b.render(&sb, c.page, d); err != nil {
			t.Errorf("render %s (#%d): %v", c.page, i, err)
		}
	}
}

func TestSettingsFieldEveryType(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	fields := []fieldRender{
		{Key: "t1", Label: "Toggle", Type: "toggle", Value: "true"},
		{Key: "t2", Label: "Text", Type: "text", Value: "hi", Placeholder: "type"},
		{Key: "t3", Label: "Textarea", Type: "textarea", Value: "multi\nline"},
		{Key: "t4", Label: "Number", Type: "number", Value: "3", Min: "0", Max: "10", Step: "1"},
		{Key: "t5", Label: "Range", Type: "range", Value: "50", Min: "0", Max: "100", Step: "5"},
		{Key: "t6", Label: "Select", Type: "select", Value: "b", Options: []string{"a", "b", "c"}},
		{Key: "t7", Label: "Multi", Type: "multi", Value: "a,c", Options: []string{"a", "b", "c"}},
		{Key: "t8", Label: "Secret", Type: "secret", Value: "••••••••"},
		{Key: "t9", Label: "Channel", Type: "channel", Value: "c", Entities: []entityOpt{{ID: "c", Name: "general"}}},
		{Key: "t10", Label: "Role", Type: "role", Value: "r", Entities: []entityOpt{{ID: "r", Name: "@everyone"}}},
	}
	// Render the shared field partial directly to cover every branch.
	for i, fr := range fields {
		var sb strings.Builder
		if err := b.tmpl.ExecuteTemplate(&sb, "field", fr); err != nil {
			t.Errorf("field partial #%d (%s): %v", i, fr.Type, err)
		}
	}
	content := settingsPageData{
		Sections: []settingsSection{{
			Title: "Bot",
			Fields: []fieldRender{
				{Key: "prefix", Label: "Command prefix", Type: "text", Value: "?"},
				{Key: "owner_id", Label: "Owner ID", Type: "text", Value: "9"},
				{Key: "log_level", Label: "Log level", Type: "select", Value: "info", Options: []string{"debug", "info", "warn", "error"}},
				{Key: "log_enabled", Label: "File logging", Type: "toggle", Value: "true"},
				{Key: "tos_url", Label: "Terms of Service URL", Type: "text", Value: ""},
				{Key: "privacy_url", Label: "Privacy Policy URL", Type: "text", Value: ""},
			},
		}},
		DashboardSelf: moduleConfigView{Name: "dashboard", Fields: fields},
	}
	d := mkData(lvlOwner)
	d.Content = content
	var sb strings.Builder
	if err := b.render(&sb, "settings", d); err != nil {
		t.Fatalf("render settings: %v", err)
	}
	if !strings.Contains(sb.String(), "Dashboard (self-config)") {
		t.Errorf("settings page missing dashboard self-config section")
	}
}

// TestSettingsSectionsPins covers the settings page restructure: five core
// sections render with their titles; secret fields are enabled for the owner
// and locked (disabled + data-owneronly) for elevated viewers; the updater
// status panel renders only for the owner.
func TestSettingsSectionsPins(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	// locked=true renders secrets locked (the elevated view); the owner
	// render passes false (OwnerOnly && !owner is computed server-side in
	// coreSettingsFields).
	build := func(locked bool) settingsPageData {
		return settingsPageData{
			Sections: []settingsSection{
				{Title: "Bot", Fields: []fieldRender{{Key: "prefix", Label: "Command prefix", Type: "text", Value: "?"}}},
				{Title: "Logging", Fields: []fieldRender{{Key: "log_enabled", Label: "File logging", Type: "toggle", Value: "true"}}},
				{Title: "Dashboard", Fields: []fieldRender{{Key: "dashboard_listen", Label: "Listen address", Type: "text", Value: ""}}},
				{Title: "Updater", Fields: []fieldRender{{Key: "updater_enabled", Label: "Enabled", Type: "toggle", Value: "true"}}},
				{Title: "Secrets", Fields: []fieldRender{
					{Key: "token", Label: "Bot token", Type: "secret", Value: "••••••••", OwnerOnly: true, Locked: locked},
					{Key: "oauth_client_secret", Label: "OAuth client secret", Type: "secret", Value: "••••••••", OwnerOnly: true, Locked: locked},
				}},
			},
		}
	}
	render := func(d renderData) string {
		t.Helper()
		var sb strings.Builder
		if err := b.render(&sb, "settings", d); err != nil {
			t.Fatalf("render settings: %v", err)
		}
		return sb.String()
	}

	owner := mkData(lvlOwner)
	owner.Content = build(false)
	out := render(owner)
	for _, title := range []string{"Bot", "Logging", "Dashboard", "Updater", "Secrets"} {
		if !strings.Contains(out, "<h3>"+title+"</h3>") {
			t.Errorf("owner render missing section %q", title)
		}
	}
	if strings.Contains(out, `data-owneronly="true"`) {
		t.Error("owner render must not lock any field")
	}
	if !strings.Contains(out, "scope-owner") {
		t.Error("owner render must show the owner scope badge on secrets")
	}
	if !strings.Contains(out, "updater-status") {
		t.Error("owner render must include the updater status panel")
	}
	if !strings.Contains(out, `id="upd-apply"`) {
		t.Error("owner render must include the updater action buttons")
	}

	elevated := mkData(lvlElevated)
	elevated.Content = build(true)
	eOut := render(elevated)
	if !strings.Contains(eOut, `data-owneronly="true"`) {
		t.Error("elevated render must mark owner-only fields data-owneronly")
	}
	if !strings.Contains(eOut, `disabled`) {
		t.Error("elevated render must disable owner-only inputs")
	}
	if strings.Contains(eOut, "updater-status") {
		t.Error("elevated render must NOT include the updater panel (owner only)")
	}
	if strings.Contains(eOut, `id="upd-apply"`) {
		t.Error("elevated render must not show updater action buttons")
	}
}

// TestScopedSidebar pins the per-server scoped sidebar: when GuildID is set,
// the header renders the server-scoped nav (server name + back-to-servers +
// Commands/Tickets/Modules/Server info) instead of the global nav.
func TestScopedSidebar(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	d := mkData(lvlOwner)
	d.ShowSidebar = true
	d.Page = "commands"
	d.GuildID = "123"
	d.GuildName = "My Server"
	d.Content = map[string]any{"groups": []moduleGroup{}, "guild": "123", "count": 0, "canRaw": true}
	var sb strings.Builder
	if err := b.render(&sb, "commands", d); err != nil {
		t.Fatalf("render commands (scoped): %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, `guild-context-name">My Server`) {
		t.Error("scoped sidebar missing server name")
	}
	if !strings.Contains(out, `guild-context-back`) {
		t.Error("scoped sidebar missing back-to-servers link")
	}
	for _, link := range []string{`/g/123/commands`, `/g/123/tickets`, `/g/123/modules`, `/guild/123`} {
		if !strings.Contains(out, link) {
			t.Errorf("scoped sidebar missing %s link", link)
		}
	}
	// Global nav must be hidden on per-server pages.
	if strings.Contains(out, `href="/" class="nav-item`) {
		t.Error("global Servers nav must be hidden on per-server pages")
	}
	if strings.Contains(out, `>Administration</a>`) {
		t.Error("global Administration nav must be hidden on per-server pages")
	}

	// Top-level pages (GuildID empty) keep the global sidebar.
	d2 := mkData(lvlOwner)
	d2.ShowSidebar = true
	d2.Page = "commands"
	d2.Content = map[string]any{"groups": []moduleGroup{}, "guild": "", "count": 0, "canRaw": true}
	var sb2 strings.Builder
	if err := b.render(&sb2, "commands", d2); err != nil {
		t.Fatalf("render commands (global): %v", err)
	}
	out2 := sb2.String()
	if strings.Contains(out2, `guild-context-name`) {
		t.Error("top-level page must not render the scoped sidebar")
	}
	if !strings.Contains(out2, `href="/" class="nav-item`) {
		t.Error("top-level page must keep the global Servers nav")
	}
}

// TestGuildPageActiveNav pins the Server info nav state: on /guild/<id>
// (Page "guild") the scoped sidebar's Server info link is active, and on
// other scoped pages it is not.
func TestGuildPageActiveNav(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	d := mkData(lvlOwner)
	d.ShowSidebar = true
	d.Page = "guild"
	d.GuildID = "123"
	d.GuildName = "My Server"
	d.Content = &guildDetail{ID: "123", Name: "My Server", MemberCount: 5, OwnerID: "1", BotPerms: "ManageGuild"}
	var sb strings.Builder
	if err := b.render(&sb, "guild", d); err != nil {
		t.Fatalf("render guild: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, `href="/guild/123" class="nav-item active"`) {
		t.Error("Server info link must be active on the guild page")
	}
	if !strings.Contains(out, `title="Server info" aria-current="page"`) {
		t.Error("Server info link must carry aria-current on the guild page")
	}

	// On another scoped page the Server info link is not active.
	d2 := mkData(lvlOwner)
	d2.ShowSidebar = true
	d2.Page = "commands"
	d2.GuildID = "123"
	d2.GuildName = "My Server"
	d2.Content = map[string]any{"groups": []moduleGroup{}, "guild": "123", "count": 0, "canRaw": true}
	var sb2 strings.Builder
	if err := b.render(&sb2, "commands", d2); err != nil {
		t.Fatalf("render commands (scoped): %v", err)
	}
	if strings.Contains(sb2.String(), `href="/guild/123" class="nav-item active"`) {
		t.Error("Server info link must not be active on other scoped pages")
	}
}

// TestServersPageBotWideSections pins the bot-wide config sections on the
// /servers page for the super owner (and their absence for non-super users).
func TestServersPageBotWideSections(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	d := mkData(lvlOwner)
	d.Page = "servers"
	d.Content = map[string]any{
		"guilds":  []guildPickerRow{{ID: "1", Name: "G", Owner: true}},
		"level":   lvlOwner,
		"isSuper": true,
		"isElev":  true,
		"adminSections": []settingsSection{
			{Title: "Bot", Fields: []fieldRender{{Key: "prefix", Label: "Command prefix", Type: "text", Value: "?"}}},
			{Title: "Secrets", Fields: []fieldRender{{Key: "token", Label: "Bot token", Type: "secret", OwnerOnly: true}}},
		},
	}
	var sb strings.Builder
	if err := b.render(&sb, "servers", d); err != nil {
		t.Fatalf("render servers: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, "Bot-wide configuration") {
		t.Error("servers page missing bot-wide configuration heading")
	}
	if !strings.Contains(out, `<h3>Bot</h3>`) {
		t.Error("servers page missing Bot section")
	}
	if !strings.Contains(out, `<h3>Secrets</h3>`) {
		t.Error("servers page missing Secrets section")
	}
	if !strings.Contains(out, `id="bk-create"`) {
		t.Error("servers page missing Backups card")
	}
	if !strings.Contains(out, `id="upd-check"`) {
		t.Error("servers page missing Updater status card")
	}

	// Non-super users must NOT see the bot-wide sections.
	d2 := mkData(lvlStaff)
	d2.Page = "servers"
	d2.Content = map[string]any{
		"guilds":  []guildPickerRow{{ID: "1", Name: "G", Owner: true}},
		"level":   lvlStaff,
		"isSuper": false,
		"isElev":  false,
	}
	var sb2 strings.Builder
	if err := b.render(&sb2, "servers", d2); err != nil {
		t.Fatalf("render servers (staff): %v", err)
	}
	if strings.Contains(sb2.String(), "Bot-wide configuration") {
		t.Error("staff render must NOT show bot-wide configuration")
	}
}

// TestCommandsRunAffordance pins the Run button on usable commands (never on SuperOwnerOnly ones).
func TestCommandsRunAffordance(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	groups := []moduleGroup{{
		Module: "core",
		Categories: []catGroup{{Name: "general", Commands: []cmdView{
			{Name: "ping", Description: "pong", Category: "general", ModuleOwner: "core", Kind: "prefix", Usable: true, CanExec: true},
			{Name: "secret", Description: "owner-only", Category: "core", ModuleOwner: "core", Kind: "prefix", OwnerOnly: true, SuperOwnerOnly: true, Usable: true, CanExec: true},
			{Name: "locked", Description: "x", Category: "core", ModuleOwner: "core", Kind: "prefix", Usable: false},
		}}},
	}}
	d := mkData(lvlOwner)
	d.ShowSidebar = true
	d.Content = map[string]any{"groups": groups, "guild": "1", "count": 3, "canRaw": true}
	var sb strings.Builder
	if err := b.render(&sb, "commands", d); err != nil {
		t.Fatalf("render commands: %v", err)
	}
	out := sb.String()
	// Run affordance is the .run-cmd button, which carries data-name + data-guild.
	if !strings.Contains(out, `run-cmd" data-name="ping"`) {
		t.Error("usable command missing Run affordance")
	}
	if strings.Contains(out, `run-cmd" data-name="secret"`) {
		t.Error("SuperOwnerOnly command must never render a Run affordance")
	}
	if strings.Contains(out, `run-cmd" data-name="locked"`) {
		t.Error("non-usable command must not render a Run affordance")
	}
	if !strings.Contains(out, `data-guild="1"`) {
		t.Error("Run affordance must carry the page's guild context")
	}
}
