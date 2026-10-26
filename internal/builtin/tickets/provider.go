package tickets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

const (
	maxArchiveReason       = 1000
	maxArchiveMessageIDs   = 500
	maxArchiveAuditEvents  = 100
	archiveLogColorWarning = 0xFEE75C
)

// provider.go — modules.TicketProvider implementation. The dashboard reaches
// the tickets module exclusively through this interface; CloseTicket is the
// SAME path the in-chat /close command uses.

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

// DeleteArchivedTicket removes a CLOSED ticket from the archive. Only the
// dashboard calls this; the store re-checks the closed status under its lock.
func (m *TicketsModule) DeleteArchivedTicket(guildID, ticketID, byUserID string) error {
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
	if tk.Status != "closed" {
		return fmt.Errorf("ticket %s is not archived", ticketID)
	}
	func() {
		m.transcriptMu.Lock()
		defer m.transcriptMu.Unlock()
		err = m.store.deleteArchivedTicket(guildID, ticketID)
	}()
	if err != nil {
		return err
	}
	m.ctx.Logger.Info("Tickets: archived ticket %s/%s deleted by %s", guildID, ticketID, byUserID)
	m.postLogEvent(guildID, "Ticket archive: ticket deleted",
		fmt.Sprintf("**%s** (`%s`) was deleted from the ticket archive by %s.",
			tk.EffectiveType(), ticketID, archiveActorLabel(byUserID, "")),
		archiveLogColorWarning)
	return nil
}

// DeleteArchivedMessages removes selected messages from one CLOSED ticket.
// A non-empty reason is mandatory and is persisted with the actor and message
// IDs in TicketArchiveEvent — never the removed content or media. The removed
// IDs are also tombstoned so a late close-tail history merge cannot resurrect
// a message that was deleted while the archive was being finished.
func (m *TicketsModule) DeleteArchivedMessages(guildID, ticketID string, msgIDs []string, byUserID, byUserName, reason string) (int, error) {
	if !m.isLoaded() {
		return 0, fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return 0, fmt.Errorf("invalid guildID")
	}
	if !validTicketID(ticketID) {
		return 0, fmt.Errorf("invalid ticket ID")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return 0, fmt.Errorf("a reason is required")
	}
	if len([]rune(reason)) > maxArchiveReason {
		return 0, fmt.Errorf("reason is too long (max %d characters)", maxArchiveReason)
	}
	if len(msgIDs) == 0 {
		return 0, fmt.Errorf("no messages selected")
	}
	if len(msgIDs) > maxArchiveMessageIDs {
		return 0, fmt.Errorf("too many messages selected (max %d)", maxArchiveMessageIDs)
	}
	selected := make(map[string]bool, len(msgIDs))
	for _, id := range msgIDs {
		id = strings.TrimSpace(id)
		if !validArchiveMessageID(id) {
			return 0, fmt.Errorf("invalid message ID")
		}
		selected[id] = true
	}
	tk, err := m.store.load(guildID, ticketID)
	if err != nil {
		return 0, err
	}
	if tk == nil {
		return 0, fmt.Errorf("ticket %s not found", ticketID)
	}
	if tk.Status != "closed" {
		return 0, fmt.Errorf("only archived tickets can be edited")
	}
	matched := false
	for _, e := range tk.VisibleLog() {
		if selected[e.MsgID] {
			matched = true
			break
		}
	}
	if !matched {
		return 0, fmt.Errorf("no matching archived messages")
	}

	var removed []modules.LogEntry
	_, changed, err := m.store.mutate(guildID, ticketID, func(cur *modules.Ticket) bool {
		if cur.Status != "closed" {
			return false
		}
		visible := make(map[string]bool)
		for _, e := range cur.VisibleLog() {
			if selected[e.MsgID] {
				visible[e.MsgID] = true
			}
		}
		if len(visible) == 0 {
			return false
		}
		kept := make([]modules.LogEntry, 0, len(cur.Log))
		removed = removed[:0]
		for _, e := range cur.Log {
			if visible[e.MsgID] {
				removed = append(removed, e)
				continue
			}
			kept = append(kept, e)
		}
		if len(removed) == 0 {
			return false
		}
		cur.Log = kept

		already := make(map[string]bool, len(cur.RemovedMessages)+len(removed))
		for _, id := range cur.RemovedMessages {
			already[id] = true
		}
		removedIDs := make([]string, 0, len(removed))
		for _, e := range removed {
			removedIDs = append(removedIDs, e.MsgID)
			if !already[e.MsgID] {
				cur.RemovedMessages = append(cur.RemovedMessages, e.MsgID)
				already[e.MsgID] = true
			}
		}
		cur.ArchiveAudit = append(cur.ArchiveAudit, modules.TicketArchiveEvent{
			Time:       time.Now().UTC(),
			ActorID:    byUserID,
			ActorName:  byUserName,
			Action:     "messages_deleted",
			MessageIDs: removedIDs,
			Count:      len(removed),
			Reason:     reason,
		})
		if len(cur.ArchiveAudit) > maxArchiveAuditEvents {
			cur.ArchiveAudit = cur.ArchiveAudit[len(cur.ArchiveAudit)-maxArchiveAuditEvents:]
		}
		return true
	})
	if err != nil {
		return 0, fmt.Errorf("failed to persist archive deletion: %w", err)
	}
	if !changed {
		return 0, fmt.Errorf("no matching archived messages")
	}

	// Remove mirrored files that belonged only to the deleted entries. This
	// runs after the store mutation because the module contract forbids I/O
	// inside mutate (it holds the store lock). Keep paths still referenced by
	// any surviving entry.
	if fresh, ferr := m.store.load(guildID, ticketID); ferr == nil && fresh != nil {
		deleteRemovedMedia(m.ctx.DataDir, guildID, ticketID, removed, mediaPaths(fresh.Log), m.ctx.Logger)
	}
	// Drop the cached HTML BEFORE re-rendering: if rendering fails, the old
	// file must not survive with the deleted message content still inside it.
	// transcriptMu keeps a concurrent RefreshTranscript / close tail from
	// writing the pre-deletion log back in between.
	var rerr error
	func() {
		m.transcriptMu.Lock()
		defer m.transcriptMu.Unlock()
		htmlPath := filepath.Join(m.ctx.DataDir, "tickets", guildID, ticketID+".html")
		if rmErr := os.Remove(htmlPath); rmErr != nil && !os.IsNotExist(rmErr) {
			m.ctx.Logger.Warn("Tickets: remove stale transcript %s failed: %v", htmlPath, rmErr)
		}
		_, _, _, rerr = m.renderTranscriptToDiskLocked(guildID, ticketID)
	}()
	if rerr != nil {
		m.ctx.Logger.Warn("Tickets: transcript re-render after archive deletion failed for %s: %v", ticketID, rerr)
	}
	m.ctx.Logger.Info("Tickets: %d archived message(s) deleted from %s/%s by %s; reason: %s",
		len(removed), guildID, ticketID, byUserID, reason)
	m.postLogEvent(guildID, "Ticket archive: messages deleted",
		fmt.Sprintf("**%s** (`%s`) · %d message(s) deleted by %s\nReason: %s",
			tk.EffectiveType(), ticketID, len(removed), archiveActorLabel(byUserID, byUserName), reason),
		archiveLogColorWarning)
	return len(removed), nil
}

// archiveActorLabel renders the actor in a log embed without assuming the ID
// is a mentionable snowflake (tests and future callers may only have a name).
func archiveActorLabel(id, name string) string {
	if id != "" {
		return "<@" + id + ">"
	}
	if name != "" {
		return name
	}
	return "an unknown operator"
}

// validArchiveMessageID accepts the characters Discord message IDs and the
// module's synthetic IDs can contain. The dashboard message deletion route is
// the only caller; matching still requires an entry in VisibleLog.
func validArchiveMessageID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// mediaPaths collects every LocalPath referenced by a log slice. It is used
// to avoid deleting a file that a surviving entry still references.
func mediaPaths(log []modules.LogEntry) map[string]bool {
	out := make(map[string]bool)
	add := func(items []modules.Media) {
		for _, med := range items {
			if med.LocalPath != "" {
				out[med.LocalPath] = true
			}
		}
	}
	for _, e := range log {
		add(e.Attachments)
		add(e.Stickers)
		add(e.Embeds)
	}
	return out
}

// deleteRemovedMedia removes LocalPath files for deleted entries, but only
// when they live under this ticket's files directory and no surviving entry
// references them. All failures are best-effort WARNs: the ticket JSON is
// already authoritative and must not be rolled back for a stale file.
func deleteRemovedMedia(dataDir, guildID, ticketID string, removed []modules.LogEntry, keep map[string]bool, log modules.Logger) {
	filesDir := filepath.Join(dataDir, "tickets", guildID, ticketID, "files")
	seen := map[string]bool{}
	remove := func(items []modules.Media) {
		for _, med := range items {
			p := strings.TrimSpace(med.LocalPath)
			if p == "" || keep[p] || seen[p] {
				continue
			}
			seen[p] = true
			abs := filepath.Join(dataDir, filepath.FromSlash(p))
			rel, err := filepath.Rel(filesDir, abs)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				log.Warn("Tickets: remove archived media %s failed: %v", p, err)
			}
		}
	}
	for _, e := range removed {
		remove(e.Attachments)
		remove(e.Stickers)
		remove(e.Embeds)
	}

	// Mirror jobs name files "<message ID>-attN-…". A download that was in
	// flight while the message was deleted may not be referenced by the log
	// yet; remove those by prefix too, unless a surviving entry references
	// the same filename.
	removedIDs := make(map[string]bool, len(removed))
	for _, e := range removed {
		removedIDs[e.MsgID] = true
	}
	keptNames := make(map[string]bool, len(keep))
	for p := range keep {
		keptNames[filepath.Base(p)] = true
	}
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || keptNames[entry.Name()] {
			continue
		}
		for id := range removedIDs {
			if !strings.HasPrefix(entry.Name(), id+"-") {
				continue
			}
			if err := os.Remove(filepath.Join(filesDir, entry.Name())); err != nil && !os.IsNotExist(err) {
				log.Warn("Tickets: remove orphaned archived media %s failed: %v", entry.Name(), err)
			}
			break
		}
	}
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
		out = append(out, panelSummary(p))
	}
	return out, nil
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
