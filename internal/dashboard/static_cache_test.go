package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStaticAssetsAreNotStaleable pins the fix for browsers serving an old
// stylesheet across bot updates: embedded assets carry no mtime/ETag, so the
// /static handler must force revalidation (templates additionally version the
// URLs with ?v=<bot version>).
func TestStaticAssetsAreNotStaleable(t *testing.T) {
	m := &DashboardModule{
		cfg:      &DashboardConfig{SessionSecret: "test-secret", Listen: "127.0.0.1:0"},
		sessions: newSessionStore(),
		logger:   deadlockLogger{},
	}
	h := m.buildHandler()
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	body := w.Body.String()
	if !strings.Contains(body, `input[type="checkbox"]`) {
		t.Error("served stylesheet is missing the checkbox styling")
	}
	if !strings.Contains(body, "appearance: none") {
		t.Error("served stylesheet is missing appearance: none")
	}
}
