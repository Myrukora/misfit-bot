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
	}}
	commandsGroups = append(commandsGroups, moduleGroup{Module: "core", Categories: []catGroup{cat}})
	commandsContent["groups"] = commandsGroups
	commandsContent["guild"] = "1"
	commandsContent["count"] = 3
	commandsContent["canRaw"] = true

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
		Core:          map[string]string{"prefix": "?", "name": "TestBot", "owner_id": "9", "status": "online", "tos_url": "", "privacy_url": "", "log_level": "info"},
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
