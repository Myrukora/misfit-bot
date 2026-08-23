// Package main implements the tickets module: per-group ticket threads
// opened from an in-chat button panel, with full conversation logging and a
// dashboard-facing TicketProvider (see modules/tickets_contract.go).
package main

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
	cfg         Config
	store       *store
	panelMsgIDs map[string]string // guildID -> control-panel message ID
}

func New() *TicketsModule {
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

func (m *TicketsModule) Commands() []commands.Command           { return m.prefixCommands() }
func (m *TicketsModule) SlashCommands() []commands.SlashCommand { return nil }

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

	ctx.Logger.Info("Tickets module loaded (%d groups configured)", len(cfg.parsed))
	return nil
}

// OnUnload flushes state so an unload/load cycle never loses tickets.
func (m *TicketsModule) OnUnload() error {
	m.mu.Lock()
	st := m.store
	m.store = nil
	m.mu.Unlock()
	if st != nil {
		return st.flushAll()
	}
	return nil
}
