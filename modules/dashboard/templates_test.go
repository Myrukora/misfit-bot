package main

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
		{Name: "eval", Description: "shell", Category: "core", ModuleOwner: "core", Kind: "prefix", OwnerOnly: true, SuperOwnerOnly: true, Usable: true},
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
		{"permissions", map[string]any{"elevated": []string{"123"}, "owner_id": "9"}},
		{"logs", map[string]any{"path": "logs/bot.log", "lines": []string{"line1", "line2"}}},
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
	// owner=true renders secrets unlocked; elevated locks them (OwnerOnly &&
	// !owner is computed server-side in coreSettingsFields).
	build := func(owner bool) settingsPageData {
		lock := owner
		return settingsPageData{
			Sections: []settingsSection{
				{Title: "Bot", Fields: []fieldRender{{Key: "prefix", Label: "Command prefix", Type: "text", Value: "?"}}},
				{Title: "Logging", Fields: []fieldRender{{Key: "log_enabled", Label: "File logging", Type: "toggle", Value: "true"}}},
				{Title: "Dashboard", Fields: []fieldRender{{Key: "dashboard_listen", Label: "Listen address", Type: "text", Value: ""}}},
				{Title: "Updater", Fields: []fieldRender{{Key: "updater_enabled", Label: "Enabled", Type: "toggle", Value: "true"}}},
				{Title: "Secrets", Fields: []fieldRender{
					{Key: "token", Label: "Bot token", Type: "secret", Value: "••••••••", OwnerOnly: true, Locked: lock},
					{Key: "oauth_client_secret", Label: "OAuth client secret", Type: "secret", Value: "••••••••", OwnerOnly: true, Locked: lock},
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

// TestCommandsRunAffordance pins the Run button on usable (non-eval) commands.
func TestCommandsRunAffordance(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	groups := []moduleGroup{{
		Module: "core",
		Categories: []catGroup{{Name: "general", Commands: []cmdView{
			{Name: "ping", Description: "pong", Category: "general", ModuleOwner: "core", Kind: "prefix", Usable: true},
			{Name: "eval", Description: "shell", Category: "core", ModuleOwner: "core", Kind: "prefix", OwnerOnly: true, SuperOwnerOnly: true, Usable: true},
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
	if !strings.Contains(out, `data-command="ping"`) {
		t.Error("usable command missing Run affordance")
	}
	if strings.Contains(out, `data-command="eval"`) {
		t.Error("SuperOwnerOnly command must never render a Run affordance")
	}
	if strings.Contains(out, `data-command="locked"`) {
		t.Error("non-usable command must not render a Run affordance")
	}
	if !strings.Contains(out, `data-guild="1"`) {
		t.Error("Run affordance must carry the page's guild context")
	}
}
