// Package main implements the tickets module: per-group ticket threads
// opened from an in-chat button panel, with full conversation logging and a
// dashboard-facing TicketProvider (see modules/tickets_contract.go).
package tickets

import (
	"sync"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// TicketsModule is the plugin entry struct. All mutable state is guarded by
// mu: the store is touched from gateway event goroutines, component-interaction
// handlers and dashboard HTTP requests concurrently.
type TicketsModule struct {
	mu          sync.RWMutex
	ctx         *modules.Context
	cfg         *Config
	store       *store
	botSelfID   string            // cached GetSelfUserID for overwrites
	loaded      bool              // OnLoad done, OnUnload not yet run
	panelMsgIDs map[string]string // legacy; superseded by cfg.Panels registry
}

// New is the plugin entry symbol. It MUST return the modules.Module
// interface type — the loader asserts the exact signature func() Module,
// and a concrete *TicketsModule return type fails the assertion at load
// time ("New() has wrong signature") even though it would satisfy the
// interface in normal Go type-checking.
func New() modules.Module {
	return &TicketsModule{panelMsgIDs: map[string]string{}}
}

func (m *TicketsModule) Name() string    { return "tickets" }
func (m *TicketsModule) Version() string { return "1.0.0" }
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
func (m *TicketsModule) SlashCommands() []commands.SlashCommand { return nil }

// WebTabs declares the dashboard tabs this module contributes: the existing
// /tickets page (list + transcripts). Routes stay at /tickets — the sidebar
// just points at them (Task 10 keeps route churn minimal).
func (m *TicketsModule) WebTabs() []modules.WebTab {
	return []modules.WebTab{{Name: "Tickets", Slug: "/tickets"}}
}

// OnLoad stores context, loads config + persisted state and registers event
// hooks (button router, conversation logging).
func (m *TicketsModule) OnLoad(ctx *modules.Context) error {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()

	cfg, err := loadConfig(ctx.DataDir)
	if err != nil {
		return err
	}
	st, err := openStore(ctx.DataDir)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.cfg = cfg
	m.store = st
	m.mu.Unlock()

	m.registerButtons()
	m.registerLogging()

	// Cache self ID for overwrite computation ([p]add etc. need it too).
	m.mu.Lock()
	m.botSelfID = ctx.Bot.GetSelfUserID()
	m.loaded = true
	m.mu.Unlock()

	ctx.Logger.Info("Tickets module loaded (v%d config: %d types, %d panels)",
		cfg.Version, len(cfg.Types), len(cfg.Panels))
	return nil
}

// OnUnload flushes state so an unload/load cycle never loses tickets. The
// store reference is RETAINED (handlers and provider methods keep running
// until the manager drops the module — a nil store would panic them); the
// loaded flag gates new work instead.
func (m *TicketsModule) OnUnload() error {
	m.mu.Lock()
	m.loaded = false
	st := m.store
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
