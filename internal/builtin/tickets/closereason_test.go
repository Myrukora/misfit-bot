package tickets

import (
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"

	"github.com/misfit/bot/modules"
)

// TestTypeCloseReasonRoundTrip pins the dashboard <-> stored config path for the
// "ask for a reason" flag: it must survive create, update, and a list read, and
// it must default to OFF so existing types keep the immediate-close flow.
func TestTypeCloseReasonRoundTrip(t *testing.T) {
	m := adminModule(t, "")
	const gid = "111"

	// Default OFF on create.
	created, err := m.CreateType(gid, modules.TypeDetail{
		Key: "plain", Label: "Plain", Category: "222333444555666777", AllowClose: true,
	})
	if err != nil {
		t.Fatalf("CreateType: %v", err)
	}
	if created.CloseReason {
		t.Errorf("CloseReason = true on create, want default off")
	}
	if g, ok := m.typeOf(gid, "plain"); !ok || g.CloseReasonOn() {
		t.Errorf("stored type CloseReasonOn = %v, want off", g.CloseReasonOn())
	}

	// Explicit ON persists.
	turned, err := m.UpdateType(gid, "plain", modules.TypeDetail{
		Key: "plain", Label: "Plain", Category: "222333444555666777",
		AllowClose: true, CloseReason: true,
	})
	if err != nil {
		t.Fatalf("UpdateType: %v", err)
	}
	if !turned.CloseReason {
		t.Error("CloseReason = false after enabling, want true")
	}
	if g, _ := m.typeOf(gid, "plain"); !g.CloseReasonOn() {
		t.Error("stored type CloseReasonOn = false after enabling, want true")
	}

	// And it reads back through the list the dashboard renders.
	list, err := m.ListTypeDetails(gid)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTypeDetails = %+v, %v", list, err)
	}
	if !list[0].CloseReason {
		t.Error("ListTypeDetails lost the CloseReason flag")
	}
}

// TestTypeCloseReasonDefaultsOffForLegacyConfig pins the migration story: a
// stored type written BEFORE this field existed has no close_reason key, so it
// must decode as off rather than failing the load or defaulting on.
func TestTypeCloseReasonDefaultsOffForLegacyConfig(t *testing.T) {
	m := adminModule(t, "")
	const gid = "111"
	g := m.guildConfig(gid)

	legacy := &TypeConfig{Key: "old", Label: "Old", Enabled: true, AllowClose: boolPtr(true)}
	m.mu.Lock()
	g.Types["old"] = legacy
	m.mu.Unlock()

	d, err := m.GetType(gid, "old")
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	if d.CloseReason {
		t.Error("legacy type CloseReason = true, want off")
	}
	if !d.AllowClose {
		t.Error("legacy type AllowClose = false, want the stored true")
	}
}

// TestCloseLogEmbedShowsReasonNote pins that the typed reason replaces the
// "None" on the record's Reason line.
func TestCloseLogEmbedShowsReasonNote(t *testing.T) {
	g := TypeConfig{Key: "support", Label: "Support"}
	base := &modules.Ticket{
		ID: "222", GuildID: "111", OpenerID: "999",
		OpenedAt:    time.Now().UTC(),
		CloseReason: "user",
	}

	// No note: the line reads None instead of disappearing.
	e := closedLogEmbed(base, g, "777", discord.User{})
	if !strings.Contains(e.Description, "**Reason:** None") {
		t.Fatalf("desc missing the None reason: %q", e.Description)
	}

	// With a note it replaces None on the same line.
	withNote := *base
	withNote.CloseReasonNote = "resolved by explaining the rules"
	e = closedLogEmbed(&withNote, g, "777", discord.User{})
	if !strings.Contains(e.Description, "**Reason:** resolved by explaining the rules") {
		t.Fatalf("desc missing the note: %q", e.Description)
	}
}

// TestMarkClosedStoresNoteAndKeepsCause pins the deliberate split between
// CloseReason (the mechanical cause the code branches on) and CloseReasonNote
// (the human explanation). Conflating them would break the channel_deleted path.
func TestMarkClosedStoresNoteAndKeepsCause(t *testing.T) {
	m := adminModule(t, "")
	tk := &modules.Ticket{ID: "222", GuildID: "111", Status: "open", OpenerID: "999"}

	if !m.markClosed(tk, "42", "tester", "user", "duplicate ticket") {
		t.Fatal("markClosed returned false for an open ticket")
	}
	if tk.CloseReason != "user" {
		t.Errorf("CloseReason = %q, want \"user\"", tk.CloseReason)
	}
	if tk.CloseReasonNote != "duplicate ticket" {
		t.Errorf("CloseReasonNote = %q, want \"duplicate ticket\"", tk.CloseReasonNote)
	}

	// Idempotent: a second close changes nothing.
	if m.markClosed(tk, "42", "tester", "user", "second attempt") {
		t.Error("markClosed returned true for an already-closed ticket")
	}
	if tk.CloseReasonNote != "duplicate ticket" {
		t.Errorf("note overwritten on re-close: %q", tk.CloseReasonNote)
	}
}

// TestCloseModalCustomIDRoundTrip pins the modal custom_id contract: the ticket
// ID must survive the encode/decode, because a modal submit carries no link
// back to the message it came from.
func TestCloseModalCustomIDRoundTrip(t *testing.T) {
	const ticketID = "staff-0007"
	modal := buildCloseModal(ticketID)

	if !strings.HasPrefix(modal.CustomID, closeModalCustomIDPrefix) {
		t.Fatalf("custom_id = %q, want prefix %q", modal.CustomID, closeModalCustomIDPrefix)
	}
	if got := strings.TrimPrefix(modal.CustomID, closeModalCustomIDPrefix); got != ticketID {
		t.Fatalf("decoded ticket ID = %q, want %q", got, ticketID)
	}
	if modal.Title == "" {
		t.Error("modal title is empty")
	}
	if len(modal.Components) != 1 {
		t.Fatalf("modal has %d components, want 1", len(modal.Components))
	}
}

// TestTypeAsksCloseReason pins the gate the button and /close both consult.
func TestTypeAsksCloseReason(t *testing.T) {
	m := adminModule(t, "")
	const gid = "111"
	g := m.guildConfig(gid)

	m.mu.Lock()
	g.Types["prompted"] = &TypeConfig{Key: "prompted", Label: "P", CloseReason: boolPtr(true)}
	g.Types["silent"] = &TypeConfig{Key: "silent", Label: "S"}
	m.mu.Unlock()

	if !m.typeAsksCloseReason(gid, "prompted") {
		t.Error("prompted type = false, want true")
	}
	if m.typeAsksCloseReason(gid, "silent") {
		t.Error("silent type = true, want false")
	}
	// A vanished type must not prompt (fail closed to immediate close).
	if m.typeAsksCloseReason(gid, "gone") {
		t.Error("missing type = true, want false")
	}
}
