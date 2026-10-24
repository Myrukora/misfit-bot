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

	"github.com/misfit/bot/modules"
)

type levelingStubManager map[string]modules.Module

func (s levelingStubManager) Get(name string) (modules.Module, bool) {
	m, ok := s[name]
	return m, ok
}

type levelingSetXP struct {
	UserID string
	XP     int64
}

type levelingStubModule struct {
	modules.Module

	mu        sync.Mutex
	cfg       modules.LevelingConfig
	saved     modules.LevelingConfig
	entries   []modules.LevelingEntry
	total     int
	stats     modules.LevelingStats
	lastErr   string
	setXP     []levelingSetXP
	reset     []string
	synced    []string
	queued    int
	saveErr   error
	syncErr   error
	configErr error
}

func (s *levelingStubModule) GetGuildConfig(string) (modules.LevelingConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.configErr != nil {
		return modules.LevelingConfig{}, s.configErr
	}
	return s.cfg, nil
}

func (s *levelingStubModule) SetGuildConfig(_ string, cfg modules.LevelingConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved = cfg
	s.cfg = cfg
	return nil
}

func (s *levelingStubModule) Leaderboard(_ string, _, limit int) ([]modules.LevelingEntry, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]modules.LevelingEntry(nil), s.entries...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, s.total, nil
}

func (s *levelingStubModule) GetEntry(_ string, userID string) (modules.LevelingEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		if entry.UserID == userID {
			return entry, true, nil
		}
	}
	return modules.LevelingEntry{}, false, nil
}

func (s *levelingStubModule) SetEntryXP(_ string, userID string, xp int64) (modules.LevelingEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setXP = append(s.setXP, levelingSetXP{UserID: userID, XP: xp})
	return modules.LevelingEntry{UserID: userID, XP: xp, Level: 3, Rank: 1}, nil
}

func (s *levelingStubModule) ResetEntry(_ string, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reset = append(s.reset, userID)
	return nil
}

func (s *levelingStubModule) Stats(string) (modules.LevelingStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats, nil
}

func (s *levelingStubModule) StartRewardSync(guildID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.syncErr != nil {
		return 0, s.syncErr
	}
	s.synced = append(s.synced, guildID)
	return s.queued, nil
}

func (s *levelingStubModule) LastError(string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *levelingStubModule) savedConfig() modules.LevelingConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved
}

func (s *levelingStubModule) recorded() (set []levelingSetXP, reset []string, synced []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]levelingSetXP(nil), s.setXP...),
		append([]string(nil), s.reset...),
		append([]string(nil), s.synced...)
}

func newLevelingTestModule(t *testing.T, withModule bool) (*DashboardModule, *levelingStubModule, string) {
	t.Helper()
	stub := &levelingStubModule{}
	mgr := levelingStubManager{}
	if withModule {
		mgr["leveling"] = stub
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
	key := "leveling-owner"
	m.sessions.put(key, &userSession{
		userID:    snowflake.ID(1),
		username:  "owner",
		csrfToken: "tok",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	return m, stub, m.signCookie(key)
}

// levelingRegularCookie creates a signed session for a user who manages no
// guilds, so every guild-scoped route must refuse them.
func levelingRegularCookie(m *DashboardModule) string {
	key := "leveling-regular"
	m.sessions.put(key, &userSession{
		userID:    snowflake.ID(2),
		username:  "regular",
		csrfToken: "tok2",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
	})
	return m.signCookie(key)
}

func levelingReq(m *DashboardModule, cookie, method, target, body, csrf string) *httptest.ResponseRecorder {
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

func TestLevelingPageRenders(t *testing.T) {
	m, stub, cookie := newLevelingTestModule(t, true)
	stub.cfg = modules.LevelingConfig{
		Enabled: true, MessageEnabled: true, MessageXPMin: 15, MessageXPMax: 25,
		MessageCooldownSeconds: 60, VoiceEnabled: true, VoiceXPPerMinute: 10,
		VoiceRequireOthers: true, MaxLevel: 500, AnnounceEnabled: true,
		AnnounceChannel: "c1", AnnounceMessage: "{user} reached **level {level}**!",
		Rewards:         []modules.LevelingReward{{Level: 5, RoleID: "r2"}, {Level: 10, RoleID: "r3"}},
		IgnoredChannels: []string{"c2"}, IgnoredRoles: []string{"r1"},
		CatchupEnabled: true, CatchupMaxMessages: 2000, CatchupMaxChannels: 50,
	}
	stub.entries = []modules.LevelingEntry{{
		UserID: "20", XP: 500, Level: 2, Rank: 1, Progress: 245, NextLevelXP: 220,
		Messages: 10, VoiceSeconds: 1800, UpdatedAt: time.Now().UTC(),
	}}
	stub.total = 1
	stub.stats = modules.LevelingStats{Tracked: 1, TotalXP: 500}
	stub.lastErr = "missing access"

	w := levelingReq(m, cookie, http.MethodGet, "/g/1/leveling", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="lvl-save"`,
		`id="lvl-toggle"`,
		`id="lvl-ignore-channels"`,
		`id="lvl-ignore-roles"`,
		`data-reward-level`,
		`data-reward-role`,
		`Unknown channel (c2)`,
		`Unknown role (r1)`,
		`Unknown channel (c1)`,
		`data-lvl-edit="20"`,
		`data-lvl-reset="20"`,
		`Save changes`,
		`missing access`,
		`Leaderboard`,
		`Leveling`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

func TestLevelingPageWithoutModule(t *testing.T) {
	m, _, cookie := newLevelingTestModule(t, false)
	w := levelingReq(m, cookie, http.MethodGet, "/g/1/leveling", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "is not loaded") {
		t.Error("missing the module-not-loaded notice")
	}
	if strings.Contains(body, `id="lvl-save"`) {
		t.Error("forms must not render when the module is not loaded")
	}
}

func TestLevelingPageFilteredMember(t *testing.T) {
	m, stub, cookie := newLevelingTestModule(t, true)
	stub.entries = []modules.LevelingEntry{
		{UserID: "20", XP: 500, Level: 2, Rank: 1},
		{UserID: "21", XP: 400, Level: 2, Rank: 2},
	}
	stub.total = 2
	w := levelingReq(m, cookie, http.MethodGet, "/g/1/leveling?user=21", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `data-lvl-edit="21"`) {
		t.Error("filtered member is missing from the table")
	}
	if strings.Contains(body, `data-lvl-edit="20"`) {
		t.Error("filtered view must show only the requested member")
	}
}

func TestLevelingAPIRequiresAuth(t *testing.T) {
	m, _, _ := newLevelingTestModule(t, true)
	w := levelingReq(m, "", http.MethodGet, "/api/guilds/1/leveling", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestLevelingAPIRejectsNonManager(t *testing.T) {
	m, _, _ := newLevelingTestModule(t, true)
	cookie := levelingRegularCookie(m)
	w := levelingReq(m, cookie, http.MethodGet, "/api/guilds/1/leveling", "", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestLevelingAPIInvalidGuild(t *testing.T) {
	m, _, cookie := newLevelingTestModule(t, true)
	w := levelingReq(m, cookie, http.MethodGet, "/api/guilds/nope/leveling", "", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestLevelingAPIMissingModule(t *testing.T) {
	m, _, cookie := newLevelingTestModule(t, false)
	w := levelingReq(m, cookie, http.MethodGet, "/api/guilds/1/leveling", "", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestLevelingAPIGet(t *testing.T) {
	m, stub, cookie := newLevelingTestModule(t, true)
	stub.cfg = modules.LevelingConfig{Enabled: true, MaxLevel: 42}
	stub.stats = modules.LevelingStats{Tracked: 3, TotalXP: 900}
	stub.lastErr = "bad role"

	w := levelingReq(m, cookie, http.MethodGet, "/api/guilds/1/leveling", "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var got struct {
		Config    modules.LevelingConfig `json:"config"`
		Stats     modules.LevelingStats  `json:"stats"`
		LastError string                 `json:"last_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Config.Enabled || got.Config.MaxLevel != 42 {
		t.Fatalf("config = %+v", got.Config)
	}
	if got.Stats.Tracked != 3 || got.LastError != "bad role" {
		t.Fatalf("stats/last_error = %+v / %q", got.Stats, got.LastError)
	}
}

func TestLevelingAPISaveConfig(t *testing.T) {
	m, stub, cookie := newLevelingTestModule(t, true)
	body := `{"enabled":true,"message_enabled":true,"message_xp_min":5,"message_xp_max":9,
		"message_cooldown_seconds":30,"voice_enabled":false,"voice_xp_per_minute":3,
		"voice_require_others":true,"max_level":99,"announce_enabled":true,
		"announce_channel":"c1","announce_message":"hi {user}","catchup_enabled":true,
		"catchup_max_messages":100,"catchup_max_channels":10,
		"ignored_channels":["c2"],"ignored_roles":["r1"],
		"rewards":[{"level":3,"role_id":"r2"}]}`

	w := levelingReq(m, cookie, http.MethodPost, "/api/guilds/1/leveling", body, "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	saved := stub.savedConfig()
	if !saved.Enabled || saved.MaxLevel != 99 || saved.MessageXPMin != 5 {
		t.Fatalf("saved config = %+v", saved)
	}
	if len(saved.Rewards) != 1 || saved.Rewards[0].RoleID != "r2" {
		t.Fatalf("saved rewards = %+v", saved.Rewards)
	}
	if len(saved.IgnoredChannels) != 1 || saved.IgnoredChannels[0] != "c2" {
		t.Fatalf("saved ignored channels = %+v", saved.IgnoredChannels)
	}
}

func TestLevelingAPISaveRequiresCSRF(t *testing.T) {
	m, _, cookie := newLevelingTestModule(t, true)
	w := levelingReq(m, cookie, http.MethodPost, "/api/guilds/1/leveling", `{}`, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestLevelingAPIToggleEnabled(t *testing.T) {
	m, stub, cookie := newLevelingTestModule(t, true)
	stub.cfg = modules.LevelingConfig{Enabled: false, MaxLevel: 500}

	w := levelingReq(m, cookie, http.MethodPost, "/api/guilds/1/leveling/enabled", `{"enabled":true}`, "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if !stub.savedConfig().Enabled {
		t.Fatal("the switch did not persist enabled=true")
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["enabled"] != true {
		t.Fatalf("response = %+v, want enabled true", got)
	}
}

func TestLevelingAPIMemberXPAndReset(t *testing.T) {
	m, stub, cookie := newLevelingTestModule(t, true)

	w := levelingReq(m, cookie, http.MethodPost, "/api/guilds/1/leveling/members/20", `{"xp":1234}`, "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	set, reset, _ := stub.recorded()
	if len(set) != 1 || set[0].UserID != "20" || set[0].XP != 1234 {
		t.Fatalf("SetEntryXP calls = %+v", set)
	}

	w = levelingReq(m, cookie, http.MethodDelete, "/api/guilds/1/leveling/members/20", "", "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("reset status = %d, body %s", w.Code, w.Body.String())
	}
	_, reset, _ = stub.recorded()
	if len(reset) != 1 || reset[0] != "20" {
		t.Fatalf("ResetEntry calls = %+v", reset)
	}

	// A non-numeric member ID is rejected before reaching the module.
	w = levelingReq(m, cookie, http.MethodDelete, "/api/guilds/1/leveling/members/nobody", "", "tok")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid member status = %d, want 400", w.Code)
	}
}

func TestLevelingAPISync(t *testing.T) {
	m, stub, cookie := newLevelingTestModule(t, true)
	stub.queued = 7
	stub.stats = modules.LevelingStats{SyncRunning: true, SyncTotal: 7}

	w := levelingReq(m, cookie, http.MethodPost, "/api/guilds/1/leveling/sync", "", "tok")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	_, _, synced := stub.recorded()
	if len(synced) != 1 || synced[0] != "1" {
		t.Fatalf("StartRewardSync calls = %+v", synced)
	}
	var got struct {
		Queued int                   `json:"queued"`
		Stats  modules.LevelingStats `json:"stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Queued != 7 || !got.Stats.SyncRunning {
		t.Fatalf("response = %+v", got)
	}
}
