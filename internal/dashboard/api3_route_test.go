package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/oauth2"
	"github.com/disgoorg/snowflake/v2"
	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// ── Test doubles ─────────────────────────────────────────────────────────

// stubImageFilter is a recording modules.Module + modules.ImageFilterAdmin. It
// exists so the route tests can assert BOTH the HTTP shape and that a crafted
// guild id never reaches the admin (i.e. no filesystem access).
type stubImageFilter struct {
	mu        sync.Mutex
	calls     []string
	enableErr error
	// addErr fails AddImageFromBytes for a matching filename, so a batch can be
	// driven into partial success (one bad file among good ones).
	addErr map[string]error
}

func (s *stubImageFilter) record(call string) {
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()
}

func (s *stubImageFilter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// modules.Module
func (s *stubImageFilter) Name() string                  { return "imagefilter" }
func (s *stubImageFilter) Version() string               { return "test" }
func (s *stubImageFilter) Description() string           { return "stub" }
func (s *stubImageFilter) Author() string                { return "test" }
func (s *stubImageFilter) OnLoad(*modules.Context) error { return nil }
func (s *stubImageFilter) OnUnload() error               { return nil }
func (s *stubImageFilter) Dependencies() []string        { return nil }

func (s *stubImageFilter) SlashCommands() []commands.SlashCommand { return nil }

// modules.ImageFilterAdmin
func (s *stubImageFilter) GetGuildConfig(guildID string) (map[string]string, error) {
	s.record("GetGuildConfig:" + guildID)
	return map[string]string{"enabled": "true", "threshold": "0.95"}, nil
}
func (s *stubImageFilter) SetGuildConfig(guildID string, values map[string]string) error {
	s.record("SetGuildConfig:" + guildID)
	return nil
}
func (s *stubImageFilter) SetGuildEnabled(guildID string, enabled bool) error {
	s.record("SetGuildEnabled:" + guildID)
	return s.enableErr
}
func (s *stubImageFilter) EnabledGuilds() []string {
	s.record("EnabledGuilds")
	return []string{"123456789"}
}
func (s *stubImageFilter) ListImages(guildID string) ([]string, error) {
	s.record("ListImages:" + guildID)
	return []string{"a.png"}, nil
}
func (s *stubImageFilter) AddImageFromBytes(guildID string, data []byte, filename string) (string, error) {
	s.record("AddImageFromBytes:" + guildID + "/" + filename)
	if err, ok := s.addErr[filename]; ok {
		return "", err
	}
	return filename, nil
}
func (s *stubImageFilter) AddImageFromURL(guildID, url string) (string, error) {
	s.record("AddImageFromURL:" + guildID)
	return "x.png", nil
}
func (s *stubImageFilter) RemoveImage(guildID, name string) error {
	s.record("RemoveImage:" + guildID + "/" + name)
	return nil
}
func (s *stubImageFilter) ReadImage(guildID, name string) ([]byte, error) {
	s.record("ReadImage:" + guildID + "/" + name)
	return []byte("\x89PNG\r\n\x1a\nfake"), nil
}
func (s *stubImageFilter) Status() modules.ImageFilterStatus {
	s.record("Status")
	return modules.ImageFilterStatus{Warm: true, Variant: "b32", EnabledGuilds: 1}
}
func (s *stubImageFilter) SetVariant(variant string) error {
	s.record("SetVariant:" + variant)
	return nil
}
func (s *stubImageFilter) Variant() string {
	s.record("Variant")
	return "b32"
}

var _ modules.ImageFilterAdmin = (*stubImageFilter)(nil)

// stubModuleGetter stands in for *modules.Manager (the dashboard only needs Get).
type stubModuleGetter struct{ mod modules.Module }

func (g stubModuleGetter) Get(name string) (modules.Module, bool) {
	if name != "imagefilter" || g.mod == nil {
		return nil, false
	}
	return g.mod, true
}

// fakeFilterBot is the minimal commands.Interface the imagefilter routes touch:
// resolveLevel (IsOwner) and imageFilterAdmin (GetModuleManager).
type fakeFilterBot struct {
	commands.Interface
	owner string
	admin modules.Module
}

func (f *fakeFilterBot) MemberIntentEnabled() bool { return true }

func (f *fakeFilterBot) IsOwner(id string) bool { return id == f.owner }
func (f *fakeFilterBot) IsElevated(string) bool { return false }
func (f *fakeFilterBot) GetModuleManager() any  { return stubModuleGetter{mod: f.admin} }

// filterTestModule builds a module whose only request-visible dependency is the
// stub admin (pass nil to exercise the "module not loaded" path), plus a signed
// owner session cookie.
func filterTestModule(t *testing.T, admin modules.Module) (*DashboardModule, string) {
	t.Helper()
	m := &DashboardModule{
		bot:      &fakeFilterBot{owner: "1", admin: admin},
		botName:  "TestBot",
		cfg:      &DashboardConfig{SessionSecret: "test-secret", Listen: "127.0.0.1:0"},
		client:   &bot.Client{},
		oauth:    oauth2.New(snowflake.ID(1), "sec"),
		sessions: newSessionStore(),
		logger:   deadlockLogger{},
		dataDir:  t.TempDir(),
	}
	key := "sess-key"
	m.sessions.put(key, &userSession{
		userID:    snowflake.ID(1),
		username:  "owner",
		csrfToken: "tok",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	return m, m.signCookie(key)
}

// doAPI drives one request through the FULL middleware chain (the same handler
// the server uses), with a valid signed session cookie + CSRF header.
func doAPI(m *DashboardModule, cookie, method, target, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	r.Header.Set(csrfHeader, "tok")
	w := httptest.NewRecorder()
	m.buildHandler().ServeHTTP(w, r)
	return w
}

// ── Tests ────────────────────────────────────────────────────────────────

// TestImageFilterGuildRoutesReachable is the C1/B5 regression: the guild-scoped
// /api/guilds/<gid>/imagefilter subtree used to be unreachable — api2.go's
// "guilds" case fell through to the outer 405 for any non-GET method and
// api3.go's guard tested parts[0] == "guild" (singular). The raw route also
// demanded len(parts) == 5 while the template emits len 4, so every gallery
// thumbnail 404'd.
func TestImageFilterGuildRoutesReachable(t *testing.T) {
	admin := &stubImageFilter{}
	m, cookie := filterTestModule(t, admin)

	for _, tc := range []struct {
		name    string
		method  string
		target  string
		body    string
		want    int
		bodyHas string
	}{
		{"enable", http.MethodPost, "/api/guilds/123456789/imagefilter/enable", `{"enabled":true}`, http.StatusOK, `"ok":true`},
		{"overview", http.MethodGet, "/api/guilds/123456789/imagefilter", "", http.StatusOK, `"config"`},
		{"raw (len 4)", http.MethodGet, "/api/guilds/123456789/imagefilter/raw?name=a.png", "", http.StatusOK, "fake"},
		{"delete (len 5)", http.MethodDelete, "/api/guilds/123456789/imagefilter/images/a.png", "", http.StatusOK, `"ok":true`},
		{"config update", http.MethodPost, "/api/guilds/123456789/imagefilter", `{"threshold":"0.9"}`, http.StatusOK, `"ok":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := doAPI(m, cookie, tc.method, tc.target, tc.body)
			if w.Code == http.StatusMethodNotAllowed {
				t.Fatalf("%s %s -> 405: the guild imagefilter subtree is unreachable again", tc.method, tc.target)
			}
			if w.Code != tc.want {
				t.Fatalf("%s %s -> %d, want %d (body %s)", tc.method, tc.target, w.Code, tc.want, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.bodyHas) {
				t.Errorf("body %s missing %s", w.Body.String(), tc.bodyHas)
			}
		})
	}

	// B5 shape pin: the pre-fix len-5 raw shape must NOT serve an image.
	w := doAPI(m, cookie, http.MethodGet, "/api/guilds/123456789/imagefilter/raw/a.png", "")
	if w.Code == http.StatusOK {
		t.Errorf("len-5 raw shape still served (%d) — the route must be len 4 + ?name=", w.Code)
	}

	// Every route above reached the admin exactly once (no auth/short-circuit).
	if n := admin.callCount(); n < 5 {
		t.Errorf("admin calls = %d, want >=5: %v", n, admin.calls)
	}
}

// TestImageFilterEnableLoadFailureSurface pins B2: a failed model load must be
// reported to the dashboard as 500, not swallowed (the guild stays cold and the
// owner sees why).
func TestImageFilterEnableLoadFailureSurface(t *testing.T) {
	admin := &stubImageFilter{enableErr: errors.New("model load failed: no such file")}
	m, cookie := filterTestModule(t, admin)

	w := doAPI(m, cookie, http.MethodPost, "/api/guilds/123456789/imagefilter/enable", `{"enabled":true}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "model load failed") {
		t.Errorf("body %s must carry the load error", w.Body.String())
	}
}

// TestImageFilterCSRFAndAuthGates pins the two guards that must fire before any
// module work: no session → 401, bad CSRF token → 403 with zero admin calls.
func TestImageFilterCSRFAndAuthGates(t *testing.T) {
	admin := &stubImageFilter{}
	m, cookie := filterTestModule(t, admin)

	// No cookie at all.
	r := httptest.NewRequest(http.MethodPost, "/api/guilds/123456789/imagefilter/enable", strings.NewReader(`{"enabled":true}`))
	w := httptest.NewRecorder()
	m.buildHandler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d, want 401", w.Code)
	}

	// Valid session, wrong CSRF token.
	r = httptest.NewRequest(http.MethodPost, "/api/guilds/123456789/imagefilter/enable", strings.NewReader(`{"enabled":true}`))
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	r.Header.Set(csrfHeader, "wrong")
	w = httptest.NewRecorder()
	m.buildHandler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("bad CSRF = %d, want 403", w.Code)
	}
	if n := admin.callCount(); n != 0 {
		t.Errorf("admin calls = %d, want 0 before the guards pass: %v", n, admin.calls)
	}
}

// ── Batch blacklist uploads ──────────────────────────────────────────────

// postMultipartUpload drives one multipart "file" batch through the full
// middleware chain (valid cookie + CSRF), the way the dashboard's Add button
// does when a whole selection is queued.
func postMultipartUpload(t *testing.T, m *DashboardModule, cookie, gid string, files map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, content := range files {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatalf("write form file: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/guilds/"+gid+"/imagefilter/images", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	r.Header.Set(csrfHeader, "tok")
	w := httptest.NewRecorder()
	m.buildHandler().ServeHTTP(w, r)
	return w
}

// TestImageFilterBatchUploadPartialSuccess pins the batch contract: one bad
// file must NOT discard the files that already landed. Before this, the handler
// returned on the first per-file error, so a 15-image purge with a single
// unreadable file stored a prefix of the batch, reported an error, and left the
// operator unable to tell what made it in.
func TestImageFilterBatchUploadPartialSuccess(t *testing.T) {
	admin := &stubImageFilter{addErr: map[string]error{"bad.png": errors.New("not a decodable image")}}
	m, cookie := filterTestModule(t, admin)

	w := postMultipartUpload(t, m, cookie, "123456789", map[string]string{
		"one.png": "a",
		"bad.png": "b",
		"two.png": "c",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	bodyStr := w.Body.String()
	for _, want := range []string{`"one.png"`, `"two.png"`, `"bad.png: not a decodable image"`} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("body %s missing %s", bodyStr, want)
		}
	}
	// All three reached the module: the loop continues past a failure.
	admin.mu.Lock()
	n := 0
	for _, c := range admin.calls {
		if strings.HasPrefix(c, "AddImageFromBytes:") {
			n++
		}
	}
	admin.mu.Unlock()
	if n != 3 {
		t.Errorf("AddImageFromBytes calls = %d, want 3 (one per file): %v", n, admin.calls)
	}
}

// TestImageFilterBatchUploadAllFailed: a batch where nothing lands is a 400
// (the dashboard must not report success), but the response still enumerates
// every failure so the operator sees all of them, not just the first.
func TestImageFilterBatchUploadAllFailed(t *testing.T) {
	admin := &stubImageFilter{addErr: map[string]error{
		"a.png": errors.New("not a decodable image"),
		"b.png": errors.New("image too large: 99999x99999 (>25MP)"),
	}}
	m, cookie := filterTestModule(t, admin)

	w := postMultipartUpload(t, m, cookie, "123456789", map[string]string{"a.png": "x", "b.png": "y"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
	// Decode rather than substring-match: encoding/json escapes ">" as \u003e,
	// so the raw body does not contain the literal bomb-guard text.
	var got struct {
		Added  []string `json:"added"`
		Failed []string `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v (body %s)", err, w.Body.String())
	}
	if len(got.Added) != 0 {
		t.Errorf("added = %v, want empty", got.Added)
	}
	joined := strings.Join(got.Failed, " | ")
	for _, want := range []string{"not a decodable image", ">25MP"} {
		if !strings.Contains(joined, want) {
			t.Errorf("failed = %q, missing %q", joined, want)
		}
	}
}

// TestSplitURLList: the URL box accepts a pasted column of CDN links.
func TestSplitURLList(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"single", "https://cdn.discordapp.com/a.png", []string{"https://cdn.discordapp.com/a.png"}},
		{"newline separated", "https://x/a.png\nhttps://x/b.png", []string{"https://x/a.png", "https://x/b.png"}},
		{"comma separated", "https://x/a.png, https://x/b.png", []string{"https://x/a.png", "https://x/b.png"}},
		{"mixed with blank lines", "\n https://x/a.png \n\n , https://x/b.png\n", []string{"https://x/a.png", "https://x/b.png"}},
		{"empty", "   \n ", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := splitURLList(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitURLList(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("splitURLList(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestImageFilterBatchURLs: a multi-URL paste is stored one by one, and one bad
// link does not drop the rest.
func TestImageFilterBatchURLs(t *testing.T) {
	admin := &stubImageFilter{}
	m, cookie := filterTestModule(t, admin)

	w := doAPI(m, cookie, http.MethodPost, "/api/guilds/123456789/imagefilter/images",
		`{"url":"https://x/a.png\nhttps://x/b.png"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	admin.mu.Lock()
	urlCalls := 0
	for _, c := range admin.calls {
		if strings.HasPrefix(c, "AddImageFromURL:") {
			urlCalls++
		}
	}
	admin.mu.Unlock()
	if urlCalls != 2 {
		t.Errorf("AddImageFromURL calls = %d, want 2: %v", urlCalls, admin.calls)
	}
	if !strings.Contains(w.Body.String(), `"x.png"`) {
		t.Errorf("body %s missing the added list", w.Body.String())
	}
}

// TestValidGuildID is the S1 unit table: only a non-zero digit string is a
// usable guild directory name.
func TestValidGuildID(t *testing.T) {
	for _, tc := range []struct {
		gid  string
		want bool
	}{
		{"123456789", true},
		{"1", true},
		{"", false},
		{"0", false},
		{"null", false},
		{"..", false},
		{"%2e%2e", false},
		{".", false},
		{"-1", false},
		{"12a", false},
		{" 123", false},
		{"123 ", false},
		{"1/2", false},
		{"0x10", false},
	} {
		if got := validGuildID(tc.gid); got != tc.want {
			t.Errorf("validGuildID(%q) = %v, want %v", tc.gid, got, tc.want)
		}
	}
}

// TestImageFilterCraftedGuildIDRejectedBeforeFS is the S1 regression at the
// HTTP layer: a crafted guild id must be refused with 400 and must NEVER reach
// the module (which would join it into <dataDir>/spam_images/<gid>).
func TestImageFilterCraftedGuildIDRejectedBeforeFS(t *testing.T) {
	admin := &stubImageFilter{}
	m, cookie := filterTestModule(t, admin)

	// %2e%2e and null survive path canonicalization and reach the handler;
	// a literal ".." is collapsed by net/http's mux redirect first (asserted
	// separately below).
	for _, tc := range []struct {
		method string
		target string
	}{
		{http.MethodPost, "/api/guilds/%2e%2e/imagefilter/enable"},
		{http.MethodGet, "/api/guilds/%2e%2e/imagefilter"},
		{http.MethodPost, "/api/guilds/null/imagefilter/enable"},
		{http.MethodDelete, "/api/guilds/0/imagefilter/images/a.png"},
	} {
		w := doAPI(m, cookie, tc.method, tc.target, "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s -> %d, want 400 (body %s)", tc.method, tc.target, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "invalid guild id") {
			t.Errorf("%s %s body = %s, want the invalid-guild-id error", tc.method, tc.target, w.Body.String())
		}
	}
	if n := admin.callCount(); n != 0 {
		t.Errorf("admin calls = %d, want 0 — a crafted id reached the filesystem layer: %v", n, admin.calls)
	}

	// A literal "../" in the path is canonicalized by net/http before any
	// handler runs, so the guild branch is never entered (and therefore the
	// module is never called either).
	w := doAPI(m, cookie, http.MethodPost, "/api/guilds/../imagefilter/enable", `{"enabled":true}`)
	if w.Code == http.StatusOK || w.Code == http.StatusBadRequest {
		t.Errorf("literal ../ = %d: expected canonicalization, not a guild-branch decision", w.Code)
	}
	if n := admin.callCount(); n != 0 {
		t.Errorf("admin called for a canonicalized path: %v", admin.calls)
	}
}

// TestImageFilterDirectCraftedID pins the dispatcher itself (no mux in the
// way): routeImageFilterAPI with a crafted gid in parts[1] must 400 without
// touching the admin, for every sub-route.
func TestImageFilterDirectCraftedID(t *testing.T) {
	for _, bad := range []string{"..", "$", "null", "", "0", "1x"} {
		admin := &stubImageFilter{}
		m, cookie := filterTestModule(t, admin)
		r := httptest.NewRequest(http.MethodPost, "/api/guilds/x/imagefilter/enable", strings.NewReader(`{"enabled":true}`))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		r.Header.Set(csrfHeader, "tok")
		r = r.WithContext(setSession(r.Context(), sessionOfCookieForTest(t, m, cookie)))

		w := httptest.NewRecorder()
		m.routeImageFilterAPI(w, r, http.MethodPost, []string{"guilds", bad, "imagefilter", "enable"})
		if w.Code != http.StatusBadRequest {
			t.Errorf("parts[1]=%q -> %d, want 400", bad, w.Code)
		}
		if n := admin.callCount(); n != 0 {
			t.Errorf("parts[1]=%q reached the admin: %v", bad, admin.calls)
		}
	}
}

// sessionOfCookieForTest resolves the signed cookie back to the stored session
// (the direct-dispatch test above bypasses authMiddleware).
func sessionOfCookieForTest(t *testing.T, m *DashboardModule, raw string) *userSession {
	t.Helper()
	key, ok := m.verifyCookie(raw)
	if !ok {
		t.Fatal("cookie did not verify")
	}
	us, ok := m.sessions.get(key)
	if !ok {
		t.Fatal("session not found")
	}
	return us
}

// TestImageFilterMissingModule404 pins the "module not loaded" path: the route
// must answer 404 rather than 500/405 when the admin cannot be resolved.
func TestImageFilterMissingModule404(t *testing.T) {
	m, cookie := filterTestModule(t, nil)
	for _, target := range []string{
		"/api/guilds/123456789/imagefilter",
		"/api/guilds/123456789/imagefilter/enable",
	} {
		w := doAPI(m, cookie, http.MethodGet, target, "")
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s -> %d, want 404", target, w.Code)
		}
	}
}

// TestImageFilterRawServesBytes pins the gallery endpoint's contract: content
// type is sniffed and the bytes are returned verbatim.
func TestImageFilterRawServesBytes(t *testing.T) {
	admin := &stubImageFilter{}
	m, cookie := filterTestModule(t, admin)
	w := doAPI(m, cookie, http.MethodGet, "/api/guilds/123456789/imagefilter/raw?name=a.png", "")
	body, _ := io.ReadAll(w.Result().Body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if string(body) != "\x89PNG\r\n\x1a\nfake" {
		t.Errorf("body = %q, want the raw image bytes", body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Errorf("Content-Type = %q, want an image/png sniff", ct)
	}
	// Traversal-shaped names collapse via filepath.Base before the module call.
	w = doAPI(m, cookie, http.MethodGet, "/api/guilds/123456789/imagefilter/raw?name=../../etc/passwd", "")
	if w.Code != http.StatusOK {
		t.Fatalf("traversal name -> %d, want 200 (filepath.Base collapses it)", w.Code)
	}
	admin.mu.Lock()
	last := admin.calls[len(admin.calls)-1]
	admin.mu.Unlock()
	if last != "ReadImage:123456789/passwd" {
		t.Errorf("module saw %q, want the basename-collapsed name", last)
	}
}
