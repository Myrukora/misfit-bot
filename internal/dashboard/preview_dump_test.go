package dashboard

// preview_dump_test.go — renders every page template with representative data
// and writes the HTML to /tmp/dash-preview/ so a human (or Camofox) can open
// the pages without running the bot. Skipped unless PREVIEW_DUMP=1.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		page    string
		content any
		scoped  bool
	}{
		{"login.html", "rd_login", nil, false},
		{"servers.html", "rd_servers", map[string]any{"guilds": []guildPickerRow{
			{ID: "1", Name: "Gaming HQ", Icon: ""},
			{ID: "2", Name: "Art Server", Icon: ""},
		}}, false},
		{"overview.html", "rd_overview", metricsSnapshot{Runtime: map[string]any{
			"alloc_mb": uint64(128), "goroutines": 14, "gc_cycles": 42, "go_version": "go1.26.4",
		}, Modules: []string{"cleanup", "tickets", "imagefilter"}}, false},
		{"config.html", "rd_admin", map[string]any{
			"sections": []settingsSection{{Title: "Bot", Help: "Identity", Fields: []fieldRender{
				{Key: "prefix", Label: "Command prefix", Type: "text", Value: "!", Placeholder: "?"},
				{Key: "log_level", Label: "Log level", Type: "select", Value: "info", Options: []string{"debug", "info", "warn", "error"}},
				{Key: "log_enabled", Label: "File logging", Type: "toggle", Value: "true"},
			}}},
			"variants": []string{"b32", "b16", "l14", "l14-336"}, "variant": "b32",
		}, false},
		{"permissions.html", "rd_permissions", map[string]any{
			"elevated": []string{"111", "222"}, "owner_id": "9",
			"names": map[string]string{"111": "Helper", "222": "Mod", "9": "Sam"},
		}, false},
		{"logs.html", "rd_logs", map[string]any{
			"path": "logs/bot.log", "note": "",
			"lines": []string{`{"time":"2026-09-11T12:00:00Z","level":"INFO","msg":"bot started"}`, `{"time":"2026-09-11T12:01:00Z","level":"ERROR","msg":"rest failed"}`},
		}, false},
		{"imagefilter.html", "rd_imagefilter", map[string]any{
			"admin": true, "guild": "1",
			"config":   map[string]string{"enabled": "true", "threshold": "0.95", "punishment": "mute", "mute_duration": "600", "log_channel": "c1", "delete_on_none": "false"},
			"images":   []string{"ref_a.png", "ref_b.png"},
			"status":   modules.ImageFilterStatus{Warm: true, Variant: "b32", EnabledGuilds: 1},
			"enabled":  true,
			"channels": []entityOpt{{ID: "c1", Name: "general"}, {ID: "c2", Name: "mod-log"}},
		}, true},
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
		if err := b.render(&sb, p.page, d); err != nil {
			t.Errorf("render %s: %v", p.page, err)
			continue
		}
		if err := os.WriteFile(filepath.Join(out, p.name), []byte(sb.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("dumped %d previews to %s", len(pages), out)
}
