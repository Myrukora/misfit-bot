package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/oauth2"
	"github.com/disgoorg/snowflake/v2"
)

const (
	sessionCookie = "dsh_session"
	csrfHeader    = "X-CSRF-Token"
	csrfMeta      = "csrf-token"
)

// userSession is an in-memory OAuth session for a logged-in dashboard user.
// Sessions are lost on restart; users simply log in again.
type userSession struct {
	mu       sync.Mutex
	oauth    oauth2.Session
	userID   snowflake.ID
	username string
	avatar   string // Discord CDN avatar URL

	oauthGuilds []discord.OAuth2Guild // cached list of the user's guilds
	guildsAt    time.Time

	csrfToken string
}

type sessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*userSession
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]*userSession)}
}

func (s *sessionStore) put(key string, us *userSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[key] = us
}

func (s *sessionStore) get(key string) (*userSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	us, ok := s.sessions[key]
	return us, ok
}

func (s *sessionStore) del(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, key)
}

func (s *sessionStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = make(map[string]*userSession)
}

// ── Signed session cookie ─────────────────────────────────────────────────
// Cookie value = "<key>.<hex HMAC-SHA256(key, sessionSecret)>" so a tampered
// key can't be substituted for a different valid session.

func (m *DashboardModule) signCookie(key string) string {
	mac := hmac.New(sha256.New, []byte(m.cfg.SessionSecret))
	mac.Write([]byte(key))
	return key + "." + hex.EncodeToString(mac.Sum(nil))
}

func (m *DashboardModule) verifyCookie(raw string) (string, bool) {
	idx := strings.IndexByte(raw, '.')
	if idx <= 0 {
		return "", false
	}
	key := raw[:idx]
	sig := raw[idx+1:]
	mac := hmac.New(sha256.New, []byte(m.cfg.SessionSecret))
	mac.Write([]byte(key))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return "", false
	}
	return key, true
}

// requestIsHTTPS reports whether the browser connection is secure (direct TLS
// or via a TLS-terminating proxy/tunnel that sets X-Forwarded-Proto). The
// session cookie's Secure flag must match the scheme the browser ACTUALLY
// sees, not the configured public_url: on a plain-http LAN origin a Secure
// cookie would silently never be stored and login would loop forever.
func (m *DashboardModule) requestIsHTTPS(r *http.Request) bool {
	return m.requestScheme(r) == "https"
}

func (m *DashboardModule) setSessionCookie(w http.ResponseWriter, r *http.Request, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: raw, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: m.requestIsHTTPS(r),
		MaxAge: 86400 * 7,
	})
}

func (m *DashboardModule) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: m.requestIsHTTPS(r),
		MaxAge: -1,
	})
}

func (m *DashboardModule) sessionFromCookie(r *http.Request) (*userSession, string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, "", false
	}
	key, ok := m.verifyCookie(c.Value)
	if !ok {
		return nil, "", false
	}
	us, ok := m.sessions.get(key)
	if !ok {
		return nil, "", false
	}
	return us, key, true
}

// authMiddleware attaches any valid session to the request context and refreshes
// an expired OAuth token. It never blocks — handlers decide whether auth is
// required (and redirect to /login if not).
func (m *DashboardModule) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		us, _, ok := m.sessionFromCookie(r)
		if ok && m.oauth != nil {
			func() {
				us.mu.Lock()
				defer us.mu.Unlock()
				if us.oauth.Expired() {
					if refreshed, err := m.oauth.VerifySession(us.oauth); err == nil {
						us.oauth = refreshed
					}
				}
			}()
			r = r.WithContext(setSession(r.Context(), us))
		}
		next.ServeHTTP(w, r)
	})
}

// ── OAuth routes ───────────────────────────────────────────────────────────

func (m *DashboardModule) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if m.oauth == nil || !m.configured() {
		m.renderSetup(w, r)
		return
	}
	if sessionOf(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	m.renderLogin(w, r)
}

func (m *DashboardModule) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	if m.oauth == nil || !m.configured() {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	url := m.oauth.GenerateAuthorizationURL(oauth2.AuthorizationURLParams{
		RedirectURI: m.redirectBaseURL(r) + "/callback",
		Scopes:      []discord.OAuth2Scope{discord.OAuth2ScopeIdentify, discord.OAuth2ScopeGuilds},
	})
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (m *DashboardModule) handleCallback(w http.ResponseWriter, r *http.Request) {
	if m.oauth == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	if code == "" || state == "" {
		writeError(w, http.StatusBadRequest, "missing code or state parameter")
		return
	}
	sess, _, err := m.oauth.StartSession(code, state)
	if err != nil {
		writeError(w, http.StatusBadGateway, "Discord login failed: "+err.Error())
		return
	}
	user, err := m.oauth.GetUser(sess)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not fetch Discord user: "+err.Error())
		return
	}
	guilds, err := m.oauth.GetGuilds(sess)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not fetch your guilds: "+err.Error())
		return
	}
	if !m.sharesMutualGuild(guilds) {
		// Friendly denial — this person shares no server with the bot.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(denialPage(m.botIdentity().Name)))
		return
	}
	key, err := randHex(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	csrf, err := randHex(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	us := &userSession{
		oauth:       sess,
		userID:      user.ID,
		username:    user.Username,
		avatar:      user.EffectiveAvatarURL(),
		oauthGuilds: guilds,
		guildsAt:    time.Now(),
		csrfToken:   csrf,
	}
	m.sessions.put(key, us)
	m.setSessionCookie(w, r, m.signCookie(key))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (m *DashboardModule) handleLogout(w http.ResponseWriter, r *http.Request) {
	if _, key, ok := m.sessionFromCookie(r); ok {
		m.sessions.del(key)
	}
	m.clearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// sharesMutualGuild reports whether the OAuth user and the bot share at least
// one guild (applied to the optional allowed_guilds allowlist).
func (m *DashboardModule) sharesMutualGuild(guilds []discord.OAuth2Guild) bool {
	botSet := m.botGuildIDs()
	if len(botSet) == 0 {
		return false // bot not in any guild: refuse everyone
	}
	for _, g := range guilds {
		if botSet[g.ID.String()] && m.allowed(g.ID.String()) {
			return true
		}
	}
	return false
}

// checkCSRF validates the X-CSRF-Token header against the session's token.
func (m *DashboardModule) checkCSRF(r *http.Request) bool {
	us := sessionOf(r)
	if us == nil {
		return false
	}
	return hmac.Equal([]byte(r.Header.Get(csrfHeader)), []byte(us.csrfToken))
}

// denialPage is the friendly "no shared server" message shown at /callback.
func denialPage(botName string) string {
	return fmt.Sprintf(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<title>Login denied</title></head><body style="font-family:system-ui;background:#1e1f22;color:#f2f3f5;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">
<div style="max-width:460px;text-align:center">
<h1 style="color:#ed4245">Access denied</h1>
<p>You must share at least one server with <strong>%s</strong> to use its dashboard.</p>
<p style="opacity:.7">This is a security restriction. Ask a server admin to invite the bot if you believe this is wrong.</p>
</div></body></html>`, botName)
}
