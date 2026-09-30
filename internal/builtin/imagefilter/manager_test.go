package imagefilter

import (
	"errors"
	"fmt"
	"image/color"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

// fakeEmbedder counts loads/closes and produces deterministic vectors.
type fakeEmbedder struct {
	inputSize int
	dim       int
}

func (f *fakeEmbedder) Embed(tensor []float32) ([]float32, error) {
	// Deterministic pseudo-embedding from the first few tensor values.
	vec := make([]float32, f.dim)
	for i := range vec {
		vec[i] = tensor[i%len(tensor)] / 255.0
	}
	return l2normalize(vec), nil
}
func (f *fakeEmbedder) InputSize() int { return f.inputSize }
func (f *fakeEmbedder) Close() error   { return nil }

// nopLogger satisfies modules.Logger silently.
type nopLogger struct{}

func (nopLogger) Debug(string, ...any) {}
func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

var _ modules.Logger = nopLogger{}

// newTestManager wires a manager with fakes: no onnxruntime, no model files.
func newTestManager(t *testing.T) (*manager, *config, *images, *atomic.Int32) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	imgs := newImages(dir)
	m := newManager(cfg, imgs, dir, nopLogger{})

	loads := &atomic.Int32{}
	m.loadFn = func(dataDir, variant string) (embedder, error) {
		loads.Add(1)
		return &fakeEmbedder{inputSize: 224, dim: 8}, nil
	}
	return m, cfg, imgs, loads
}

// rgbaOf builds a color for test images.
func rgbaOf(r, g, b uint8) (c color.RGBA) { return color.RGBA{r, g, b, 255} }

func TestManagerWarmColdCycle(t *testing.T) {
	m, _, _, loads := newTestManager(t)
	m.startWorker()
	defer m.stopWorker()

	// First enable → load.
	if err := m.SetGuildEnabled("111", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !m.warm() {
		t.Fatal("model should be warm after first enable")
	}
	if n := loads.Load(); n != 1 {
		t.Fatalf("loads = %d, want 1", n)
	}

	// Second guild → no reload.
	if err := m.SetGuildEnabled("222", true); err != nil {
		t.Fatalf("enable 2: %v", err)
	}
	if n := loads.Load(); n != 1 {
		t.Fatalf("second enable should not reload, loads = %d", n)
	}

	// Disable one → still warm.
	if err := m.SetGuildEnabled("111", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !m.warm() {
		t.Fatal("model should stay warm while one guild remains")
	}

	// Disable the last → cold.
	if err := m.SetGuildEnabled("222", false); err != nil {
		t.Fatalf("disable 2: %v", err)
	}
	if m.warm() {
		t.Fatal("model should be cold with zero enabled guilds")
	}

	// Re-enable → reload.
	if err := m.SetGuildEnabled("222", true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if !m.warm() {
		t.Fatal("model should reload on re-enable")
	}
}

func TestManagerWorkerDetection(t *testing.T) {
	m, cfg, imgs, _ := newTestManager(t)
	m.startWorker()
	defer m.stopWorker()

	// Guild enabled with a reference image; embedder is deterministic, so the
	// same image content as the ref must score ~1.0.
	if err := m.SetGuildEnabled("g1", true); err != nil {
		t.Fatal(err)
	}
	ref := smallPNG(t, 64, 64, rgbaOf(200, 30, 30))
	name, _, err := imgs.Add("g1", ref, "ref.png")
	if err != nil {
		t.Fatal(err)
	}
	_ = name

	var punished atomic.Int32
	var gotScore atomic.Value // float64
	cfgSet := defaultGuildConfig()
	cfgSet.Enabled = true
	cfgSet.Threshold = 0.95
	cfgSet.Punishment = PunishMute
	cfgSet.DeleteOnNone = false
	if err := cfg.SetGuildConfig("g1", cfgSet); err != nil {
		t.Fatal(err)
	}
	m.punishFn = func(j job, score float64, gc GuildConfig) {
		punished.Add(1)
		gotScore.Store(score)
	}
	// fetchFn returns the same image as the ref → cosine ~1.
	m.fetchFn = func(url string) ([]byte, error) { return ref, nil }

	ok := m.submit(job{guildID: "g1", channelID: "c", messageID: "m1", authorID: "u1", imageURL: "https://cdn.discordapp.com/x.png"})
	if !ok {
		t.Fatal("submit failed")
	}
	m.drainForTest(t, 1)

	if n := punished.Load(); n != 1 {
		t.Fatalf("punishments = %d, want 1 (score %v)", n, gotScore.Load())
	}
	if s, _ := gotScore.Load().(float64); s <= 0.95 {
		t.Errorf("score %v should exceed threshold 0.95", s)
	}
}

func TestManagerNoRefsNoPunish(t *testing.T) {
	m, cfg, _, _ := newTestManager(t)
	m.startWorker()
	defer m.stopWorker()

	if err := m.SetGuildEnabled("g2", true); err != nil {
		t.Fatal(err)
	}
	set := defaultGuildConfig()
	set.Enabled = true
	set.Threshold = 0.1 // anything matches if refs existed
	if err := cfg.SetGuildConfig("g2", set); err != nil {
		t.Fatal(err)
	}
	var punished atomic.Int32
	m.punishFn = func(job, float64, GuildConfig) { punished.Add(1) }
	m.fetchFn = func(string) ([]byte, error) { return smallPNG(t, 8, 8, rgbaOf(1, 2, 3)), nil }

	m.submit(job{guildID: "g2", imageURL: "u"})
	m.drainForTest(t, 1)
	if n := punished.Load(); n != 0 {
		t.Errorf("no refs configured — must not punish, got %d", n)
	}
}

func TestManagerDisabledGuildDropsJob(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	m.startWorker()
	defer m.stopWorker()

	var fetches atomic.Int32
	m.fetchFn = func(string) ([]byte, error) { fetches.Add(1); return nil, fmt.Errorf("no fetch should happen") }
	m.submit(job{guildID: "disabled-guild", imageURL: "u"})
	m.drainForTest(t, 1)
	if n := fetches.Load(); n != 0 {
		t.Errorf("disabled guild job must drop before fetch, got %d fetches", n)
	}
}

func TestManagerPanicRecovered(t *testing.T) {
	m, cfg, _, _ := newTestManager(t)
	m.startWorker()
	defer m.stopWorker()

	if err := m.SetGuildEnabled("g3", true); err != nil {
		t.Fatal(err)
	}
	set := defaultGuildConfig()
	set.Enabled = true
	set.Threshold = 0.5
	if err := cfg.SetGuildConfig("g3", set); err != nil {
		t.Fatal(err)
	}
	m.fetchFn = func(string) ([]byte, error) { panic("boom — a hostile image must not kill the bot") }
	m.submit(job{guildID: "g3", imageURL: "u"})
	m.drainForTest(t, 1)
	// Worker still alive: a second job processes fine.
	m.fetchFn = func(string) ([]byte, error) { return smallPNG(t, 8, 8, rgbaOf(9, 9, 9)), nil }
	m.submit(job{guildID: "g3", imageURL: "u2"})
	m.drainForTest(t, 2) // cumulative: the first job already bumped the counter
}

func TestManagerConcurrentEnableDisable(t *testing.T) {
	m, _, _, loads := newTestManager(t)
	m.startWorker()
	defer m.stopWorker()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			gid := fmt.Sprintf("guild-%d", n%4)
			_ = m.SetGuildEnabled(gid, n%2 == 0)
		}(i)
	}
	wg.Wait()
	// Invariant: refcount consistency — warm iff ≥1 guild enabled.
	if got := len(m.cfg.EnabledGuilds()); got > 0 != m.warm() {
		t.Errorf("inconsistent state: %d enabled, warm=%v", got, m.warm())
	}
	if n := loads.Load(); n > 4 {
		t.Errorf("loads = %d, want ≤4 (one per distinct enable transition)", n)
	}
}

// TestManagerEnableReportsLoadError is the B2 regression: a failed model load
// used to be logged and swallowed, so the dashboard's enable call reported
// success while the guild stayed cold with no visible reason. The error must
// reach the caller.
func TestManagerEnableReportsLoadError(t *testing.T) {
	if err := initRuntime(); err != nil {
		t.Skipf("onnxruntime not available: %v", err)
	}
	m, cfg, _, _ := newTestManager(t)
	m.startWorker()
	defer m.stopWorker()

	wantErr := errors.New("boom: model file missing")
	m.loadFn = func(dataDir, variant string) (embedder, error) { return nil, wantErr }

	err := m.SetGuildEnabled("g-load-fail", true)
	if err == nil {
		t.Fatal("SetGuildEnabled swallowed the model load error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want it to wrap %v", err, wantErr)
	}
	if m.warm() {
		t.Error("model must not be warm after a failed load")
	}
	// The persisted enable flag is NOT rolled back: it mirrors what the user
	// asked for, and the guild simply stays cold until a load succeeds.
	if !cfg.GuildSettings("g-load-fail").Enabled {
		t.Error("enable flag should stay persisted after a failed load")
	}

	// Recovery: once the loader works again, enabling another guild warms the
	// model even though refcount was already > 0 with a nil session.
	m.loadFn = func(dataDir, variant string) (embedder, error) { return &fakeEmbedder{inputSize: 224, dim: 8}, nil }
	if err := m.SetGuildEnabled("g-load-ok", true); err != nil {
		t.Fatalf("recovery enable: %v", err)
	}
	if !m.warm() {
		t.Error("model should be warm after a successful load following the failure")
	}
}

// TestManagerSubmitDuringWorkerRestart is the C3 race regression: submit must
// never send on a channel stopWorker just closed. Runs 8 hammering goroutines
// against 50 stop/start cycles — pre-fix, one of them panics with "send on
// closed channel" (killing the test binary) and -race reports the read/write
// race on m.jobs.
func TestManagerSubmitDuringWorkerRestart(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	m.startWorker()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				m.submit(job{guildID: "g-race", imageURL: "https://cdn.discordapp.com/x.png"})
			}
		}()
	}
	for i := 0; i < 50; i++ {
		m.stopWorker()
		m.startWorker()
	}
	close(stop)
	wg.Wait()
	m.stopWorker()
}

// drainForTest waits until at least `completed` jobs have run. `completed` is
// CUMULATIVE (the processed counter is monotonic), so a second drain after one
// job must pass 2 — passing 1 again would return immediately and silently.
// Deadline-bounded, so a stalled worker fails the test instead of hanging it.
func (m *manager) drainForTest(t *testing.T, completed int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for m.processed.Load() < int64(completed) {
		if time.Now().After(deadline) {
			t.Fatalf("worker stalled: processed %d, want %d", m.processed.Load(), completed)
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}
