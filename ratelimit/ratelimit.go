// Package ratelimit provides per-user, per-command rate limiting to prevent spam.
package ratelimit

import (
	"sync"
	"time"
)

// Config holds rate limiter configuration.
type Config struct {
	// MaxCommands is the maximum number of commands a user can execute
	// within the Window duration before being rate limited.
	MaxCommands int
	// Window is the time window for rate limit tracking.
	Window time.Duration
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() Config {
	return Config{
		MaxCommands: 10,
		Window:      5 * time.Second,
	}
}

// Entry tracks a single user's command history.
type entry struct {
	timestamps []time.Time
}

// Limiter tracks command usage per user and enforces rate limits.
type Limiter struct {
	config Config
	mu     sync.Mutex
	users  map[string]*entry // userID -> entry
}

// New creates a new rate limiter with the given config.
func New(cfg Config) *Limiter {
	return &Limiter{
		config: cfg,
		users:  make(map[string]*entry),
	}
}

// Allow checks if the user is allowed to execute a command.
// If the user is rate limited, it returns false and the remaining wait time.
// Allowed attempts are recorded; use [Limiter.Status] to inspect a user's
// state without recording a command execution.
func (l *Limiter) Allow(userID string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	e := l.pruneAndGet(userID, now)

	if len(e.timestamps) >= l.config.MaxCommands {
		return false, waitUntil(e.timestamps[0], l.config.Window, now)
	}

	// Allow the command and record it
	e.timestamps = append(e.timestamps, now)
	return true, 0
}

// Status reports whether the user is currently rate limited and the remaining
// wait time, WITHOUT recording a new command execution. It prunes expired
// timestamps so repeated status checks don't accumulate memory.
func (l *Limiter) Status(userID string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	e := l.pruneAndGet(userID, now)

	if len(e.timestamps) >= l.config.MaxCommands {
		return false, waitUntil(e.timestamps[0], l.config.Window, now)
	}
	return true, 0
}

// pruneAndGet drops timestamps outside the window and returns the user's
// entry, creating it if needed. Callers must hold l.mu.
func (l *Limiter) pruneAndGet(userID string, now time.Time) *entry {
	windowStart := now.Add(-l.config.Window)

	e, ok := l.users[userID]
	if !ok {
		e = &entry{}
		l.users[userID] = e
	}

	// Remove timestamps outside the window
	valid := make([]time.Time, 0, len(e.timestamps))
	for _, ts := range e.timestamps {
		if ts.After(windowStart) {
			valid = append(valid, ts)
		}
	}
	e.timestamps = valid
	return e
}

// waitUntil returns how long until the given timestamp leaves the window.
func waitUntil(oldest time.Time, window time.Duration, now time.Time) time.Duration {
	wait := oldest.Add(window).Sub(now)
	if wait < 0 {
		wait = 0
	}
	return wait
}

// Reset removes all rate limit data for a user.
func (l *Limiter) Reset(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.users, userID)
}

// ResetAll removes all rate limit data.
func (l *Limiter) ResetAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.users = make(map[string]*entry)
}
