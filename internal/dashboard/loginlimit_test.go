package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginLimiterBurstAndRefill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newLoginLimiter()
	l.now = func() time.Time { return now }
	l.lastPrune = now

	for i := 0; i < loginBurst; i++ {
		if ok, _ := l.allow("203.0.113.5"); !ok {
			t.Fatalf("attempt %d within burst was rejected", i+1)
		}
	}
	ok, retry := l.allow("203.0.113.5")
	if ok {
		t.Fatal("attempt past the burst should be rejected")
	}
	if retry <= 0 || retry > loginRefillEvery {
		t.Fatalf("retry = %v, want (0, %v]", retry, loginRefillEvery)
	}

	// One token refills after loginRefillEvery.
	now = now.Add(loginRefillEvery)
	if ok, _ := l.allow("203.0.113.5"); !ok {
		t.Fatal("attempt after one refill should pass")
	}
	// Other clients are unaffected.
	if ok, _ := l.allow("198.51.100.7"); !ok {
		t.Fatal("a different IP must have its own bucket")
	}
}

func TestLoginLimiterPrunesIdleBuckets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newLoginLimiter()
	l.now = func() time.Time { return now }
	l.lastPrune = now

	l.allow("203.0.113.5")
	now = now.Add(loginBucketTTL + loginPruneEvery + time.Second)
	l.allow("198.51.100.7")

	l.mu.Lock()
	_, stale := l.buckets["203.0.113.5"]
	l.mu.Unlock()
	if stale {
		t.Fatal("idle bucket was not pruned")
	}
}

// A throttled /login start must answer 429 with Retry-After instead of
// starting another OAuth flow.
func TestLoginStartRateLimited(t *testing.T) {
	m := &DashboardModule{loginLimiter: newLoginLimiter()}
	for i := 0; i < loginBurst; i++ {
		m.loginLimiter.allow("203.0.113.9")
	}

	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.9:4321"
	w := httptest.NewRecorder()
	m.handleLoginStart(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	if got := w.Header().Get("Retry-After"); got == "" || got == "0" {
		t.Fatalf("Retry-After = %q, want a positive value", got)
	}
}
