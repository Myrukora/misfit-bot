// Package imagefilter implements the image spam filter as a compiled-in
// builtin: images posted in a guild are embedded with a CLIP vision tower
// (ONNX Runtime, CPU) and compared by cosine similarity against that guild's
// blacklisted reference images. All configuration lives on the dashboard —
// the module registers no Discord commands.
//
// The CLIP session is refcounted across guilds: enabling the feature on the
// first guild loads the model and keeps it warm; disabling it everywhere
// closes the session and frees the RAM (see manager.go).
package imagefilter

import (
	"sync"

	corecfg "github.com/misfit/bot/config"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// ImageFilterModule is the builtin entry struct. Mutable state is guarded by
// mu: gateway event goroutines and dashboard HTTP requests hit it
// concurrently.
type ImageFilterModule struct {
	mu  sync.RWMutex
	ctx *modules.Context

	mgr  *manager
	cfg  *config
	imgs *images
	ok   bool // OnLoad completed

	prov provisionState // lazy runtime setup (provision.go)
}

// New is the builtin entry symbol. It MUST return the modules.Module
// interface type — the loader asserts the exact signature func() Module.
func New() modules.Module {
	return &ImageFilterModule{}
}

func (m *ImageFilterModule) Name() string    { return "imagefilter" }
func (m *ImageFilterModule) Version() string { return "1.0.0" }
func (m *ImageFilterModule) Description() string {
	return "Image spam filter: CLIP (ONNX, CPU) similarity against per-server blacklisted images"
}
func (m *ImageFilterModule) Author() string         { return "misfit-bot" }
func (m *ImageFilterModule) Dependencies() []string { return nil }

// Commands is intentionally nil: this module is dashboard-only by design.
func (m *ImageFilterModule) Commands() []commands.Command { return nil }

func (m *ImageFilterModule) SlashCommands() []commands.SlashCommand { return nil }

// OnLoad stores the module context, loads config + reference images, wires the
// manager and registers the message detector. It must not fail when the ONNX
// runtime lib or model files are absent — the filter then stays cold and
// reports why on the dashboard instead of blocking the whole builtin
// registration.
func (m *ImageFilterModule) OnLoad(ctx *modules.Context) error {
	core, _ := ctx.CoreConfig.(*corecfg.Config)
	var (
		cfg *config
		err error
	)
	if core != nil {
		cfg, err = loadCoreConfig(core, ctx.DataDir)
	} else {
		cfg, err = loadConfig(ctx.DataDir)
	}
	if err != nil {
		return err
	}
	imgs := newImages(ctx.DataDir)
	mgr := newManager(cfg, imgs, ctx.DataDir, ctx.Logger)
	// Production seams: real fetch; punishments wired to REST (punishment.go).
	mgr.fetchFn = fetchImage
	mgr.punishFn = m.executePunishment

	mgr.SyncEnabled()

	m.mu.Lock()
	m.ctx = ctx
	m.cfg = cfg
	m.imgs = imgs
	m.mgr = mgr
	m.ok = true
	m.mu.Unlock()

	m.registerDetector()

	ctx.Logger.Info("Image filter module loaded (dashboard-configured; %d server(s) enabled)", len(cfg.EnabledGuilds()))

	// A deployment that had the filter enabled before its artifacts were
	// installed heals itself: the setup runs in the background and warms the
	// model when it finishes (no-op when nothing is missing).
	if len(cfg.EnabledGuilds()) > 0 && artifactsMissing(mgr.dataDir, cfg.ClipVariant()) != "" {
		m.startProvisioning()
	}
	return nil
}

// OnUnload stops the worker and closes the CLIP session if warm.
func (m *ImageFilterModule) OnUnload() error {
	m.mu.Lock()
	mgr := m.mgr
	m.ok = false
	m.mu.Unlock()
	if mgr != nil {
		mgr.stopWorker()
		mgr.unloadModel()
	}
	return nil
}

// isLoaded gates entry points after unload.
func (m *ImageFilterModule) isLoaded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ok
}
