package tickets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// provider.go — modules.TicketProvider implementation. The dashboard reaches
// the tickets module exclusively through this interface; CloseTicket is the
// SAME path the in-chat [p]close command uses.

// validGuildID reports whether s is a well-formed Discord snowflake.
// snowflake.Parse("null") returns (0, nil) by contract, so the zero check is
// required — a "null" guild id would otherwise reach the filesystem paths.
func validGuildID(s string) bool {
	id, err := snowflake.Parse(s)
	return err == nil && id != 0
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
	// Resolve closer display name BEFORE the mutation (REST call can block).
	closerName := byUserID
	if mem, ok := m.memberName(guildID, byUserID); ok {
		closerName = mem
	}
	// The close itself is a compare-and-set under the store lock: a second
	// caller (dashboard + in-chat, or two dashboard tabs) sees Status already
	// closed and returns without duplicating the close entry or the tail.
	tk, changed, err := m.store.mutate(guildID, ticketID, func(cur *modules.Ticket) bool {
		return m.markClosed(cur, byUserID, closerName, "user")
	})
	if err != nil {
		return fmt.Errorf("failed to persist close: %w", err)
	}
	if tk == nil {
		return fmt.Errorf("ticket %s not found", ticketID)
	}
	if !changed {
		return nil // idempotent — already closed
	}
	g, _ := m.typeOf(guildID, tk.EffectiveType())
	m.editClosedButtons(tk, g)
	// Close tail: lock channel → history merge → attachment mirror → HTML
	// transcript → log channel. Runs in a recovered goroutine (paging +
	// downloads can be slow; CloseTicket must return promptly).
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.ctx.Logger.Error("Tickets: panic in close tail for %s: %v", tk.ID, r)
			}
		}()
		m.finalizeTicket(tk, g, byUserID, "user", closeOptions{}, true)
	}()
	return nil
}

// ListTypes exposes configured types for dashboard editors.
func (m *TicketsModule) ListTypes(guildID string) ([]modules.TypeSummary, error) {
	if !m.isLoaded() {
		return nil, fmt.Errorf("tickets module is not loaded")
	}
	types := m.typesSnapshot(guildID)
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

// validMediaFilename accepts a bare filename (no path separators, no ..).
func validMediaFilename(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, "/\\") {
		return false
	}
	return !strings.Contains(name, "..")
}

// TicketFilePath resolves a mirrored media file to an absolute path, refusing
// anything that escapes the ticket's files dir.
func (m *TicketsModule) TicketFilePath(guildID, ticketID, name string) (string, error) {
	if !m.isLoaded() {
		return "", fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return "", fmt.Errorf("invalid guildID")
	}
	if !validTicketID(ticketID) {
		return "", fmt.Errorf("invalid ticketID")
	}
	if !validMediaFilename(name) {
		return "", fmt.Errorf("invalid media filename")
	}
	filesDir := filepath.Join(ticketsRoot(m.ctx.DataDir), guildID, ticketID, "files")
	p := filepath.Join(filesDir, name)
	rel, err := filepath.Rel(filesDir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid media path")
	}
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return "", fmt.Errorf("media file not found")
	}
	return p, nil
}

// ListPanels returns the guild's panels with their open-time question forms.
func (m *TicketsModule) ListPanels(guildID string) ([]modules.PanelSummary, error) {
	if !m.isLoaded() {
		return nil, fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return nil, fmt.Errorf("invalid guildID")
	}
	panels := m.panelsSnapshot(guildID)
	out := make([]modules.PanelSummary, 0, len(panels))
	for _, p := range panels {
		qs := make([]modules.PanelQuestion, 0, len(p.Questions))
		for _, q := range p.Questions {
			qs = append(qs, modules.PanelQuestion{
				Label: q.Label, Placeholder: q.Placeholder, Style: q.Style,
				Required: q.Required, Value: q.Value,
			})
		}
		out = append(out, modules.PanelSummary{
			Name: p.Name, TypeKey: p.TypeKey, ChannelID: p.ChannelID,
			Title: p.Title, Description: p.Description, ModalTitle: p.ModalTitle,
			Suspended: p.Suspended, Questions: qs,
		})
	}
	return out, nil
}

// SetPanelQuestions replaces a panel's open-time questions (empty = instant
// open). Validates; the error is shown verbatim in the browser.
func (m *TicketsModule) SetPanelQuestions(guildID, panel string, questions []modules.PanelQuestion) error {
	if !m.isLoaded() {
		return fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return fmt.Errorf("invalid guildID")
	}
	qs := make([]QuestionConfig, 0, len(questions))
	for _, q := range questions {
		qs = append(qs, QuestionConfig{
			Label: q.Label, Placeholder: q.Placeholder, Style: q.Style,
			Required: q.Required, Value: q.Value,
		})
	}
	if err := validateQuestions(qs); err != nil {
		return err
	}
	cfg := m.guildConfig(guildID)
	m.mu.Lock()
	p, ok := cfg.Panels[panel]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown panel %q", panel)
	}
	p.Questions = qs
	if len(qs) == 0 {
		p.ModalTitle = ""
	}
	cfg.Panels[panel] = p
	err := m.saveGuildLocked(guildID)
	m.mu.Unlock()
	return err
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
