package ratelimit

import (
	"testing"
	"time"
)

// TestStatusDoesNotRecord verifies Status is non-mutating: checking a user's
// status must not consume one of their allowed commands (this used to happen —
// the ratelimit status command called Allow, which records a timestamp).
func TestStatusDoesNotRecord(t *testing.T) {
	l := New(Config{MaxCommands: 1, Window: time.Minute})
	user := "u1"

	if allowed, _ := l.Allow(user); !allowed {
		t.Fatal("first Allow should be allowed")
	}
	// At the limit now: Allow is denied.
	if allowed, _ := l.Allow(user); allowed {
		t.Fatal("Allow at the limit should be denied")
	}
	// Repeated Status calls must not record anything.
	for i := 0; i < 5; i++ {
		if allowed, _ := l.Status(user); allowed {
			t.Fatal("Status should report rate limited at the limit")
		}
	}
	// …so the reported wait must shrink over time. If Status recorded, the
	// oldest timestamp would become the latest Status call and the wait would
	// jump back up to a full window.
	_, wait1 := l.Status(user)
	time.Sleep(10 * time.Millisecond)
	_, wait2 := l.Status(user)
	if wait2 >= wait1 {
		t.Errorf("wait time did not decrease across Status calls (wait1=%v, wait2=%v) — Status is recording", wait1, wait2)
	}
}

// TestAllowEnforcesWindow verifies the basic rate-limit contract.
func TestAllowEnforcesWindow(t *testing.T) {
	l := New(Config{MaxCommands: 2, Window: 50 * time.Millisecond})
	user := "u1"

	for i := 0; i < 2; i++ {
		if allowed, _ := l.Allow(user); !allowed {
			t.Fatalf("Allow #%d should be allowed", i+1)
		}
	}
	if allowed, _ := l.Allow(user); allowed {
		t.Fatal("third Allow should be denied")
	}
	// Wait out the window: the user is allowed again.
	time.Sleep(60 * time.Millisecond)
	if allowed, _ := l.Allow(user); !allowed {
		t.Fatal("Allow should succeed after the window expires")
	}
}

// TestResetClearsUser verifies per-user reset.
func TestResetClearsUser(t *testing.T) {
	l := New(Config{MaxCommands: 1, Window: time.Minute})
	l.Allow("u1")
	l.Allow("u2") // u2's own slot is separate
	if allowed, _ := l.Allow("u2"); allowed {
		t.Fatal("u2 at its own limit should be denied")
	}
	l.Reset("u2")
	if allowed, _ := l.Allow("u2"); !allowed {
		t.Fatal("u2 should be allowed after Reset")
	}
	if allowed, _ := l.Allow("u1"); allowed {
		t.Fatal("u1 must be unaffected by resetting u2")
	}
}
