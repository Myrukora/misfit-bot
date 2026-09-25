package imagefilter

import (
	"sync"
	"sync/atomic"

	"github.com/misfit/bot/modules"
)

// embedder is the session surface the manager needs (satisfied by clipSession;
// faked in tests so lifecycle logic is verifiable without the real model).
type embedder interface {
	Embed(tensor []float32) ([]float32, error)
	InputSize() int
	Close() error
}

// refEmbed is one cached reference-image embedding.
type refEmbed struct {
	name string
	vec  []float32
}

// job is one pending detection task (single image URL).
type job struct {
	guildID   string
	channelID string
	messageID string
	authorID  string
	imageURL  string
}

// manager owns the model lifecycle (warm/cold refcount), per-guild reference
// embeddings, and the serialized detection worker. All transitions come from
// dashboard actions (enable/disable, image add/remove, variant switch) and
// gateway events (message create), so everything is mutex-guarded.
type manager struct {
	mu      sync.Mutex
	cfg     *config
	imgs    *images
	dataDir string
	log     modules.Logger

	// Injection seams (production defaults set in newManager; tests swap them).
	loadFn   func(dataDir, variant string) (embedder, error) // set in newManager
	fetchFn  func(url string) ([]byte, error)                // set in A8 (SSRF rules)
	punishFn func(j job, score float64, cfg GuildConfig)     // set in A8 (REST actions)

	// Model lifecycle. sess != nil ⇒ warm.
	sess        embedder
	sessVariant string
	refcount    int // guilds with enabled=true

	// Per-guild reference embeddings (variant-scoped: a variant switch wipes all).
	refsMu     sync.RWMutex
	refs       map[string][]refEmbed
	refsVector string

	// Serialized worker: one goroutine consumes jobs; all heavy work (fetch,
	// embed, punish) happens there, never on gateway/dashboard goroutines.
	jobs      chan job
	workerWg  sync.WaitGroup
	workerRun bool
	processed atomic.Int64 // completed jobs (monotonic; test-drain + status)
}

// workerQueueSize bounds pending detections; over that, drop + warn (the bot
// must not balloon RAM when a spam flood hits a cold model).
const workerQueueSize = 256

func newManager(cfg *config, imgs *images, dataDir string, log modules.Logger) *manager {
	m := &manager{
		cfg:     cfg,
		imgs:    imgs,
		dataDir: dataDir,
		log:     log,
		refs:    map[string][]refEmbed{},
		jobs:    make(chan job, workerQueueSize),
	}
	// loadClip returns *clipSession which satisfies embedder; wrap for the seam.
	m.loadFn = func(dataDir, variant string) (embedder, error) {
		return loadClip(dataDir, variant)
	}
	return m
}

// startWorker launches the single consumer goroutine (idempotent).
func (m *manager) startWorker() {
	m.mu.Lock()
	if m.workerRun {
		m.mu.Unlock()
		return
	}
	m.workerRun = true
	m.mu.Unlock()
	m.workerWg.Add(1)
	go m.workerLoop()
}

// stopWorker halts the consumer and waits for the in-flight job to finish.
func (m *manager) stopWorker() {
	m.mu.Lock()
	if !m.workerRun {
		m.mu.Unlock()
		return
	}
	m.workerRun = false
	close(m.jobs)
	m.mu.Unlock()
	m.workerWg.Wait()
	// Recreate the channel so a subsequent startWorker works on a fresh queue.
	m.mu.Lock()
	m.jobs = make(chan job, workerQueueSize)
	m.mu.Unlock()
}

// workerLoop serializes every detection. Panics are recovered per job — the
// dashboard shares this process, a panic here must never take the bot down.
func (m *manager) workerLoop() {
	defer m.workerWg.Done()
	for j := range m.jobs {
		func() {
			defer func() {
				m.processed.Add(1)
				if r := recover(); r != nil {
					m.log.Error("imagefilter: panic processing job (guild %s): %v", j.guildID, r)
				}
			}()
			m.handleJob(j)
		}()
	}
}

// submit enqueues a detection without ever blocking the gateway goroutine.
// Returns the job's sequence number (processed counter watermark) so callers
// can wait for completion in tests.
func (m *manager) submit(j job) bool {
	m.mu.Lock()
	ch := m.jobs
	m.mu.Unlock()
	select {
	case ch <- j:
		return true
	default:
		m.log.Warn("imagefilter: queue full, dropping detection for guild %s", j.guildID)
		return false
	}
}

// SyncEnabled loads state at startup: count enabled guilds, warm the model if
// any, and start the worker. Never fails the module load — a missing lib or
// model file just leaves the filter cold and dashboard-visible.
func (m *manager) SyncEnabled() {
	m.startWorker()
	enabled := m.cfg.EnabledGuilds()
	variant := m.cfg.ClipVariant()

	m.mu.Lock()
	m.refcount = len(enabled)
	m.sessVariant = variant
	if m.refcount > 0 {
		if err := initRuntime(); err != nil {
			m.log.Error("imagefilter: onnxruntime unavailable (%v) — filter stays cold", err)
			m.mu.Unlock()
			return
		}
		sess, err := m.loadFn(m.dataDir, variant)
		if err != nil {
			m.log.Error("imagefilter: model load failed (%v) — filter stays cold", err)
			m.mu.Unlock()
			return
		}
		m.sess = sess
	}
	m.mu.Unlock()

	if m.refcount > 0 && m.sess != nil {
		m.log.Info("imagefilter: model warm (variant %s, %d enabled server(s))", variant, m.refcount)
	}
}

// SetGuildEnabled applies an enable/disable from the dashboard and drives the
// warm/cold refcount. Returns after the model state transition completes.
func (m *manager) SetGuildEnabled(guildID string, enabled bool) error {
	changed, err := m.cfg.SetGuildEnabled(guildID, enabled)
	if err != nil || !changed {
		return err
	}
	m.adjust(enabled)
	return nil
}

// adjust moves the refcount and loads/unloads the model at the boundaries.
func (m *manager) adjust(up bool) {
	m.mu.Lock()
	if up {
		m.refcount++
	} else if m.refcount > 0 {
		m.refcount--
	}
	need := m.refcount > 0
	var loadErr error
	switch {
	case need && m.sess == nil:
		if err := initRuntime(); err != nil {
			m.log.Error("imagefilter: onnxruntime unavailable (%v)", err)
			loadErr = err
			break
		}
		sess, err := m.loadFn(m.dataDir, m.sessVariant)
		if err != nil {
			m.log.Error("imagefilter: model load failed: %v", err)
			loadErr = err
			break
		}
		m.sess = sess
		m.log.Info("imagefilter: model loaded (variant %s)", m.sessVariant)
	case !need && m.sess != nil:
		sess := m.sess
		m.sess = nil
		m.mu.Unlock()
		if err := sess.Close(); err != nil {
			m.log.Error("imagefilter: model close failed: %v", err)
		}
		// Free the per-guild ref embeddings too — the vector space dies with
		// the session.
		m.refsMu.Lock()
		m.refs = map[string][]refEmbed{}
		m.refsMu.Unlock()
		m.log.Info("imagefilter: model unloaded (no enabled servers) — RAM freed")
		return
	}
	m.mu.Unlock()
	_ = loadErr
}

// SetClipVariant switches the bot-wide model: close the warm session, wipe
// cached embeddings (different vector space), reload if any guild is enabled.
func (m *manager) SetClipVariant(variant string) error {
	invalidated, err := m.cfg.SetClipVariant(variant)
	if err != nil || !invalidated {
		return err
	}

	m.mu.Lock()
	old := m.sess
	m.sess = nil
	m.mu.Unlock()

	if old != nil {
		if err := old.Close(); err != nil {
			m.log.Error("imagefilter: old session close failed: %v", err)
		}
	}

	m.refsMu.Lock()
	m.refs = map[string][]refEmbed{}
	m.refsMu.Unlock()

	// Re-warm when something is enabled (reload with the new variant).
	m.mu.Lock()
	need := m.refcount > 0
	m.sessVariant = variant // sessions built from now on use the new variant
	if need {
		if err := initRuntime(); err != nil {
			m.mu.Unlock()
			return err
		}
		sess, err := m.loadFn(m.dataDir, variant)
		if err != nil {
			m.refcount = 0 // stay cold; dashboard shows the error
			m.mu.Unlock()
			return err
		}
		m.sess = sess
		m.log.Info("imagefilter: switched model to variant %s", variant)
	}
	m.mu.Unlock()
	return nil
}

// Variant returns the currently configured variant (config value, even when cold).
func (m *manager) Variant() string {
	return m.cfg.ClipVariant()
}

// InvalidateRefs drops a guild's cached embeddings (after image add/remove).
func (m *manager) InvalidateRefs(guildID string) {
	m.refsMu.Lock()
	delete(m.refs, guildID)
	m.refsMu.Unlock()
}

// warm reports whether the model is currently loaded.
func (m *manager) warm() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sess != nil
}

// unloadModel force-closes the session (shutdown path). Refcount resets so a
// later SyncEnabled/re-enable reloads cleanly.
func (m *manager) unloadModel() {
	m.mu.Lock()
	sess := m.sess
	m.sess = nil
	m.refcount = 0
	m.mu.Unlock()
	if sess != nil {
		if err := sess.Close(); err != nil {
			m.log.Error("imagefilter: model close failed: %v", err)
		}
	}
	m.refsMu.Lock()
	m.refs = map[string][]refEmbed{}
	m.refsMu.Unlock()
}

// guildRefs returns cached ref embeddings for a guild, embedding the images
// on first need (called from the worker only — serialized).
func (m *manager) guildRefs(guildID string) []refEmbed {
	m.refsMu.RLock()
	refs, ok := m.refs[guildID]
	variant := m.refsVector
	m.refsMu.RUnlock()
	if ok && variant == m.sessVariantString() {
		return refs
	}

	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()
	if sess == nil {
		return nil
	}

	names, err := m.imgs.List(guildID)
	if err != nil {
		m.log.Error("imagefilter: list refs for guild %s: %v", guildID, err)
		return nil
	}
	built := make([]refEmbed, 0, len(names))
	size := sess.InputSize()
	for _, name := range names {
		data, err := m.imgs.Read(guildID, name)
		if err != nil {
			m.log.Error("imagefilter: read ref %s/%s: %v", guildID, name, err)
			continue
		}
		tensor, err := Preprocess(data, size)
		if err != nil {
			m.log.Error("imagefilter: preprocess ref %s/%s: %v", guildID, name, err)
			continue
		}
		vec, err := sess.Embed(tensor)
		if err != nil {
			m.log.Error("imagefilter: embed ref %s/%s: %v", guildID, name, err)
			continue
		}
		built = append(built, refEmbed{name: name, vec: vec})
	}

	m.refsMu.Lock()
	m.refs[guildID] = built
	m.refsVector = m.sessVariantString()
	m.refsMu.Unlock()
	return built
}

// sessVariantString returns the loaded session's variant ("" when cold).
func (m *manager) sessVariantString() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessVariant
}

// handleJob: fetch → preprocess → embed → max-cosine vs guild refs → punish.
// Runs on the worker goroutine.
func (m *manager) handleJob(j job) {
	settings := m.cfg.GuildSettings(j.guildID)
	if !settings.Enabled {
		return
	}
	m.mu.Lock()
	sess := m.sess
	m.mu.Unlock()
	if sess == nil {
		return
	}
	if m.fetchFn == nil {
		return
	}
	data, err := m.fetchFn(j.imageURL)
	if err != nil {
		m.log.Error("imagefilter: fetch %s: %v", j.imageURL, err)
		return
	}
	tensor, err := Preprocess(data, sess.InputSize())
	if err != nil {
		m.log.Error("imagefilter: preprocess incoming: %v", err)
		return
	}
	vec, err := sess.Embed(tensor)
	if err != nil {
		m.log.Error("imagefilter: embed incoming: %v", err)
		return
	}
	refs := m.guildRefs(j.guildID)
	if len(refs) == 0 {
		return // no blacklist configured → nothing can match
	}
	best := float32(0)
	for _, r := range refs {
		if c := Cosine(vec, r.vec); c > best {
			best = c
		}
	}
	if float64(best) <= settings.Threshold {
		return
	}
	m.log.Info("imagefilter: hit in guild %s (score %.4f > %.2f, author %s)",
		j.guildID, best, settings.Threshold, j.authorID)
	if m.punishFn != nil {
		m.punishFn(j, float64(best), settings)
	}
}
