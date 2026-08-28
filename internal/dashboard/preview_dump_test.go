package dashboard

import (
	"os"
	"strings"
	"testing"
)

func mkServers() renderData {
	d := mkData(lvlOwner)
	d.Page = "servers"
	d.Content = map[string]any{
		"guilds": []guildPickerRow{
			{ID: "111111111111111111", Name: "Misfit's Tavern", Owner: true},
			{ID: "222222222222222222", Name: "Dev Sandbox"},
		},
		"level":   lvlOwner,
		"isSuper": true,
		"isElev":  true,
	}
	return d
}

func mkAdmin() renderData {
	d := mkData(lvlOwner)
	d.Page = "admin"
	d.Content = adminPageData{Sections: []settingsSection{
		{Title: "Bot", Help: "Identity, ownership and the links shown by the info command.", Fields: []fieldRender{
			{Key: "prefix", Label: "Command prefix", Help: "Prefix for text commands.", Type: "text", Value: "[p]"},
			{Key: "owner_id", Label: "Owner ID", Type: "user", Value: "123456789012345678", OwnerOnly: true},
			{Key: "tos_url", Label: "Terms of Service URL", Type: "text", Value: "https://example.com/tos"},
			{Key: "privacy_url", Label: "Privacy Policy URL", Type: "text", Value: ""},
		}},
		{Title: "Logging", Help: "File logging (JSON, daily rotation).", Fields: []fieldRender{
			{Key: "log_level", Label: "Log level", Type: "select", Value: "info", Options: []string{"debug", "info", "warn", "error"}},
			{Key: "log_enabled", Label: "File logging", Type: "toggle", Value: "true"},
			{Key: "log_file_path", Label: "Log file path", Type: "text", Value: "logs/bot.log"},
		}},
		{Title: "Updater", Help: "Self-update from GitHub.", Fields: []fieldRender{
			{Key: "updater_enabled", Label: "Enabled", Type: "toggle", Value: "true"},
			{Key: "updater_repo", Label: "Repository", Type: "text", Value: "Myrukora/misfit-bot"},
			{Key: "updater_branch", Label: "Branch", Type: "text", Value: "main"},
			{Key: "updater_interval", Label: "Check interval (seconds)", Type: "number", Value: "300", Min: "30", Step: "1"},
			{Key: "updater_auto_pull", Label: "Auto pull", Type: "toggle", Value: "true"},
		}},
		{Title: "Secrets", Help: "Credentials. Owner only.", Fields: []fieldRender{
			{Key: "token", Label: "Bot token", Type: "secret", OwnerOnly: true},
			{Key: "oauth_client_secret", Label: "OAuth client secret", Type: "secret", OwnerOnly: true},
		}},
	}}
	return d
}

func mkOverview() renderData {
	d := mkData(lvlOwner)
	d.Page = "index"
	d.Content = metricsSnapshot{
		Guilds: 1, Members: 1284, Channels: 42, Roles: 18,
		Latency: "43ms", Uptime: "3d4h12m",
		ModulesLoaded: 4, ModulesAvail: 4, Commands: 47,
		Modules: []string{"cleanup", "dashboard", "tickets"},
		Runtime: map[string]any{"alloc_mb": uint64(96), "goroutines": 87, "gc_cycles": uint32(1421), "go_version": "go1.26.4"},
	}
	return d
}

func mkCommands() renderData {
	d := mkData(lvlOwner)
	d.Page = "commands"
	cat := catGroup{Name: "general", Commands: []cmdView{
		{Name: "ping", Description: "Check bot latency", Kind: "prefix", ModuleOwner: "core", Usable: true, CanExec: true},
		{Name: "help", Kind: "prefix", ModuleOwner: "core", Usable: true},
		{Name: "reload", Kind: "prefix", ModuleOwner: "core", OwnerOnly: true, Usable: true},
	}}
	second := catGroup{Name: "moderation", Commands: []cmdView{
		{Name: "cleanup", Description: "Bulk delete messages", Kind: "prefix", ModuleOwner: "cleanup", Usable: true, HasGuildOverride: true},
	}}
	d.Content = map[string]any{
		"groups":      []moduleGroup{{Module: "core", Categories: []catGroup{cat}}, {Module: "cleanup", Categories: []catGroup{second}}},
		"guild":       "111111111111111111",
		"selectedTab": "core",
		"count":       4,
		"mode":        "prefix",
		"canRaw":      false,
		"canManage":   true,
		"level":       lvlOwner,
		"guilds":      []guildOpt{{ID: "111111111111111111", Name: "Misfit's Tavern"}},
		"channels":    []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "staff-only"}, {ID: "c3", Name: "bots"}},
		"roles":       []entityOpt{{ID: "r1", Name: "@everyone"}, {ID: "r2", Name: "Staff"}, {ID: "r3", Name: "Mods"}},
	}
	return d
}

func mkTickets() renderData {
	d := mkData(lvlStaff)
	d.Page = "tickets"
	d.Content = struct {
		GuildID string
		Open    any
		Closed  any
		Types   any
		Error   string
	}{GuildID: "111111111111111111", Error: "tickets module is not loaded (preview)"}
	return d
}

// TestDumpPreviewHTML renders key pages with fixture data into DASH_PREVIEW_DIR
// so a browser can screenshot them without a live bot.
func TestDumpPreviewHTML(t *testing.T) {
	dir := os.Getenv("DASH_PREVIEW_DIR")
	if dir == "" {
		t.Skip("DASH_PREVIEW_DIR not set")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	pages := map[string]renderData{
		"servers":  mkServers(),
		"admin":    mkAdmin(),
		"index":    mkOverview(),
		"commands": mkCommands(),
		"tickets":  mkTickets(),
	}
	for name, d := range pages {
		var sb strings.Builder
		if err := b.render(&sb, name, d); err != nil {
			t.Errorf("render %s: %v", name, err)
			continue
		}
		if err := os.WriteFile(dir+"/"+name+".html", []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s.html", name)
	}
}
