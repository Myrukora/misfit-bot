package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/oauth2"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// ── test doubles ─────────────────────────────────────────────────────────

var errTest = errors.New("broken config")

type welcomeStubManager map[string]modules.Module

func (s welcomeStubManager) Get(name string) (modules.Module, bool) {
	m, ok := s[name]
	return m, ok
}

type welcomeStubModule struct {
	modules.Module
	mu      sync.Mutex
	cfg     modules.WelcomeConfig
	saved   modules.WelcomeConfig
	err     error
	tests   []string
	lastErr map[string]string
}

func (s *welcomeStubModule) GetGuildConfig(string) (modules.WelcomeConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.err
}

func (s *welcomeStubModule) SetGuildConfig(_ string, cfg modules.WelcomeConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = cfg
	s.cfg = cfg
	return s.err
}

func (s *welcomeStubModule) SendTest(_, event, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tests = append(s.tests, event)
	return nil
}

func (s *welcomeStubModule) LastError(_, event string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr[event]
}

func (s *welcomeStubModule) sentTests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.tests...)
}

func (s *welcomeStubModule) savedConfig() modules.WelcomeConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved
}

type welcomeFakeBot struct {
	commands.Interface
	owner string
	mgr   any
}

func (f *welcomeFakeBot) MemberIntentEnabled() bool { return true }
func (f *welcomeFakeBot) IsOwner(id string) bool    { return id == f.owner }
func (f *welcomeFakeBot) IsElevated(string) bool    { return false }
func (f *welcomeFakeBot) GetModuleManager() any     { return f.mgr }
func (f *welcomeFakeBot) GetVersion() string        { return "test" }

// newWelcomeTestModule builds a dashboard module with an optional stub welcome
// module and a signed owner session cookie.
func newWelcomeTestModule(t *testing.T, withModule bool) (*DashboardModule, *welcomeStubModule, string) {
	t.Helper()
	mod := &welcomeStubModule{lastErr: map[string]string{}}
	mgr := welcomeStubManager{}
	if withModule {
		mgr["welcome"] = mod
	}
	m := &DashboardModule{
		bot:      &welcomeFakeBot{owner: "1", mgr: mgr},
		botName:  "TestBot",
		cfg:      &DashboardConfig{SessionSecret: "test-secret", Listen: "127.0.0.1:0"},
		client:   &bot.Client{Caches: cache.New()},
		oauth:    oauth2.New(snowflake.ID(1), "sec"),
		sessions: newSessionStore(),
		logger:   deadlockLogger{},
		dataDir:  t.TempDir(),
	}
	tmpl, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	m.tmpl = tmpl
	m.client.Caches.SetSelfUser(discord.OAuth2User{User: discord.User{ID: snowflake.ID(1), Username: "TestBot"}})
	key := "welcome-owner"
	m.sessions.put(key, &userSession{
		userID:    snowflake.ID(1),
		username:  "owner",
		csrfToken: "tok",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	return m, mod, m.signCookie(key)
}

func welcomeReq(m *DashboardModule, cookie, method, target, body, csrf string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	if csrf != "" {
		r.Header.Set(csrfHeader, csrf)
	}
	w := httptest.NewRecorder()
	m.buildHandler().ServeHTTP(w, r)
	return w
}

// ── page ─────────────────────────────────────────────────────────────────

func TestWelcomePageRenders(t *testing.T) {
	m, mod, cookie := newWelcomeTestModule(t, true)
	mod.cfg = modules.WelcomeConfig{Join: modules.WelcomeEvent{
		Enabled:   true,
		ChannelID: "2",
		Message:   "Hello {mention}",
		Embed:     modules.WelcomeEmbed{Enabled: true, Title: "Welcome"},
	}}
	w := welcomeReq(m, cookie, http.MethodGet, "/g/1/welcome", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`data-welcome-panel="join"`,
		`data-welcome-panel="leave"`,
		`id="welcome-data"`,
		"Hello {mention}",
		`href="/g/1/welcome"`,
		"Send test",
		"Variable reference",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestWelcomePageModuleMissing(t *testing.T) {
	m, _, cookie := newWelcomeTestModule(t, false)
	w := welcomeReq(m, cookie, http.MethodGet, "/g/1/welcome", "", "")
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(body, "module is not loaded") {
		t.Fatalf("missing stub message: %s", body)
	}
	if strings.Contains(body, "Send test") {
		t.Fatal("editor controls must not render without the module")
	}
}

func TestWelcomePageNonManager(t *testing.T) {
	m, _, _ := newWelcomeTestModule(t, true)
	m.sessions.put("regular", &userSession{
		userID:    snowflake.ID(2),
		username:  "regular",
		csrfToken: "tok2",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	w := welcomeReq(m, m.signCookie("regular"), http.MethodGet, "/g/1/welcome", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// ── API ──────────────────────────────────────────────────────────────────

func TestWelcomeAPIGet(t *testing.T) {
	m, mod, cookie := newWelcomeTestModule(t, true)
	mod.cfg = modules.WelcomeConfig{Join: modules.WelcomeEvent{Enabled: true, ChannelID: "2", Message: "Hi {user}"}}
	mod.lastErr = map[string]string{"join": "missing access"}
	w := welcomeReq(m, cookie, http.MethodGet, "/api/guilds/1/welcome", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var out struct {
		Config    modules.WelcomeConfig `json:"config"`
		JoinError string                `json:"join_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Config.Join.Message != "Hi {user}" || out.JoinError != "missing access" {
		t.Fatalf("response = %+v", out)
	}
}

func TestWelcomeAPISaveRequiresCSRF(t *testing.T) {
	m, mod, cookie := newWelcomeTestModule(t, true)
	cfg := modules.WelcomeConfig{Join: modules.WelcomeEvent{Enabled: true, ChannelID: "2", Message: "Saved {user}"}}
	body, _ := json.Marshal(cfg)

	w := welcomeReq(m, cookie, http.MethodPost, "/api/guilds/1/welcome", string(body), "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("without CSRF status = %d, want 403", w.Code)
	}
	w = welcomeReq(m, cookie, http.MethodPost, "/api/guilds/1/welcome", string(body), "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := mod.savedConfig().Join.Message; got != "Saved {user}" {
		t.Fatalf("saved message = %q", got)
	}
}

func TestWelcomeAPISaveValidationError(t *testing.T) {
	m, mod, cookie := newWelcomeTestModule(t, true)
	mod.err = errTest
	w := welcomeReq(m, cookie, http.MethodPost, "/api/guilds/1/welcome", `{"join":{}}`, "tok")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "broken") {
		t.Fatalf("body = %s", w.Body.String())
	}
}

func TestWelcomeAPITest(t *testing.T) {
	m, mod, cookie := newWelcomeTestModule(t, true)
	w := welcomeReq(m, cookie, http.MethodPost, "/api/guilds/1/welcome/test", `{"event":"leave"}`, "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if tests := mod.sentTests(); len(tests) != 1 || tests[0] != "leave" {
		t.Fatalf("tests = %+v", tests)
	}
	w = welcomeReq(m, cookie, http.MethodPost, "/api/guilds/1/welcome/test", `{"event":"ban"}`, "tok")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid event status = %d, want 400", w.Code)
	}
}

func TestWelcomeAPIInvalidGuildID(t *testing.T) {
	m, _, cookie := newWelcomeTestModule(t, true)
	for _, gid := range []string{"abc", "0", "01", "-1"} {
		w := welcomeReq(m, cookie, http.MethodGet, "/api/guilds/"+gid+"/welcome", "", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("gid %q status = %d, want 400", gid, w.Code)
		}
	}
}

func TestWelcomeAPIModuleMissing(t *testing.T) {
	m, _, cookie := newWelcomeTestModule(t, false)
	w := welcomeReq(m, cookie, http.MethodGet, "/api/guilds/1/welcome", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestWelcomeAPINonManager(t *testing.T) {
	m, _, _ := newWelcomeTestModule(t, true)
	m.sessions.put("regular", &userSession{
		userID:    snowflake.ID(2),
		username:  "regular",
		csrfToken: "tok2",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	w := welcomeReq(m, m.signCookie("regular"), http.MethodGet, "/api/guilds/1/welcome", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
