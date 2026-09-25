package dashboard

// preview_dump_test.go — renders every page template with representative data
// and writes the HTML to /tmp/dash-preview/ so a human (or Camofox) can open
// the pages without running the bot. Skipped unless PREVIEW_DUMP=1.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

func previewData(level string) renderData {
	d := mkData(level)
	d.ShowSidebar = true
	d.ShowConfig = level == lvlOwner || level == lvlElevated
	d.ShowStaff = true
	d.ShowImageFilter = true
	return d
}

func TestPreviewDumpRedesign(t *testing.T) {
	if os.Getenv("PREVIEW_DUMP") != "1" {
		t.Skip("set PREVIEW_DUMP=1 to dump preview HTML")
	}
	b, err := loadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	out := "/tmp/dash-preview"
	_ = os.MkdirAll(out, 0o755)

	pages := []struct {
		name    string
		page    string // renderData.Page (what the real handler sets)
		tpl     string // template to render (rd_*)
		content any
		scoped  bool
	}{
		{"login.html", "", "rd_login", nil, false},
		{"servers.html", "servers", "rd_servers", map[string]any{"guilds": []guildPickerRow{
			{ID: "1", Name: "Gaming HQ", Icon: ""},
			{ID: "2", Name: "Art Server", Icon: ""},
		}}, false},
		{"overview.html", "index", "rd_overview", metricsSnapshot{Runtime: map[string]any{
			"alloc_mb": uint64(128), "goroutines": 14, "gc_cycles": 42, "go_version": "go1.26.4",
		}, Modules: []string{"cleanup", "tickets", "imagefilter"}}, false},
		{"config.html", "config", "rd_admin", map[string]any{
			"sections": []settingsSection{{Title: "Bot", Help: "Identity", Fields: []fieldRender{
				{Key: "prefix", Label: "Command prefix", Type: "text", Value: "!", Placeholder: "?"},
				{Key: "log_level", Label: "Log level", Type: "select", Value: "info", Options: []string{"debug", "info", "warn", "error"}},
				{Key: "log_enabled", Label: "File logging", Type: "toggle", Value: "true"},
			}}},
			"variants": []string{"b32", "b16", "l14", "l14-336"}, "variant": "b32",
		}, false},
		{"permissions.html", "permissions", "rd_permissions", map[string]any{
			"elevated": []string{"111", "222"}, "owner_id": "9",
			"names": map[string]string{"111": "Helper", "222": "Mod", "9": "Sam"},
		}, false},
		{"logs.html", "logs", "rd_logs", map[string]any{
			"path": "logs/bot.log", "note": "",
			"lines": []string{`{"time":"2026-09-11T12:00:00Z","level":"INFO","msg":"bot started"}`, `{"time":"2026-09-11T12:01:00Z","level":"ERROR","msg":"rest failed"}`},
		}, false},
		{"imagefilter.html", "imagefilter", "rd_imagefilter", map[string]any{
			"admin": true, "guild": "1",
			"config":   map[string]string{"enabled": "true", "threshold": "0.95", "punishment": "mute", "mute_duration": "600", "log_channel": "c1", "delete_on_none": "false"},
			"images":   []string{"ref_a.png", "ref_b.png"},
			"status":   modules.ImageFilterStatus{Warm: true, Variant: "b32", EnabledGuilds: 1},
			"enabled":  true,
			"channels": []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "mod-log"}},
		}, true},
		{"commands.html", "gcommands", "rd_commands", map[string]any{
			"groups": []moduleGroup{
				{Module: "core", Categories: []catGroup{{Name: "general", Commands: []cmdView{
					{Name: "ping", Description: "pong", Category: "general", ModuleOwner: "core", Kind: "prefix", Usage: "ping", Usable: true, CanExec: true},
					{Name: "ban", Description: "ban a member", Category: "moderation", ModuleOwner: "core", Kind: "prefix", Usage: "ban <user> [reason]", Usable: true, CanExec: true, RequiredPerm: "Ban Members"},
					{Name: "owner-only-cmd", Description: "owner only", Category: "moderation", ModuleOwner: "core", Kind: "prefix", OwnerOnly: true, Usable: false},
				}}}},
				{Module: "cleanup", Categories: []catGroup{{Name: "cleanup", Commands: []cmdView{
					{Name: "cleanup", Description: "clean messages", Category: "cleanup", ModuleOwner: "cleanup", Kind: "prefix", Usage: "cleanup <n>", Usable: true, CanExec: true},
				}}}},
			},
			"guild": "1", "selectedTab": "core", "count": 4, "mode": "prefix",
			"canRaw": false, "canManage": true, "level": lvlOwner,
			"guilds":   []guildOpt{{ID: "1", Name: "Gaming HQ"}},
			"channels": []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "mod-log"}},
			"roles":    []entityOpt{{ID: "r1", Name: "@everyone"}, {ID: "r2", Name: "Staff"}},
		}, true},
		{"tickets.html", "gtickets", "rd_tickets", map[string]any{
			"GuildID": "1",
			"Types": []struct{ Key, Label string }{
				{Key: "support", Label: "Support"},
				{Key: "report", Label: "Report a member"},
			},
			"Open": []struct {
				ID, Type, OpenerID, ClaimerID string
				OpenedAt                      time.Time
			}{
				{ID: "12", Type: "support", OpenerID: "111", ClaimerID: "9", OpenedAt: time.Now().Add(-2 * time.Hour)},
			},
			"Closed": []struct {
				ID, Type, OpenerID string
				ClosedAt           time.Time
			}{
				{ID: "11", Type: "report", OpenerID: "222", ClosedAt: time.Now().Add(-26 * time.Hour)},
			},
		}, true},
		{"tickets-empty.html", "gtickets", "rd_tickets", map[string]any{
			"GuildID": "1", "Types": []struct{ Key, Label string }{},
			"Open": nil, "Closed": nil,
		}, true},
		{"tickets-error.html", "gtickets", "rd_tickets", map[string]any{
			"GuildID": "", "Error": "tickets module is not loaded",
		}, true},
		{"modules.html", "modules", "rd_modules", settingsPageData{Manage: true, MgmtRows: []moduleView{
			{Name: "cleanup", Loaded: true, Description: "Bulk message cleanup"},
			{Name: "tickets", Loaded: true},
			{Name: "hello", Loaded: false, Description: "Demo module"},
		}}, false},
		{"gmodules.html", "gmodules", "rd_modules", settingsPageData{
			GuildID: "1", GuildName: "Gaming HQ",
			Sections: []settingsSection{{Title: "Presence", Help: "Shown on this server's bot profile.", Fields: []fieldRender{
				{Key: "presence_enabled", Label: "Custom presence", Type: "toggle", Value: "true"},
				{Key: "presence_text", Label: "Activity text", Type: "text", Value: "with the fire", GuildScoped: true, GuildID: "1"},
			}}},
			DashboardSelf: moduleConfigView{Name: "dashboard", Fields: []fieldRender{
				{Key: "exec_mode", Label: "Command execution way", Type: "select", Value: "prefix", Options: []string{"prefix", "slash"}},
			}},
			ModulesView: []moduleConfigView{{Name: "tickets", Fields: []fieldRender{
				{Key: "allow_dashboard_close", Label: "Allow closing from the dashboard", Type: "toggle", Value: "false", GuildScoped: true, GuildID: "1"},
				{Key: "transcript_channel", Label: "Transcript channel", Type: "channel", Value: "c1", GuildScoped: true, GuildID: "1", Entities: []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "mod-log"}}},
				{Key: "welcome_message", Label: "Welcome message", Type: "textarea", Value: "hey {user}", GuildScoped: true, GuildID: "1"},
				{Key: "max_open", Label: "Max open tickets", Type: "number", Value: "3", Min: "1", Max: "20", Step: "1", GuildScoped: true, GuildID: "1"},
			}}},
		}, true},
		{"transcript.html", "transcript", "rd_transcript", struct {
			Ticket   *modules.Ticket
			GuildID  string
			CloseURL string
		}{
			Ticket: &modules.Ticket{
				ID: "support-0012", Type: "support", GuildID: "1", OpenerID: "111", ClaimerID: "9", Status: "open",
				OpenedAt: time.Now().Add(-2 * time.Hour), Members: []string{"333"},
				Log: []modules.LogEntry{
					{MsgID: "m1", AuthorID: "111", AuthorName: "Helper", Timestamp: time.Now().Add(-2 * time.Hour), Content: "My game **crashes**.",
						Attachments: []modules.Media{{URL: "https://cdn.discordapp.com/a.png", Kind: "image", Filename: "a.png"}}},
					{MsgID: "m2", AuthorID: "9", AuthorName: "Sam", IsBot: false, Timestamp: time.Now().Add(-1 * time.Hour), Content: "Try a clean install.", Edited: true},
					{MsgID: "m3", AuthorID: "111", AuthorName: "Helper", Timestamp: time.Now(), Content: "deleted msg", Deleted: true},
				},
			},
			GuildID: "1",
		}, false},
	}

	for _, p := range pages {
		d := previewData(lvlOwner)
		d.Page = p.page
		if p.scoped {
			d.GuildID = "1"
			d.GuildName = "Gaming HQ"
		}
		d.Content = p.content
		var sb strings.Builder
		if err := b.render(&sb, p.tpl, d); err != nil {
			t.Errorf("render %s: %v", p.tpl, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(out, p.name), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("dumped %d previews to %s", len(pages), out)
}
