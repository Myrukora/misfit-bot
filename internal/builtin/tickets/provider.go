package tickets

import (
	"fmt"
	"sort"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// provider.go — modules.TicketProvider implementation. The dashboard reaches
// the tickets module exclusively through this interface; CloseTicket is the
// SAME path the in-chat [p]close command uses.

// validGuildID reports whether s is a well-formed Discord snowflake.
func validGuildID(s string) bool {
	_, err := snowflake.Parse(s)
	return err == nil && s != ""
}

// ListOpenTickets returns every open ticket in the guild, oldest first.
func (m *TicketsModule) ListOpenTickets(guildID string) ([]modules.TicketSummary, error) {
	if !m.isLoaded() {
		return nil, fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return nil, fmt.Errorf("invalid guildID")
	}
	return m.store.listOpen(guildID), nil
}

// ListClosedTickets returns closed tickets newest-first (archive UI).
func (m *TicketsModule) ListClosedTickets(guildID string) ([]modules.TicketSummary, error) {
	if !m.isLoaded() {
		return nil, fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return nil, fmt.Errorf("invalid guildID")
	}
	out, err := m.store.listClosed(guildID)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClosedAt.After(out[j].ClosedAt) })
	return out, nil
}

// GetTicket returns one ticket incl. its full log. (nil, nil) = not found.
func (m *TicketsModule) GetTicket(guildID, ticketID string) (*modules.Ticket, error) {
	if !m.isLoaded() {
		return nil, fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return nil, fmt.Errorf("invalid guildID")
	}
	if !validTicketID(ticketID) {
		return nil, fmt.Errorf("invalid ticket ID")
	}
	return m.store.load(guildID, ticketID)
}

// CloseTicket closes a ticket on behalf of byUserID. Idempotent.
func (m *TicketsModule) CloseTicket(guildID, ticketID, byUserID string) error {
	if !m.isLoaded() {
		return fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return fmt.Errorf("invalid guildID")
	}
	if !validTicketID(ticketID) {
		return fmt.Errorf("invalid ticket ID")
	}
	tk, err := m.store.load(guildID, ticketID)
	if err != nil {
		return err
	}
	if tk == nil {
		return fmt.Errorf("ticket %s not found", ticketID)
	}
	// Resolve closer display name BEFORE locks (REST call can block).
	closerName := byUserID
	if mem, ok := m.memberName(guildID, byUserID); ok {
		closerName = mem
	}
	m.mu.Lock()
	if tk.Status != "open" {
		m.mu.Unlock()
		return nil // idempotent
	}
	tk.Status = "closed"
	now := time.Now().UTC()
	tk.ClosedAt = now
	tk.Log = append(tk.Log, modules.LogEntry{
		MsgID: "system-close-" + tk.ID, AuthorID: byUserID,
		AuthorName: closerName, IsBot: true,
		Timestamp: now, Content: "_Ticket closed._",
	})
	m.mu.Unlock()

	g, _ := m.typeOf(tk.EffectiveType())
	m.editClosedButtons(tk, g)
	// v2 close tail: lock channel → full history merge → attachment mirror →
	// HTML transcript → log channel. This involves paging potentially thousands
	// of messages and downloading files, so it runs in a recovered goroutine —
	// CloseTicket must return promptly (interaction 3s deadline; dashboard HTTP).
	if err := m.store.save(tk); err != nil {
		return fmt.Errorf("failed to persist close: %w", err)
	}
	closerID := byUserID
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.ctx.Logger.Error("Tickets: panic in close tail for %s: %v", tk.ID, r)
			}
		}()
		m.closeWithTranscript(tk, g, closerID)
	}()
	m.postCloseSummary(tk, g, closerName)
	return nil
}

// ListTypes exposes configured types for dashboard editors.
func (m *TicketsModule) ListTypes(guildID string) ([]modules.TypeSummary, error) {
	if !m.isLoaded() {
		return nil, fmt.Errorf("tickets module is not loaded")
	}
	types := m.typesSnapshot()
	out := make([]modules.TypeSummary, len(types))
	for i, t := range types {
		out[i] = modules.TypeSummary{
			Key: t.Key, Label: t.Label, Enabled: t.Enabled,
			ButtonLabel: t.ButtonLabel, ButtonEmoji: t.ButtonEmoji,
			Color: int(t.Color),
		}
	}
	return out, nil
}

// postCloseSummary drops a compact summary into log_channel when configured.
func (m *TicketsModule) postCloseSummary(tk *modules.Ticket, g TypeConfig, closedBy string) {
	m.mu.RLock()
	logCh := ""
	if m.cfg != nil {
		logCh = m.cfg.LogChannel
	}
	m.mu.RUnlock()
	if logCh == "" {
		return
	}
	chID, err := snowflake.Parse(logCh)
	if err != nil {
		return
	}
	label := g.Label
	if label == "" {
		label = tk.EffectiveType()
	}
	desc := fmt.Sprintf("**%s** (`%s`) · opened <t:%d:R> by <@%s>%s\nClosed by **%s**",
		label, tk.ID, tk.OpenedAt.Unix(), tk.OpenerID, claimedSuffix(tk), closedBy)
	create := discord.MessageCreate{Embeds: []discord.Embed{embedInfo("Ticket closed", desc)}}
	if _, err := m.ctx.Rest.CreateMessage(chID, create); err != nil {
		m.ctx.Logger.Warn("Tickets: failed to post close summary: %v", err)
	}
}

func claimedSuffix(tk *modules.Ticket) string {
	if tk.ClaimerID != "" {
		return fmt.Sprintf(" · claimed by <@%s>", tk.ClaimerID)
	}
	return ""
}

// memberName resolves a display name via REST.
func (m *TicketsModule) memberName(guildID, userID string) (string, bool) {
	gid, e1 := snowflake.Parse(guildID)
	uid, e2 := snowflake.Parse(userID)
	if e1 != nil || e2 != nil {
		return "", false
	}
	mem, err := m.ctx.Rest.GetMember(gid, uid)
	if err != nil || mem == nil {
		return "", false
	}
	return mem.EffectiveName(), true
}
