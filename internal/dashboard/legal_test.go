package dashboard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misfit/bot/commands"
)

// fakeLegalBot is the minimal commands.Interface the legal pages touch:
// GetConfigDir (where legal/*.md lives), GetVersion (asset cache-busting) and
// MemberIntentEnabled (gatedMode).
type fakeLegalBot struct {
	commands.Interface
	configDir string
}

func (f *fakeLegalBot) MemberIntentEnabled() bool { return true }
func (f *fakeLegalBot) GetVersion() string        { return "test" }
func (f *fakeLegalBot) GetConfigDir() string      { return f.configDir }

// legalTestModule builds a dashboard module whose bot directory is a temp dir.
func legalTestModule(t *testing.T) *DashboardModule {
	t.Helper()
	m := &DashboardModule{
		bot:      &fakeLegalBot{configDir: t.TempDir()},
		botName:  "TestBot",
		cfg:      &DashboardConfig{SessionSecret: "test-secret", Listen: "127.0.0.1:0"},
		sessions: newSessionStore(),
		logger:   deadlockLogger{},
		dataDir:  t.TempDir(),
	}
	tmpl, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	m.tmpl = tmpl
	return m
}

// TestLegalPagesArePublic pins that /tos and /policy render without a session:
// they are linked from the login card, before the user has authenticated.
func TestLegalPagesArePublic(t *testing.T) {
	m := legalTestModule(t)
	h := m.buildHandler()
	for path, title := range map[string]string{
		"/tos":    "Terms of Service",
		"/policy": "Privacy Policy",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (body %s)", path, w.Code, body)
		}
		if strings.Contains(body, "Sign in with Discord") {
			t.Errorf("GET %s rendered the login card, want the legal document", path)
		}
		if !strings.Contains(body, `class="legal-card"`) {
			t.Errorf("GET %s missing legal-card (body %s)", path, body)
		}
		if !strings.Contains(body, "<h1>"+title+"</h1>") {
			t.Errorf("GET %s missing markdown heading %q", path, title)
		}
	}
	// First render seeds the editable starter documents.
	for _, name := range []string{"terms.md", "privacy.md"} {
		p := filepath.Join(m.bot.GetConfigDir(), "legal", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("starter document %s not created: %v", p, err)
		}
	}
}

// TestLegalMarkdownRenderedFromFile pins that edits to the markdown file show
// up without a restart and that GFM (tables, links, emphasis) is supported.
func TestLegalMarkdownRenderedFromFile(t *testing.T) {
	m := legalTestModule(t)
	dir := filepath.Join(m.bot.GetConfigDir(), "legal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "# Custom Terms\n\nVisit [example](https://example.com) and read **this**.\n\n| A | B |\n|---|---|\n| 1 | 2 |\n"
	if err := os.WriteFile(filepath.Join(dir, "terms.md"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	m.buildHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tos", nil))
	body := w.Body.String()
	for _, want := range []string{
		"<h1>Custom Terms</h1>",
		`<a href="https://example.com"`,
		"<strong>this</strong>",
		"<table>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
}

// TestLegalMarkdownEscapesRawHTML pins that a markdown file cannot inject
// markup into the public page.
func TestLegalMarkdownEscapesRawHTML(t *testing.T) {
	m := legalTestModule(t)
	dir := filepath.Join(m.bot.GetConfigDir(), "legal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "privacy.md"), []byte("<script>alert(1)</script>"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	m.buildHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/policy", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if strings.Contains(body, "<script>") {
		t.Fatalf("raw HTML was not neutralised:\n%s", body)
	}
	if !strings.Contains(body, "raw HTML omitted") {
		t.Fatalf("goldmark did not replace the raw HTML block:\n%s", body)
	}
}

// TestLoginPageHasLegalLinks pins the two links under the login card.
func TestLoginPageHasLegalLinks(t *testing.T) {
	m := legalTestModule(t)
	w := httptest.NewRecorder()
	m.renderLogin(w, httptest.NewRequest(http.MethodGet, "/login", nil))
	body := w.Body.String()
	for _, want := range []string{`href="/tos"`, `href="/policy"`, "Terms of Service", "Privacy Policy", `class="login-legal"`} {
		if !strings.Contains(body, want) {
			t.Errorf("login page missing %q", want)
		}
	}
}
