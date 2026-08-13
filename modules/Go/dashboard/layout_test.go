package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveLogFilePath covers the daily-rotating log resolution: the logger
// writes <base>-YYYY-MM-DD.log files (DailyRotatingWriter), so the dashboard
// must tail the newest non-empty dated file rather than the stale plain
// <base>.log that pre-rotation installs left behind.
func TestResolveLogFilePath(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy non-rotated file (stale, must lose) + empty "today" file (created
	// at rotation before any write) + a real daily file.
	write("bot.log", "stale june logs\n")
	write("bot-2026-08-06.log", "")
	write("bot-2026-08-05.log", "yesterday\nline2\n")

	if got := resolveLogFilePath(dir, "bot"); got != filepath.Join(dir, "bot-2026-08-05.log") {
		t.Errorf("newest non-empty daily = %q, want bot-2026-08-05.log", got)
	}

	// Only empty daily files → fall back to the newest dated file anyway.
	dir2 := t.TempDir()
	write2 := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir2, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write2("bot-2026-08-01.log")
	write2("bot-2026-08-02.log")
	if got := resolveLogFilePath(dir2, "bot"); got != filepath.Join(dir2, "bot-2026-08-02.log") {
		t.Errorf("all-empty dailies = %q, want newest dated file", got)
	}

	// No daily files at all → legacy plain path.
	dir3 := t.TempDir()
	if got := resolveLogFilePath(dir3, "bot"); got != filepath.Join(dir3, "bot.log") {
		t.Errorf("no dailies = %q, want legacy bot.log", got)
	}
}

// TestTemplatesStandaloneLayout pins the login/setup standalone layout: no
// sidebar/topbar (useless pre-auth), the card is centered by .app-standalone.
func TestTemplatesStandaloneLayout(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	render := func(page string, sidebar bool, content any) string {
		t.Helper()
		d := mkData(lvlOwner)
		d.ShowSidebar = sidebar
		d.Content = content
		var sb strings.Builder
		if err := b.render(&sb, page, d); err != nil {
			t.Fatalf("render %s: %v", page, err)
		}
		return sb.String()
	}

	login := render("login", false, nil)
	if strings.Contains(login, `class="sidebar"`) {
		t.Error("login page renders the sidebar — remove it (pre-auth it is useless)")
	}
	if strings.Contains(login, `class="topbar"`) {
		t.Error("login page renders the topbar — remove it")
	}
	if !strings.Contains(login, "app-standalone") {
		t.Error("login page missing .app-standalone centering wrapper")
	}

	index := render("index", true, metricsSnapshot{Runtime: map[string]any{}, Modules: []string{}})
	if !strings.Contains(index, `class="sidebar"`) {
		t.Error("index page missing sidebar")
	}
	if strings.Contains(index, "app-standalone") {
		t.Error("index page wrongly uses standalone layout")
	}
}

// TestCommandsRawSwitch pins the glass switch markup for the raw toggle.
func TestCommandsRawSwitch(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	d := mkData(lvlOwner)
	d.ShowSidebar = true
	d.Content = map[string]any{"groups": []moduleGroup{}, "guild": "", "count": 0, "canRaw": true}
	var sb strings.Builder
	if err := b.render(&sb, "commands", d); err != nil {
		t.Fatalf("render commands: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, `class="switch"`) || !strings.Contains(out, `id="cmd-raw"`) {
		t.Error("raw toggle must be the glass .switch component with id cmd-raw")
	}
	if !strings.Contains(out, `class="track"`) || !strings.Contains(out, `class="knob"`) {
		t.Error("switch missing track/knob")
	}
}
