package dashboard

import (
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Login throttling. OAuth starts and callbacks are the only unauthenticated
// entry points into the dashboard, so each client IP gets a small token bucket.
// The limits are intentionally generous: a human logs in a handful of times,
// while a script trying to mint OAuth states or replay callbacks hits the wall.
const (
	loginBurst       = 8
	loginRefillEvery = 10 * time.Second // one token per 10s, 6/min sustained
	loginBucketTTL   = 15 * time.Minute
	loginPruneEvery  = 5 * time.Minute
	loginMaxBuckets  = 4096
)

type loginBucket struct {
	tokens float64
	last   time.Time
}

// loginLimiter is a per-key token bucket with lazy pruning. now is injectable
// so tests can advance time without sleeping.
type loginLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*loginBucket
	lastPrune time.Time
	now       func() time.Time
}

func newLoginLimiter() *loginLimiter {
	now := time.Now
	return &loginLimiter{
		buckets:   make(map[string]*loginBucket),
		lastPrune: now(),
		now:       now,
	}
}

// allow consumes one token for key. It returns false with the time to wait
// before the next token is available.
func (l *loginLimiter) allow(key string) (bool, time.Duration) {
	if key == "" {
		key = "unknown"
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &loginBucket{tokens: loginBurst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() / loginRefillEvery.Seconds()
		if b.tokens > loginBurst {
			b.tokens = loginBurst
		}
	}
	b.last = now
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) * float64(loginRefillEvery))
		if wait < time.Second {
			wait = time.Second
		}
		return false, wait
	}
	b.tokens--
	return true, 0
}

// pruneLocked drops idle buckets and evicts the oldest entries if a spoofed
// header flood somehow pushes the map past loginMaxBuckets.
func (l *loginLimiter) pruneLocked(now time.Time) {
	if now.Sub(l.lastPrune) < loginPruneEvery && len(l.buckets) <= loginMaxBuckets {
		return
	}
	for key, b := range l.buckets {
		if now.Sub(b.last) > loginBucketTTL {
			delete(l.buckets, key)
		}
	}
	if len(l.buckets) > loginMaxBuckets {
		type entry struct {
			key  string
			last time.Time
		}
		entries := make([]entry, 0, len(l.buckets))
		for key, b := range l.buckets {
			entries = append(entries, entry{key: key, last: b.last})
		}
		// Oldest first; evict down to the cap. Rare path, so the sort is fine.
		for i := 1; i < len(entries); i++ {
			for j := i; j > 0 && entries[j].last.Before(entries[j-1].last); j-- {
				entries[j], entries[j-1] = entries[j-1], entries[j]
			}
		}
		for i := 0; i < len(entries)-loginMaxBuckets; i++ {
			delete(l.buckets, entries[i].key)
		}
	}
	l.lastPrune = now
}

// loginAllowed applies the per-IP login bucket and writes a 429 with
// Retry-After when the client must wait.
func (m *DashboardModule) loginAllowed(w http.ResponseWriter, r *http.Request) bool {
	if m.loginLimiter == nil {
		return true
	}
	ok, retry := m.loginLimiter.allow(clientIP(r))
	if ok {
		return true
	}
	secs := int(retry / time.Second)
	if retry%time.Second != 0 {
		secs++
	}
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "too many login attempts, try again shortly")
	return false
}
