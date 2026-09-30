package tickets

import (
	"sync"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

// TestBackgroundLoopsStop verifies that the three background goroutines
// (mirror worker, reconcile sweep, retention loop) return promptly once the
// stop channel is closed, and that a second OnUnload does not double-close.
func TestBackgroundLoopsStop(t *testing.T) {
	dataDir := t.TempDir()
	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}

	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{}},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{},
	}

	// Override the delays to small values so the loops exit quickly.
	m.reconcileDelay = 10 * time.Millisecond
	m.retentionFirstDelay = 10 * time.Millisecond
	m.retentionInterval = 20 * time.Millisecond

	// Create the stop channel and wire it into the mirror queue.
	m.stopCh = make(chan struct{})
	m.stopOnce = new(sync.Once)
	m.mirror = newMirrorQueue(m.ctx.Logger)
	m.mirror.setStop(m.stopCh)
	m.loaded = true

	stop := m.stopCh
	done := make(chan struct{})
	go func() {
		// Run all three loops in parallel; each returns when stop is closed.
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); m.mirror.run(dataDir, st, stop) }()
		go func() { defer wg.Done(); m.reconcileOpenTickets(stop) }()
		go func() { defer wg.Done(); m.retentionLoop(stop) }()
		wg.Wait()
		close(done)
	}()

	// Give the loops a moment to start, then close the stop channel.
	time.Sleep(50 * time.Millisecond)
	m.stopOnce.Do(func() { close(stop) })

	// Wait for all three loops to exit (5s cap).
	select {
	case <-done:
		// All loops exited.
	case <-time.After(5 * time.Second):
		t.Fatal("background loops did not exit within 5s of stop close")
	}

	// A second OnUnload must not double-close / panic.
	if err := m.OnUnload(); err != nil {
		t.Fatalf("second OnUnload: %v", err)
	}
}
