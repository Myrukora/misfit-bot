package dashboard

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/oauth2"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
)

// rlTestBot answers the identity + limiter calls the ratelimit API makes.
type rlTestBot struct {
	commands.Interface
	owner  string
	status map[string]rlStatus
	resets []string
}

type rlStatus struct {
	allowed bool
	wait    time.Duration
}

func (b *rlTestBot) IsOwner(id string) bool    { return id == b.owner }
func (b *rlTestBot) IsElevated(string) bool    { return false }
func (b *rlTestBot) MemberIntentEnabled() bool { return true }
func (b *rlTestBot) StatusRateLimit(id string) (bool, time.Duration) {
	s := b.status[id]
	return s.allowed, s.wait
}
func (b *rlTestBot) ResetRateLimit(id string) { b.resets = append(b.resets, id) }

func rlTestModule(t *testing.T, b *rlTestBot) (*DashboardModule, string) {
	t.Helper()
	m := &DashboardModule{
		bot:      b,
		botName:  "TestBot",
		cfg:      &DashboardConfig{SessionSecret: "test-secret", Listen: "127.0.0.1:0"},
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

// TestRateLimitAPI pins the owner-only status/reset endpoints that replaced the
// /ratelimit Discord command.
func TestRateLimitAPI(t *testing.T) {
	b := &rlTestBot{
		owner:  "1",
		status: map[string]rlStatus{"42": {allowed: false, wait: 30 * time.Second}},
	}
	m, cookie := rlTestModule(t, b)

	w := doAPI(m, cookie, http.MethodGet, "/api/ratelimit/status?user_id=42", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"allowed":false`) || !strings.Contains(body, `"wait_seconds":30`) {
		t.Fatalf("status body = %s, want allowed=false wait_seconds=30", body)
	}

	w = doAPI(m, cookie, http.MethodPost, "/api/ratelimit/reset", `{"user_id":"42"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reset code = %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(b.resets) != 1 || b.resets[0] != "42" {
		t.Fatalf("resets = %v, want [42]", b.resets)
	}

	// Invalid IDs are rejected before touching the limiter.
	if w := doAPI(m, cookie, http.MethodGet, "/api/ratelimit/status?user_id=abc", ""); w.Code != http.StatusBadRequest {
		t.Errorf("invalid status id code = %d, want 400", w.Code)
	}
	if w := doAPI(m, cookie, http.MethodPost, "/api/ratelimit/reset", `{"user_id":"0"}`); w.Code != http.StatusBadRequest {
		t.Errorf("zero reset id code = %d, want 400", w.Code)
	}

	// A regular (non-owner) session is denied even with a valid CSRF token.
	regKey := "regular-key"
	m.sessions.put(regKey, &userSession{
		userID:    snowflake.ID(2),
		username:  "regular",
		csrfToken: "tok",
		expiresAt: time.Now().Add(time.Hour),
		oauth:     oauth2.Session{AccessToken: "t", Expiration: time.Now().Add(time.Hour)},
		// A fresh, non-empty OAuth guild list keeps refreshGuilds offline.
		oauthGuilds: []discord.OAuth2Guild{{ID: snowflake.ID(10)}},
		guildsAt:    time.Now(),
	})
	regCookie := m.signCookie(regKey)
	if w := doAPI(m, regCookie, http.MethodGet, "/api/ratelimit/status?user_id=42", ""); w.Code != http.StatusForbidden {
		t.Errorf("regular user status code = %d, want 403", w.Code)
	}
	if w := doAPI(m, regCookie, http.MethodPost, "/api/ratelimit/reset", `{"user_id":"42"}`); w.Code != http.StatusForbidden {
		t.Errorf("regular user reset code = %d, want 403", w.Code)
	}
}
