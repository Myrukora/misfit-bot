package dashboard

import (
	"net/http"
	"net/http/httptest"
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
// sidebar/topbar (useless pre-auth), and sidebar pages keep the chrome.
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

	login := render("rd_login", false, nil)
	if strings.Contains(login, `class="sidebar"`) {
		t.Error("login page renders the sidebar — remove it (pre-auth it is useless)")
	}
	if strings.Contains(login, `class="topbar"`) {
		t.Error("login page renders the topbar — remove it")
	}
	if !strings.Contains(login, `class="login-card"`) {
		t.Error("login page missing the centered login card")
	}

	setup := render("rd_setup", false, map[string]string{"prefix": "?"})
	if strings.Contains(setup, `class="sidebar"`) {
		t.Error("setup page renders the sidebar — remove it (pre-auth it is useless)")
	}

	overview := render("rd_overview", true, metricsSnapshot{Runtime: map[string]any{}, Modules: []string{}})
	if !strings.Contains(overview, `class="sidebar"`) {
		t.Error("overview page missing sidebar")
	}
}

// TestAdminRedirectsToRoot pins that /admin is a redirect to / (the bot-wide
// admin panel moved onto the /servers page; /admin is kept as a redirect so
// old links still land somewhere sensible).
func TestAdminRedirectsToRoot(t *testing.T) {
	m := &DashboardModule{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/admin", nil)
	m.handleAdminPage(w, r)
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

// TestCommandsRawSwitch pins the raw toggle on the redesign commands page.
func TestCommandsRawSwitch(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	d := mkData(lvlOwner)
	d.ShowSidebar = true
	d.Content = map[string]any{"groups": []moduleGroup{}, "guild": "", "count": 0, "canRaw": true}
	var sb strings.Builder
	if err := b.render(&sb, "rd_commands", d); err != nil {
		t.Fatalf("render rd_commands: %v", err)
	}
	out := sb.String()
	if !strings.Contains(out, `class="toggle"`) || !strings.Contains(out, `id="cmd-raw"`) {
		t.Error("raw toggle must be the redesign .toggle component with id cmd-raw")
	}
	if !strings.Contains(out, `class="toggle-track"`) {
		t.Error("toggle missing track")
	}
}
