package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/bot"
)

// newLoginTestModule builds a dashboard module with OAuth configured (valid
// client ID + secret) so handleLoginStart can issue a real authorization URL
// without a bot.
func newLoginTestModule(t *testing.T, publicURL string) *DashboardModule {
	t.Helper()
	m := &DashboardModule{
		cfg: &DashboardConfig{
			Listen:       "127.0.0.1:0",
			PublicURL:    publicURL,
			ClientID:     "123456789012345678",
			ClientSecret: "test-secret",
		},
		mu:     sync.Mutex{},
		logger: deadlockLogger{},
		client: &bot.Client{},
	}
	m.refreshOAuth()
	return m
}

// TestLoginStartRedirectURI is a regression test for the login button: the
// page must POST to /login (the router only starts OAuth on non-GET), and the
// authorization URL must carry exactly the redirect URI the owner registered
// in the Developer Portal — a scheme/host mismatch there makes Discord reject
// the whole login.
func TestLoginStartRedirectURI(t *testing.T) {
	cases := []struct {
		name      string
		publicURL string
		host      string
		forwarded string
		want      string
	}{
		{
			name:      "reverse proxy with X-Forwarded-Proto",
			host:      "test.myrukora.dpdns.org",
			forwarded: "https",
			want:      "https://test.myrukora.dpdns.org/callback",
		},
		{
			name: "plain http host",
			host: "192.168.1.5:8080",
			want: "http://192.168.1.5:8080/callback",
		},
		{
			name:      "public_url wins over request origin",
			publicURL: "https://test.myrukora.dpdns.org",
			host:      "127.0.0.1:8080",
			want:      "https://test.myrukora.dpdns.org/callback",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newLoginTestModule(t, tc.publicURL)
			r := httptest.NewRequest(http.MethodPost, "/login", nil)
			r.Host = tc.host
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			w := httptest.NewRecorder()
			m.handleLoginStart(w, r)

			res := w.Result()
			if res.StatusCode != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusSeeOther)
			}
			loc, err := url.Parse(res.Header.Get("Location"))
			if err != nil {
				t.Fatalf("parse Location: %v", err)
			}
			if loc.Host != "discord.com" || loc.Path != "/oauth2/authorize" {
				t.Fatalf("Location = %q, want the Discord authorize endpoint", res.Header.Get("Location"))
			}
			if got := loc.Query().Get("redirect_uri"); got != tc.want {
				t.Fatalf("redirect_uri = %q, want %q", got, tc.want)
			}
		})
	}
}