package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
)

// guardTestBot answers the identity calls guildAPIGuard's authorization path
// makes.
type guardTestBot struct {
	commands.Interface
	owner string
}

func (b *guardTestBot) IsOwner(id string) bool        { return id == b.owner }
func (b *guardTestBot) IsElevated(string) bool        { return false }
func (b *guardTestBot) MemberIntentEnabled() bool     { return true }
func (b *guardTestBot) GetGuildOwnerID(string) string { return "" }

func guardRequest(t *testing.T, m *DashboardModule, us *userSession, method, target, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	if us != nil {
		r = r.WithContext(setSession(context.Background(), us))
	}
	if csrf != "" {
		r.Header.Set(csrfHeader, csrf)
	}
	w := httptest.NewRecorder()
	m.guildAPIGuard(w, r, "100")
	return w
}

func TestGuildAPIGuardRequiresSession(t *testing.T) {
	m := &DashboardModule{bot: &guardTestBot{owner: "1"}}
	w := guardRequest(t, m, nil, http.MethodGet, "/api/guilds/100/welcome", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestGuildAPIGuardRejectsNonManagers(t *testing.T) {
	m := &DashboardModule{bot: &guardTestBot{owner: "1"}}
	us := &userSession{userID: snowflake.ID(2)}
	w := guardRequest(t, m, us, http.MethodGet, "/api/guilds/100/welcome", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestGuildAPIGuardCSRFOnMutations(t *testing.T) {
	m := &DashboardModule{bot: &guardTestBot{owner: "1"}}
	us := &userSession{userID: snowflake.ID(1), csrfToken: "tok"}

	if w := guardRequest(t, m, us, http.MethodGet, "/api/guilds/100/welcome", ""); w.Code != http.StatusOK && w.Code != 200 {
		t.Fatalf("GET status = %d, want pass-through", w.Code)
	}
	if w := guardRequest(t, m, us, http.MethodPost, "/api/guilds/100/welcome", ""); w.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if w := guardRequest(t, m, us, http.MethodPost, "/api/guilds/100/welcome", "tok"); w.Code != http.StatusOK && w.Code != 200 {
		t.Fatalf("POST with CSRF status = %d, want pass-through", w.Code)
	}
}

func TestGuildAPIGuardRejectsInvalidGuildID(t *testing.T) {
	m := &DashboardModule{bot: &guardTestBot{owner: "1"}}
	us := &userSession{userID: snowflake.ID(1)}
	r := httptest.NewRequest(http.MethodGet, "/api/guilds/..%2f/welcome", nil)
	r = r.WithContext(setSession(context.Background(), us))
	w := httptest.NewRecorder()
	m.guildAPIGuard(w, r, "..")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// The route dispatcher must apply the guard before a module handler runs, so a
// non-manager never reaches a guild-scoped API endpoint.
func TestRouteAPIGuildSubtreeRunsGuard(t *testing.T) {
	m := &DashboardModule{bot: &guardTestBot{owner: "1"}}
	us := &userSession{userID: snowflake.ID(2)}
	r := httptest.NewRequest(http.MethodGet, "/api/guilds/100/welcome", nil)
	r = r.WithContext(setSession(context.Background(), us))
	w := httptest.NewRecorder()

	m.routeAPI(w, r, []string{"guilds", "100", "welcome"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// Command execution was removed from the dashboard surface; the endpoint must
// stay gone (404, not a dormant handler).
func TestAPIExecRemoved(t *testing.T) {
	m := &DashboardModule{bot: &guardTestBot{owner: "1"}}
	us := &userSession{userID: snowflake.ID(1), csrfToken: "tok"}
	r := httptest.NewRequest(http.MethodPost, "/api/exec", nil)
	r = r.WithContext(setSession(context.Background(), us))
	r.Header.Set(csrfHeader, "tok")
	w := httptest.NewRecorder()

	m.routeAPI(w, r, []string{"exec"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	if knownAPIPath([]string{"exec"}) {
		t.Fatal("/api/exec must not be a known API path")
	}
}
