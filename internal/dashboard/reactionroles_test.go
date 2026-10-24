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

type rrStubManager map[string]modules.Module

func (s rrStubManager) Get(name string) (modules.Module, bool) {
	m, ok := s[name]
	return m, ok
}

type rrDeleteCall struct {
	id      int
	message bool
}

type rrStubModule struct {
	modules.Module
	mu      sync.Mutex
	cfg     modules.ReactionRolesConfig
	created modules.ReactionPanel
	updated modules.ReactionPanel
	posted  []int
	deleted []rrDeleteCall
	lastErr string
	err     error
}

func (s *rrStubModule) GetGuildConfig(string) (modules.ReactionRolesConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, nil
}

func (s *rrStubModule) CreatePanel(_ string, p modules.ReactionPanel) (modules.ReactionPanel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return p, s.err
	}
	p.ID = 7
	s.created = p
	return p, nil
}

func (s *rrStubModule) UpdatePanel(_ string, p modules.ReactionPanel) (modules.ReactionPanel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return p, s.err
	}
	s.updated = p
	return p, nil
}

func (s *rrStubModule) DeletePanel(_ string, id int, deleteMessage bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, rrDeleteCall{id: id, message: deleteMessage})
	return s.err
}

func (s *rrStubModule) PostPanel(_ string, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posted = append(s.posted, id)
	return s.err
}

func (s *rrStubModule) LastError(string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

type rrFakeBot struct {
	commands.Interface
	owner string
	mgr   any
}

func (f *rrFakeBot) MemberIntentEnabled() bool { return true }
func (f *rrFakeBot) IsOwner(id string) bool    { return id == f.owner }
func (f *rrFakeBot) IsElevated(string) bool    { return false }
func (f *rrFakeBot) GetModuleManager() any     { return f.mgr }
func (f *rrFakeBot) GetVersion() string        { return "test" }

func newRRDashboardModule(t *testing.T, withModule bool) (*DashboardModule, *rrStubModule, string) {
	t.Helper()
	mod := &rrStubModule{}
	mgr := rrStubManager{}
	if withModule {
		mgr["reactionroles"] = mod
	}
	m := &DashboardModule{
		bot:      &rrFakeBot{owner: "1", mgr: mgr},
		botName:  "TestBot",
		cfg:      &DashboardConfig{SessionSecret: "test-secret", Listen: "127.0.0.1:0"},
		client:   &bot.Client{Caches: cache.New(cache.WithCaches(cache.FlagEmojis))},
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
	m.client.Caches.AddEmoji(discord.Emoji{ID: 123, GuildID: 1, Name: "wave", Available: true})
	key := "rr-owner"
	m.sessions.put(key, &userSession{
		userID:    snowflake.ID(1),
		username:  "owner",
		csrfToken: "tok",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	return m, mod, m.signCookie(key)
}

func rrReq(m *DashboardModule, cookie, method, target, body, csrf string) *httptest.ResponseRecorder {
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

func rrSamplePanel() modules.ReactionPanel {
	return modules.ReactionPanel{
		ID:              1,
		Name:            "Pick roles",
		Enabled:         true,
		ChannelID:       "c1",
		MessageID:       "500",
		PostedChannelID: "c1",
		PostedEmojis:    []string{"👍"},
		RemoveOnUnreact: true,
		Embed: modules.ReactionRoleEmbed{
			Title:       "Choose your roles",
			Description: "React below.",
			Color:       "#5865f2",
		},
		Mappings: []modules.ReactionRoleMapping{{Emoji: "👍", RoleID: "r1"}},
	}
}

func TestReactionRolesPageRenders(t *testing.T) {
	m, mod, cookie := newRRDashboardModule(t, true)
	mod.cfg = modules.ReactionRolesConfig{NextID: 2, Panels: []modules.ReactionPanel{rrSamplePanel()}}
	w := rrReq(m, cookie, http.MethodGet, "/g/1/reactionroles?panel=1", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		"Reaction roles",
		"Pick roles",
		"Choose your roles",
		"rr-mapping",
		"rr-save-post",
		"Posted",
		"wave:123",
		":wave:",
		"Unknown channel",
		`href="/g/1/reactionroles"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestReactionRolesPageNewAndMissing(t *testing.T) {
	m, mod, cookie := newRRDashboardModule(t, true)
	mod.cfg = modules.ReactionRolesConfig{NextID: 1}
	w := rrReq(m, cookie, http.MethodGet, "/g/1/reactionroles?panel=new", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "New panel") {
		t.Fatalf("new panel page: %d %s", w.Code, w.Body.String())
	}

	m2, _, cookie2 := newRRDashboardModule(t, false)
	w = rrReq(m2, cookie2, http.MethodGet, "/g/1/reactionroles", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "module is not loaded") {
		t.Fatalf("missing module page: %d %s", w.Code, w.Body.String())
	}
}

func TestReactionRolesPageNonManager(t *testing.T) {
	m, _, _ := newRRDashboardModule(t, true)
	m.sessions.put("regular", &userSession{
		userID:    snowflake.ID(2),
		username:  "regular",
		csrfToken: "tok2",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	w := rrReq(m, m.signCookie("regular"), http.MethodGet, "/g/1/reactionroles", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestReactionRolesAPIGet(t *testing.T) {
	m, mod, cookie := newRRDashboardModule(t, true)
	mod.cfg = modules.ReactionRolesConfig{NextID: 2, Panels: []modules.ReactionPanel{rrSamplePanel()}}
	mod.lastErr = "boom"
	w := rrReq(m, cookie, http.MethodGet, "/api/guilds/1/reactionroles", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out struct {
		Config    modules.ReactionRolesConfig `json:"config"`
		LastError string                      `json:"last_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Config.Panels) != 1 || out.LastError != "boom" {
		t.Fatalf("response = %+v", out)
	}
}

func TestReactionRolesAPICreateUpdatePostDelete(t *testing.T) {
	m, mod, cookie := newRRDashboardModule(t, true)
	body := `{"name":"Roles","enabled":true,"channel_id":"c1","mappings":[{"emoji":"👍","role_id":"r1"}]}`
	w := rrReq(m, cookie, http.MethodPost, "/api/guilds/1/reactionroles/panels", body, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("without CSRF = %d", w.Code)
	}
	w = rrReq(m, cookie, http.MethodPost, "/api/guilds/1/reactionroles/panels", body, "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	if mod.created.Name != "Roles" {
		t.Fatalf("created = %+v", mod.created)
	}

	w = rrReq(m, cookie, http.MethodPost, "/api/guilds/1/reactionroles/panels/7", body, "tok")
	if w.Code != http.StatusOK || mod.updated.ID != 7 {
		t.Fatalf("update = %d %+v", w.Code, mod.updated)
	}
	w = rrReq(m, cookie, http.MethodPost, "/api/guilds/1/reactionroles/panels/7/post", "", "tok")
	if w.Code != http.StatusOK || len(mod.posted) != 1 || mod.posted[0] != 7 {
		t.Fatalf("post = %d %+v", w.Code, mod.posted)
	}
	w = rrReq(m, cookie, http.MethodDelete, "/api/guilds/1/reactionroles/panels/7?delete_message=0", "", "tok")
	if w.Code != http.StatusOK || len(mod.deleted) != 1 || mod.deleted[0].message {
		t.Fatalf("delete = %d %+v", w.Code, mod.deleted)
	}
}

func TestReactionRolesAPIValidation(t *testing.T) {
	m, mod, cookie := newRRDashboardModule(t, true)
	mod.err = errTest
	w := rrReq(m, cookie, http.MethodPost, "/api/guilds/1/reactionroles/panels", `{}`, "tok")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "broken") {
		t.Fatalf("status = %d body %s", w.Code, w.Body.String())
	}
	for _, target := range []string{
		"/api/guilds/1/reactionroles/panels/abc",
		"/api/guilds/1/reactionroles/panels/0",
	} {
		w = rrReq(m, cookie, http.MethodPost, target, `{}`, "tok")
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", target, w.Code)
		}
	}
	w = rrReq(m, cookie, http.MethodGet, "/api/guilds/abc/reactionroles", "", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid guild = %d, want 400", w.Code)
	}
}

func TestReactionRolesAPIModuleMissingAndNonManager(t *testing.T) {
	m, _, cookie := newRRDashboardModule(t, false)
	w := rrReq(m, cookie, http.MethodGet, "/api/guilds/1/reactionroles", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing module = %d, want 404", w.Code)
	}

	m2, _, _ := newRRDashboardModule(t, true)
	m2.sessions.put("regular", &userSession{
		userID:    snowflake.ID(2),
		username:  "regular",
		csrfToken: "tok2",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	w = rrReq(m2, m2.signCookie("regular"), http.MethodGet, "/api/guilds/1/reactionroles", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-manager = %d, want 403", w.Code)
	}
}
