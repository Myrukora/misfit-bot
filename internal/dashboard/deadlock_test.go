package dashboard

import (
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
)

// testLogger is a no-op modules.Logger for tests.
type deadlockLogger struct{}

func (deadlockLogger) Debug(string, ...any) {}
func (deadlockLogger) Info(string, ...any)  {}
func (deadlockLogger) Warn(string, ...any)  {}
func (deadlockLogger) Error(string, ...any) {}

// TestStartServerNoSelfDeadlock is a regression test for a self-deadlock that
// made [p]load dashboard hang the whole bot (and killed the Discord gateway):
// startServer() used to call effectiveListen() WHILE holding m.mu, and
// effectiveListen() also locks m.mu — sync.Mutex is non-reentrant, so the same
// goroutine deadlocked forever. OnLoad never returned, the gateway event
// goroutine (which runs [p]load synchronously) blocked, heartbeat ACKs went
// unread, and the gateway went zombie → invalid session → reconnect failure.
//
// We use a NON-nil client (so startServer passes the nil-client check and
// reaches the formerly-deadlocking effectiveListen() call) plus a deliberately
// invalid listen address so net.Listen fails fast and no real server starts.
// If someone moves effectiveListen() back under m.mu, this test hangs and fails.
func TestStartServerNoSelfDeadlock(t *testing.T) {
	m := &DashboardModule{
		cfg:    &DashboardConfig{Listen: "bad-addr-no-port"}, // net.Listen fails instantly
		mu:     sync.Mutex{},
		logger: deadlockLogger{},
		client: &bot.Client{}, // non-nil so the nil-client guard is passed
		// m.bot is nil → coreConfig() returns nil → effectiveListen() uses m.cfg.Listen
	}
	done := make(chan error, 1)
	go func() { done <- m.startServer() }()
	select {
	case err := <-done:
		// It returned (with a net.Listen error). The point is that it returned
		// at all — the old code deadlocked here.
		_ = err
	case <-time.After(3 * time.Second):
		t.Fatal("startServer deadlocked — effectiveListen() is being called under m.mu again (sync.Mutex is non-reentrant)")
	}
}
