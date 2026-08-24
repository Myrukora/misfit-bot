package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// ── modules.TicketProvider implementation ─────────────────────────────────
// The dashboard reaches the tickets module exclusively through this
// interface; CloseTicket is the SAME path the in-chat Close button uses.

// validGuildID reports whether s is a well-formed Discord snowflake. Every
// provider entry point validates IDs explicitly so path building
// (filepath.Join(ticketsRoot, guildID, ...)) never depends on router shape:
// a non-snowflake guildID cannot traverse, and ticketIDs are checked against
// the strict "<group>-<digits>" scheme in the store.
func validGuildID(s string) bool {
	_, err := snowflake.Parse(s)
	return err == nil && strings.TrimSpace(s) != ""
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

// CloseTicket closes a ticket on behalf of byUserID: marks it closed, greys
// the panel buttons, archives the thread, appends a closure log entry and
// updates the index. Idempotent for already-closed tickets (returns nil).
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
	// Resolve the closer's display name BEFORE taking any lock — this makes a
	// Discord REST call that can block for seconds under rate limiting.
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
	tk.ClosedAt = time.Now().UTC()
	tk.Log = append(tk.Log, modules.LogEntry{
		MsgID: "system-close-" + tk.ID, AuthorID: byUserID,
		AuthorName: closerName, IsBot: true,
		Timestamp: tk.ClosedAt, Content: "_Ticket closed._",
	})
	m.mu.Unlock()

	g, _ := m.group(tk.Group)
	m.editClosedButtons(tk, g)
	m.archiveThread(tk)
	if err := m.store.save(tk); err != nil {
		return fmt.Errorf("failed to persist close: %w", err)
	}
	m.postCloseSummary(tk, g, closerName)
	return nil
}

// ListGroups exposes configured groups (dashboard filter UI).
func (m *TicketsModule) ListGroups(guildID string) ([]modules.GroupSummary, error) {
	groups := m.groupsSnapshot()
	out := make([]modules.GroupSummary, len(groups))
	for i, g := range groups {
		out[i] = modules.GroupSummary{Key: g.Key, Label: g.Label, Enabled: g.Enabled}
	}
	return out, nil
}

// archiveThread archives+locks the ticket thread so it stays readable but
// frozen. Best-effort: failures are logged, never fatal to closing.
func (m *TicketsModule) archiveThread(tk *modules.Ticket) {
	cid, err := snowflake.Parse(tk.ChannelID)
	if err != nil {
		return
	}
	archived := true
	locked := true
	update := discord.GuildThreadUpdate{Archived: &archived, Locked: &locked}
	if _, err := m.ctx.Rest.UpdateChannel(cid, update); err != nil {
		m.ctx.Logger.Warn("Tickets: failed to archive thread %s: %v", tk.ChannelID, err)
	}
}

// postCloseSummary drops a compact summary into log_channel when configured.
func (m *TicketsModule) postCloseSummary(tk *modules.Ticket, g GroupConfig, closedBy string) {
	m.mu.RLock()
	gcfg, ok := m.cfg.Guilds[tk.GuildID]
	m.mu.RUnlock()
	if !ok || gcfg.LogChannel == "" {
		return
	}
	chID, err := snowflake.Parse(gcfg.LogChannel)
	if err != nil {
		return
	}
	msgCount := len(tk.Log)
	desc := fmt.Sprintf("**%s** (`%s`) · opened <t:%d:R> by <@%s> · %d messages\nClosed by **%s**",
		g.Label, tk.ID, tk.OpenedAt.Unix(), tk.OpenerID, msgCount, closedBy)
	create := discord.MessageCreate{
		Embeds: []discord.Embed{embedInfo("Ticket closed", desc)},
	}
	if _, err := m.ctx.Rest.CreateMessage(chID, create); err != nil {
		m.ctx.Logger.Warn("Tickets: failed to post close summary: %v", err)
	}
}

// memberName resolves a display name via REST cache fallback chain.
func (m *TicketsModule) memberName(guildID, userID string) (string, bool) {
	gid, err1 := snowflake.Parse(guildID)
	uid, err2 := snowflake.Parse(userID)
	if err1 != nil || err2 != nil {
		return "", false
	}
	mem, err := m.ctx.Rest.GetMember(gid, uid)
	if err != nil || mem == nil {
		return "", false
	}
	return mem.EffectiveName(), true
}
