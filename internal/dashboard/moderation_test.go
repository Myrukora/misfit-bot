package dashboard

import (
	"encoding/json"
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

type moderationStubManager map[string]modules.Module

func (s moderationStubManager) Get(name string) (modules.Module, bool) {
	m, ok := s[name]
	return m, ok
}

type removedWarning struct {
	id   int
	by   string
	tag  string
	note string
}

type moderationStubModule struct {
	modules.Module
	mu        sync.Mutex
	cfg       modules.ModerationConfig
	saved     modules.ModerationConfig
	cases     []modules.ModerationCase
	filter    modules.ModerationCaseFilter
	removed   []removedWarning
	lastErr   string
	saveErr   error
	removeErr error
}

func (s *moderationStubModule) GetGuildConfig(string) (modules.ModerationConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, nil
}

func (s *moderationStubModule) SetGuildConfig(_ string, cfg modules.ModerationConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = cfg
	s.cfg = cfg
	return nil
}

func (s *moderationStubModule) ListCases(_ string, f modules.ModerationCaseFilter) ([]modules.ModerationCase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.filter = f
	return append([]modules.ModerationCase(nil), s.cases...), nil
}

func (s *moderationStubModule) RemoveWarning(_ string, id int, by, tag, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.removeErr != nil {
		return s.removeErr
	}
	s.removed = append(s.removed, removedWarning{id: id, by: by, tag: tag, note: reason})
	return nil
}

func (s *moderationStubModule) LastError(string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *moderationStubModule) lastFilter() modules.ModerationCaseFilter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.filter
}

func (s *moderationStubModule) savedConfig() modules.ModerationConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved
}

type moderationFakeBot struct {
	commands.Interface
	owner string
	mgr   any
}

func (f *moderationFakeBot) MemberIntentEnabled() bool { return true }
func (f *moderationFakeBot) IsOwner(id string) bool    { return id == f.owner }
func (f *moderationFakeBot) IsElevated(string) bool    { return false }
func (f *moderationFakeBot) GetModuleManager() any     { return f.mgr }
func (f *moderationFakeBot) GetVersion() string        { return "test" }

func newModerationTestModule(t *testing.T, withModule bool) (*DashboardModule, *moderationStubModule, string) {
	t.Helper()
	mod := &moderationStubModule{}
	mgr := moderationStubManager{}
	if withModule {
		mgr["moderation"] = mod
	}
	m := &DashboardModule{
		bot:      &moderationFakeBot{owner: "1", mgr: mgr},
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
	key := "moderation-owner"
	m.sessions.put(key, &userSession{
		userID:    snowflake.ID(1),
		username:  "owner",
		csrfToken: "tok",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	return m, mod, m.signCookie(key)
}

func moderationReq(m *DashboardModule, cookie, method, target, body, csrf string) *httptest.ResponseRecorder {
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

func TestModerationPageRenders(t *testing.T) {
	m, mod, cookie := newModerationTestModule(t, true)
	now := time.Now().UTC()
	future := now.Add(48 * time.Hour)
	mod.cfg = modules.ModerationConfig{
		Enabled:          true,
		AuditChannel:     "c1",
		WarnChannel:      "c2",
		WarnNotify:       true,
		WarnExpiryDays:   30,
		BanDeleteSeconds: 60,
		Rules: []modules.ModerationRule{
			{Threshold: 2, Action: modules.ModerationActionTimeout, DurationMinutes: 60},
			{Threshold: 4, Action: modules.ModerationActionBan},
		},
	}
	mod.cases = []modules.ModerationCase{
		{ID: 1, Type: modules.ModerationTypeWarn, TargetID: "20", TargetTag: "Target", ModeratorID: "10", ModeratorTag: "Mod", Reason: "spam", CreatedAt: now, ExpiresAt: &future},
		{ID: 2, Type: modules.ModerationTypeBan, TargetID: "21", TargetTag: "Raider", ModeratorID: "10", ModeratorTag: "Mod", Reason: "raid", CreatedAt: now},
		{ID: 3, Type: modules.ModerationTypeWarn, TargetID: "22", TargetTag: "Reformed", ModeratorID: "10", ModeratorTag: "Mod", Reason: "old", CreatedAt: now, Removed: true},
	}
	mod.lastErr = "missing access"
	w := moderationReq(m, cookie, http.MethodGet, "/g/1/moderation", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"Automatic escalation",
		"Audit log",
		"data-mod-unwarn=\"1\"",
		"Unknown channel",
		"missing access",
		"Warning",
		"Ban",
		"Active",
		"Removed",
		"Save changes",
		`href="/g/1/moderation"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestModerationPageModuleMissing(t *testing.T) {
	m, _, cookie := newModerationTestModule(t, false)
	w := moderationReq(m, cookie, http.MethodGet, "/g/1/moderation", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "module is not loaded") {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestModerationPageNonManager(t *testing.T) {
	m, _, _ := newModerationTestModule(t, true)
	m.sessions.put("regular", &userSession{
		userID:    snowflake.ID(2),
		username:  "regular",
		csrfToken: "tok2",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	w := moderationReq(m, m.signCookie("regular"), http.MethodGet, "/g/1/moderation", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestModerationAPIGet(t *testing.T) {
	m, mod, cookie := newModerationTestModule(t, true)
	mod.cfg = modules.ModerationConfig{Enabled: true, AuditChannel: "c1"}
	mod.lastErr = "boom"
	w := moderationReq(m, cookie, http.MethodGet, "/api/guilds/1/moderation", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	var out struct {
		Config    modules.ModerationConfig `json:"config"`
		LastError string                   `json:"last_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Config.Enabled || out.Config.AuditChannel != "c1" || out.LastError != "boom" {
		t.Fatalf("response = %+v", out)
	}
}

func TestModerationAPISave(t *testing.T) {
	m, mod, cookie := newModerationTestModule(t, true)
	cfg := modules.ModerationConfig{
		Enabled: true,
		Rules:   []modules.ModerationRule{{Threshold: 3, Action: modules.ModerationActionKick}},
	}
	body, _ := json.Marshal(cfg)
	w := moderationReq(m, cookie, http.MethodPost, "/api/guilds/1/moderation", string(body), "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("without CSRF = %d, want 403", w.Code)
	}
	w = moderationReq(m, cookie, http.MethodPost, "/api/guilds/1/moderation", string(body), "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if got := mod.savedConfig(); !got.Enabled || len(got.Rules) != 1 {
		t.Fatalf("saved = %+v", got)
	}
}

func TestModerationAPISaveValidation(t *testing.T) {
	m, mod, cookie := newModerationTestModule(t, true)
	mod.saveErr = errTest
	w := moderationReq(m, cookie, http.MethodPost, "/api/guilds/1/moderation", `{}`, "tok")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "broken") {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
}

func TestModerationAPICases(t *testing.T) {
	m, mod, cookie := newModerationTestModule(t, true)
	w := moderationReq(m, cookie, http.MethodGet, "/api/guilds/1/moderation/cases?type=warn&user=20&limit=5", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	f := mod.lastFilter()
	if f.Type != modules.ModerationTypeWarn || f.UserID != "20" || f.Limit != 5 {
		t.Fatalf("filter = %+v", f)
	}
}

func TestModerationAPIRemoveWarning(t *testing.T) {
	m, mod, cookie := newModerationTestModule(t, true)
	w := moderationReq(m, cookie, http.MethodDelete, "/api/guilds/1/moderation/cases/1", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("without CSRF = %d, want 403", w.Code)
	}
	w = moderationReq(m, cookie, http.MethodDelete, "/api/guilds/1/moderation/cases/1", "", "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	if len(mod.removed) != 1 || mod.removed[0].id != 1 || mod.removed[0].by != "1" {
		t.Fatalf("removed = %+v", mod.removed)
	}
	w = moderationReq(m, cookie, http.MethodDelete, "/api/guilds/1/moderation/cases/abc", "", "tok")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid id = %d, want 400", w.Code)
	}
}

func TestModerationAPIInvalidGuild(t *testing.T) {
	m, _, cookie := newModerationTestModule(t, true)
	for _, gid := range []string{"abc", "0", "01"} {
		w := moderationReq(m, cookie, http.MethodGet, "/api/guilds/"+gid+"/moderation", "", "")
		if w.Code != http.StatusBadRequest {
			t.Errorf("gid %q = %d, want 400", gid, w.Code)
		}
	}
}

func TestModerationAPIModuleMissing(t *testing.T) {
	m, _, cookie := newModerationTestModule(t, false)
	w := moderationReq(m, cookie, http.MethodGet, "/api/guilds/1/moderation", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestModerationAPINonManager(t *testing.T) {
	m, _, _ := newModerationTestModule(t, true)
	m.sessions.put("regular", &userSession{
		userID:    snowflake.ID(2),
		username:  "regular",
		csrfToken: "tok2",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	w := moderationReq(m, m.signCookie("regular"), http.MethodGet, "/api/guilds/1/moderation", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
