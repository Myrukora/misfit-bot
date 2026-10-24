package dashboard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestAuditLogRecordReloadFilterRetention(t *testing.T) {
	dir := t.TempDir()
	a := newAuditLog(dir, deadlockLogger{})
	fixed := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return fixed }

	a.record(auditEntry{Time: fixed.Add(-31 * 24 * time.Hour), Event: "page", Action: "GET /old"})
	a.record(auditEntry{Time: fixed.Add(-time.Hour), Event: "page", Success: true, Action: "GET /tos", Detail: "Terms of Service"})
	a.record(auditEntry{Time: fixed, Event: "login", Success: true, UserID: "1", Username: "owner", Level: "owner", IP: "203.0.113.9", Action: "Discord OAuth"})

	entries := a.list(auditFilter{Limit: 10})
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (old entry pruned): %+v", len(entries), entries)
	}
	if entries[0].Event != "login" || entries[1].Action != "GET /tos" {
		t.Fatalf("order = %+v", entries)
	}
	if got := a.list(auditFilter{Event: "page"}); len(got) != 1 {
		t.Fatalf("page filter = %d", len(got))
	}
	if got := a.list(auditFilter{UserID: "1"}); len(got) != 1 {
		t.Fatalf("user filter = %d", len(got))
	}
	if got := a.list(auditFilter{Limit: 1}); len(got) != 1 || got[0].Event != "login" {
		t.Fatalf("limit = %+v", got)
	}
	if a.count() != 2 {
		t.Fatalf("count = %d", a.count())
	}

	info, err := os.Stat(filepath.Join(dir, "dashboard-audit.jsonl"))
	if err != nil {
		t.Fatalf("audit file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("audit file mode = %o, want 600", perm)
	}

	// Reloading must drop expired entries and keep the valid ones.
	b := newAuditLog(dir, deadlockLogger{})
	b.now = func() time.Time { return fixed }
	reloaded := b.list(auditFilter{})
	if len(reloaded) != 2 {
		t.Fatalf("reloaded = %+v", reloaded)
	}
}

func TestAuditLogMemoryOnlyAndCorruptLines(t *testing.T) {
	a := newAuditLog("", deadlockLogger{})
	fixed := time.Now().UTC()
	a.now = func() time.Time { return fixed }
	a.record(auditEntry{Event: "page", Action: "GET /tos"})
	if got := a.list(auditFilter{}); len(got) != 1 {
		t.Fatalf("memory-only list = %+v", got)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dashboard-audit.jsonl"), []byte("{broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newAuditLog(dir, deadlockLogger{})
	b.now = func() time.Time { return fixed }
	if got := b.list(auditFilter{}); len(got) != 0 {
		t.Fatalf("corrupt lines should be skipped: %+v", got)
	}
	if info, err := os.Stat(filepath.Join(dir, "dashboard-audit.jsonl")); err == nil && info.Size() != 0 {
		t.Fatalf("corrupt file not rewritten (size %d)", info.Size())
	}
}

func TestAuditMiddlewarePagesAndMutations(t *testing.T) {
	m := &DashboardModule{
		bot:    &welcomeFakeBot{owner: "1"},
		audit:  newAuditLog(t.TempDir(), deadlockLogger{}),
		logger: deadlockLogger{},
	}
	us := &userSession{userID: snowflake.ID(1), username: "owner", csrfToken: "tok"}
	withSession := func(r *http.Request) *http.Request {
		return r.WithContext(setSession(r.Context(), us))
	}
	htmlHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html></html>"))
	})
	jsonHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	})
	h := m.auditMiddleware(htmlHandler)

	do := func(handler http.Handler, method, path string, authed bool) {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "203.0.113.9:1234"
		if authed {
			r = withSession(r)
		}
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}

	do(h, http.MethodGet, "/tos", false)
	do(h, http.MethodGet, "/tos", true)
	do(m.auditMiddleware(jsonHandler), http.MethodGet, "/api/me", true)
	do(h, http.MethodPost, "/api/settings/core", true)
	do(h, http.MethodPost, "/api/settings/core", false)
	do(h, http.MethodPost, "/login", true)
	do(h, http.MethodGet, "/static/style.css", true)

	entries := m.audit.list(auditFilter{})
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 (anonymous page, owner page, mutation): %+v", len(entries), entries)
	}
	byAction := map[string]auditEntry{}
	for _, e := range entries {
		byAction[e.Event+"|"+e.Action] = e
	}
	anon := byAction["page|GET /tos"]
	if anon.UserID != "" || anon.IP != "203.0.113.9" {
		t.Fatalf("anonymous page = %+v", anon)
	}
	ownerPage := entries[1]
	if ownerPage.UserID != "1" || ownerPage.Level != lvlOwner || ownerPage.Detail != "Terms of Service" {
		t.Fatalf("owner page = %+v", ownerPage)
	}
	mutation := byAction["mutation|POST /api/settings/core"]
	if mutation.UserID != "1" || mutation.Detail != "Core settings update" || !mutation.Success {
		t.Fatalf("mutation = %+v", mutation)
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name     string
		remote   string
		xff      string
		realIP   string
		expected string
	}{
		{"direct public", "198.51.100.9:1234", "10.0.0.1", "", "198.51.100.9"},
		{"trusted loopback with xff", "127.0.0.1:1234", "203.0.113.5, 10.0.0.1", "", "203.0.113.5"},
		{"trusted private with real ip", "192.168.1.5:1234", "", "203.0.113.6", "203.0.113.6"},
		{"invalid xff ignored", "127.0.0.1:1234", "not-an-ip", "", "127.0.0.1"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if tc.realIP != "" {
			r.Header.Set("X-Real-IP", tc.realIP)
		}
		if got := clientIP(r); got != tc.expected {
			t.Errorf("%s: clientIP = %q, want %q", tc.name, got, tc.expected)
		}
	}
}

func TestAuditLoginAndLogoutEntries(t *testing.T) {
	m := &DashboardModule{
		bot:    &welcomeFakeBot{owner: "1"},
		audit:  newAuditLog(t.TempDir(), deadlockLogger{}),
		logger: deadlockLogger{},
	}
	r := httptest.NewRequest(http.MethodGet, "/callback", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	user := &discord.User{ID: 42, Username: "vixen"}
	m.recordLogin(r, true, user, lvlOwner, "signed in", http.StatusSeeOther)
	m.recordLogin(r, false, user, "", "no shared server", http.StatusForbidden)
	m.recordLogout(r, &userSession{userID: 42, username: "vixen"})

	entries := m.audit.list(auditFilter{})
	if len(entries) != 3 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[2].Event != "login" || !entries[2].Success || entries[2].UserID != "42" {
		t.Fatalf("success login = %+v", entries[2])
	}
	if entries[1].Success || entries[1].Detail != "no shared server" {
		t.Fatalf("failed login = %+v", entries[1])
	}
	if entries[0].Event != "logout" || entries[0].IP != "203.0.113.9" {
		t.Fatalf("logout = %+v", entries[0])
	}
}

func TestAuditPageRoute(t *testing.T) {
	m, _, cookie := newWelcomeTestModule(t, true)
	m.audit = newAuditLog(t.TempDir(), deadlockLogger{})
	m.audit.record(auditEntry{Event: "page", Success: true, UserID: "1", Username: "owner", Level: "owner", IP: "203.0.113.9", Action: "GET /tos", Detail: "Terms of Service", Status: 200})

	w := welcomeReq(m, cookie, http.MethodGet, "/audit", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Dashboard audit", "GET /tos", "Terms of Service", "203.0.113.9", "30 days"} {
		if !strings.Contains(body, want) {
			t.Errorf("audit page missing %q", want)
		}
	}

	m.sessions.put("staff", &userSession{userID: snowflake.ID(2), username: "staff", csrfToken: "tok2"})
	w = welcomeReq(m, m.signCookie("staff"), http.MethodGet, "/audit", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("staff status = %d, want 403", w.Code)
	}
}

func TestTosAndPolicyVisitsAreLogged(t *testing.T) {
	m := legalTestModule(t)
	m.audit = newAuditLog(t.TempDir(), deadlockLogger{})
	h := m.buildHandler()
	for _, path := range []string{"/tos", "/policy"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, w.Code)
		}
	}
	entries := m.audit.list(auditFilter{})
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(entries), entries)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Event != "page" {
			t.Errorf("event = %q", e.Event)
		}
		seen[e.Action] = true
	}
	if !seen["GET /tos"] || !seen["GET /policy"] {
		t.Fatalf("missing legal page visits: %+v", entries)
	}
}

func TestAuditMutationRecordedThroughHandler(t *testing.T) {
	m, _, cookie := newWelcomeTestModule(t, true)
	m.audit = newAuditLog(t.TempDir(), deadlockLogger{})
	w := welcomeReq(m, cookie, http.MethodPost, "/api/does-not-exist", "{}", "tok")
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", w.Code)
	}
	entries := m.audit.list(auditFilter{Event: "mutation"})
	if len(entries) != 1 || entries[0].Action != "POST /api/does-not-exist" || entries[0].Success {
		t.Fatalf("mutation entries = %+v", entries)
	}
}
