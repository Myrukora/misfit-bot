package tickets

import (
	"sync"
	"time"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// TicketsModule is the plugin entry struct. All mutable state is guarded by
// mu: the store is touched from gateway event goroutines, component-interaction
// handlers and dashboard HTTP requests concurrently.
type TicketsModule struct {
	mu        sync.RWMutex
	ctx       *modules.Context
	module    *ModuleConfig
	guilds    map[string]*Config
	store     *store
	mirror    *mirrorQueue
	stopCh    chan struct{}
	stopOnce  *sync.Once // closes stopCh exactly once (reload-safe; re-created per load)
	botSelfID string     // cached GetSelfUserID for overwrites
	loaded    bool       // OnLoad done, OnUnload not yet run

	// Background-loop delays (testability seam; defaults unchanged).
	reconcileDelay      time.Duration // reconcileOpenTickets startup delay
	retentionFirstDelay time.Duration // retentionLoop first-run delay
	retentionInterval   time.Duration // retentionLoop ticker interval

	// channelGuild resolves a channel ID to its owning guild. A function
	// field so tests can stub it; nil falls back to resolveChannelGuild.
	channelGuild func(channelID string) (string, bool)
}

// interface type — the loader asserts the exact signature func() Module,
// and a concrete *TicketsModule return type fails the assertion at load
// time ("New() has wrong signature") even though it would satisfy the
// interface in normal Go type-checking.
func New() modules.Module {
	return &TicketsModule{guilds: map[string]*Config{}}
}

func (m *TicketsModule) Name() string    { return "tickets" }
func (m *TicketsModule) Version() string { return "3.0.0" }
func (m *TicketsModule) Description() string {
	return "Grouped ticket system: button panel, claim/close, transcripts in the dashboard"
}
func (m *TicketsModule) Author() string { return "misfit-bot" }
func (m *TicketsModule) Dependencies() []string {
	return []string{"dashboard"} // soft dep: dashboard renders transcripts if present
}

func (m *TicketsModule) Commands() []commands.Command {
	out := make([]commands.Command, 0, len(m.prefixCommands())+len(m.inChannelCommands()))
	out = append(out, m.prefixCommands()...)
	out = append(out, m.inChannelCommands()...)
	return out
}
func (m *TicketsModule) SlashCommands() []commands.SlashCommand { return m.slashCommands() }

// WebTabs declares the dashboard tabs this module contributes: the existing
// /tickets page (list + transcripts). Routes stay at /tickets — the sidebar
// just points at them (Task 10 keeps route churn minimal).
func (m *TicketsModule) WebTabs() []modules.WebTab {
	return []modules.WebTab{{Name: "Tickets", Slug: "/tickets"}}
}

// OnLoad stores context, migrates legacy config, loads module + store state
// and registers event hooks (button router, modal, conversation logging,
// channel-delete finalization).
func (m *TicketsModule) OnLoad(ctx *modules.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()

	// Migrate legacy single-file config to per-guild files (idempotent).
	if err := m.migrateToV3(ctx.DataDir); err != nil {
		return err
	}

	mod, err := loadModuleConfig(ctx.DataDir)
	if err != nil {
		return err
	}
	st, err := openStore(ctx.DataDir)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.module = mod
	m.store = st
	// Fresh stop channel + once per load: a reload re-arms the loops without
	// double-closing the previous (already-closed) channel.
	m.stopCh = make(chan struct{})
	m.stopOnce = new(sync.Once)
	m.mirror = newMirrorQueue(ctx.Logger)
	// Wire the stop channel into the mirror queue BEFORE any hook can
	// enqueue, so a post-unload event bails early instead of enqueuing.
	m.mirror.setStop(m.stopCh)
	// Background-loop delays (defaults unchanged; tests override).
	m.reconcileDelay = 15 * time.Second
	m.retentionFirstDelay = 60 * time.Second
	m.retentionInterval = 24 * time.Hour
	m.mu.Unlock()

	m.registerButtons()
	m.registerLogging()
	m.registerModal()
	m.registerChannels()

	// Cache self ID for overwrite computation ([p]add etc. need it too).
	m.mu.Lock()
	m.botSelfID = ctx.Bot.GetSelfUserID()
	m.loaded = true
	m.mu.Unlock()

	// Capture the stop channel once (parameter, not the field) so a reload's
	// new channel cannot resurrect a previous load's goroutines.
	stop := m.stopCh
	go m.mirror.run(ctx.DataDir, st, stop)
	go m.reconcileOpenTickets(stop)
	go m.retentionLoop(stop)

	ctx.Logger.Info("Tickets module loaded (v%d config)", mod.Version)
	return nil
}

// OnUnload flushes state so an unload/load cycle never loses tickets. The
// store reference is RETAINED (handlers and provider methods keep running
// until the manager drops the module — a nil store would panic them); the
// loaded flag gates new work instead. The stop channel is closed once so the
// mirror worker, reconcile sweep and retention loop exit.
func (m *TicketsModule) OnUnload() error {
	m.mu.Lock()
	m.loaded = false
	st := m.store
	// Close the stop channel exactly once (sync.Once): a second OnUnload
	// (disable after unload, or a reload cycle) must not double-close. The
	// channel is never nil'd — the loops captured it as a parameter.
	if m.stopOnce != nil {
		m.stopOnce.Do(func() { close(m.stopCh) })
	}
	m.mu.Unlock()
	if st != nil {
		return st.flushAll()
	}
	return nil
}

// OnDisable implements modules.Disablable: disabling tickets via
// [p]modules disable flushes state (same as unload) so no tickets are lost
// when the feature is turned off and later re-enabled.
func (m *TicketsModule) OnDisable() error {
	return m.OnUnload()
}

// isLoaded reports whether OnLoad completed and OnUnload has not run. Entry
// points that touch the store check this first.
func (m *TicketsModule) isLoaded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loaded && m.store != nil
}

// guildConfig returns the guild's config, loading it on first use. Never nil,
// never errors (a missing/invalid file yields an empty config).
func (m *TicketsModule) guildConfig(guildID string) *Config {
	m.mu.RLock()
	if cfg, ok := m.guilds[guildID]; ok {
		m.mu.RUnlock()
		return cfg
	}
	m.mu.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg, ok := m.guilds[guildID]; ok {
		return cfg
	}
	cfg := loadGuildConfig(m.ctx.DataDir, guildID, m.ctx.Logger)
	m.guilds[guildID] = cfg
	return cfg
}

// saveGuild persists the guild's config (locking wrapper).
func (m *TicketsModule) saveGuild(guildID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveGuildLocked(guildID)
}

// saveGuildLocked persists the guild's config; the caller holds m.mu.
func (m *TicketsModule) saveGuildLocked(guildID string) error {
	cfg := m.guilds[guildID]
	if cfg == nil {
		return nil
	}
	return cfg.save(m.ctx.DataDir, guildID)
}
