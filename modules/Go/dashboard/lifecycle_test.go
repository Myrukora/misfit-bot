package main

import (
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/bot"
)

// TestUnloadPreventsRestart is a regression test for the zombie-server race:
// an in-flight rebindSoon goroutine used to be able to restart the HTTP server
// AFTER OnUnload, leaving an unloaded module's server running (and serving the
// dead module's handlers). OnUnload must mark the module stopped so a late
// startServer is refused.
func TestUnloadPreventsRestart(t *testing.T) {
	m := &DashboardModule{
		cfg:    &DashboardConfig{Listen: "127.0.0.1:0"}, // ephemeral port, real bind
		mu:     sync.Mutex{},
		logger: deadlockLogger{},
		client: &bot.Client{},
	}
	if err := m.startServer(); err != nil {
		t.Fatalf("startServer: %v", err)
	}
	if !m.isRunning() {
		t.Fatal("expected server running after startServer")
	}
	if err := m.OnUnload(); err != nil {
		t.Fatalf("OnUnload: %v", err)
	}
	if m.isRunning() {
		t.Fatal("server still running after OnUnload")
	}
	// A late rebind (the old rebindSoon race) must be refused, not resurrect
	// the server.
	if err := m.startServer(); err == nil {
		t.Fatal("startServer after OnUnload should refuse (stopped flag)")
	}
	if m.isRunning() {
		t.Fatal("server resurrected after unload")
	}
}

// TestRestartServerLifecycle exercises the stop→start handoff through the
// serve-goroutine WaitGroup: restartServer must never deadlock (or silently
// return without a server) and must end with exactly one running server.
func TestRestartServerLifecycle(t *testing.T) {
	m := &DashboardModule{
		cfg:    &DashboardConfig{Listen: "127.0.0.1:0"},
		mu:     sync.Mutex{},
		logger: deadlockLogger{},
		client: &bot.Client{},
	}
	if err := m.startServer(); err != nil {
		t.Fatalf("startServer: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- m.restartServer() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("restartServer: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restartServer deadlocked — serve goroutine not reaped (serveWG.Wait)")
	}
	if !m.isRunning() {
		t.Fatal("expected server running after restart")
	}
	// Different-address path: rebind on a NEW address while the old server
	// serves, exercising bind-new-listener → stop → startServerWithListener
	// (including the caller-supplied listener close on early returns).
	m.mu.Lock()
	m.cfg.Listen = "localhost:0" // distinct from 127.0.0.1:0 → different-address branch
	m.mu.Unlock()
	done = make(chan error, 1)
	go func() { done <- m.restartServer() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("restartServer (different address): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restartServer (different address) deadlocked")
	}
	if !m.isRunning() {
		t.Fatal("expected server running after different-address restart")
	}
	m.stopServer()
	if m.isRunning() {
		t.Fatal("server still running after stopServer")
	}
}
