package tickets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

const archiveGuild = "111222333444555666"

func archiveModule(t *testing.T) (*TicketsModule, string) {
	t.Helper()
	dataDir := t.TempDir()
	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{}, Rest: &mockRest{}},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{},
		loaded: true,
	}
	return m, dataDir
}

func archivedTicketFixture(t *testing.T) (*modules.Ticket, string, string) {
	t.Helper()
	now := time.Now().UTC()
	mediaRel := filepath.Join("tickets", archiveGuild, "support-1", "files", "m1-att0-secret.png")
	tk := &modules.Ticket{
		ID:        "support-1",
		Type:      "support",
		GuildID:   archiveGuild,
		MessageID: "welcome",
		OpenerID:  "42",
		OpenedAt:  now.Add(-time.Hour),
		ClosedAt:  now.Add(-time.Minute),
		Status:    "closed",
		Log: []modules.LogEntry{
			{MsgID: "welcome", AuthorID: "1", Content: "opening message", Timestamp: now.Add(-time.Hour)},
			{MsgID: "m1", AuthorID: "42", AuthorName: "user", Content: "private secret", Timestamp: now.Add(-50 * time.Minute),
				Attachments: []modules.Media{{URL: "https://cdn.discordapp.com/attachments/x/secret.png", LocalPath: mediaRel, Kind: "image", Filename: "secret.png"}}},
			{MsgID: "m2", AuthorID: "42", AuthorName: "user", Content: "keep this", Timestamp: now.Add(-40 * time.Minute)},
			{MsgID: "system-close-1", Content: "Ticket closed.", Timestamp: now.Add(-time.Minute)},
		},
	}
	return tk, mediaRel, filepath.Join("tickets", archiveGuild, "support-1", "support-1.html")
}

func TestDeleteArchivedMessagesRemovesContentAndLogsReason(t *testing.T) {
	m, dataDir := archiveModule(t)
	tk, mediaRel, _ := archivedTicketFixture(t)
	if err := m.store.save(tk); err != nil {
		t.Fatalf("save ticket: %v", err)
	}
	mediaAbs := filepath.Join(dataDir, mediaRel)
	if err := os.MkdirAll(filepath.Dir(mediaAbs), 0o700); err != nil {
		t.Fatalf("mkdir media: %v", err)
	}
	if err := os.WriteFile(mediaAbs, []byte("secret bytes"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	// A mirror download that was in flight when the message was deleted is
	// not referenced by LocalPath yet; prefix cleanup must still remove it.
	orphanAbs := filepath.Join(filepath.Dir(mediaAbs), "m1-att0-orphan.png")
	if err := os.WriteFile(orphanAbs, []byte("orphan bytes"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}

	n, err := m.DeleteArchivedMessages(archiveGuild, tk.ID, []string{"m1"}, "7", "moderator", "privacy request")
	if err != nil {
		t.Fatalf("DeleteArchivedMessages: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted = %d, want 1", n)
	}

	updated, err := m.store.load(archiveGuild, tk.ID)
	if err != nil || updated == nil {
		t.Fatalf("load after delete: %v / %v", updated, err)
	}
	for _, e := range updated.Log {
		if e.MsgID == "m1" {
			t.Fatalf("deleted message still present: %+v", e)
		}
		if strings.Contains(e.Content, "private secret") {
			t.Fatalf("deleted content still stored: %q", e.Content)
		}
	}
	if len(updated.RemovedMessages) != 1 || updated.RemovedMessages[0] != "m1" {
		t.Fatalf("tombstones = %v, want [m1]", updated.RemovedMessages)
	}
	if len(updated.ArchiveAudit) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(updated.ArchiveAudit))
	}
	ev := updated.ArchiveAudit[0]
	if ev.Reason != "privacy request" || ev.ActorID != "7" || ev.ActorName != "moderator" || ev.Count != 1 {
		t.Fatalf("unexpected audit event: %+v", ev)
	}
	if len(ev.MessageIDs) != 1 || ev.MessageIDs[0] != "m1" {
		t.Fatalf("audit message IDs = %v, want [m1]", ev.MessageIDs)
	}
	if _, err := os.Stat(mediaAbs); !os.IsNotExist(err) {
		t.Fatalf("mirrored media still exists (err=%v)", err)
	}
	if _, err := os.Stat(orphanAbs); !os.IsNotExist(err) {
		t.Fatalf("orphaned in-flight media still exists (err=%v)", err)
	}
	htmlAbs := filepath.Join(dataDir, "tickets", archiveGuild, tk.ID+".html")
	html, err := os.ReadFile(htmlAbs)
	if err != nil {
		t.Fatalf("read regenerated transcript: %v", err)
	}
	if strings.Contains(string(html), "private secret") {
		t.Fatal("deleted content appears in the regenerated transcript")
	}
	if !strings.Contains(string(html), "privacy request") {
		t.Fatal("audit reason missing from the regenerated transcript")
	}
}

func TestDeleteArchivedMessagesRequiresReasonAndClosedTicket(t *testing.T) {
	m, _ := archiveModule(t)
	tk, _, _ := archivedTicketFixture(t)
	if err := m.store.save(tk); err != nil {
		t.Fatalf("save ticket: %v", err)
	}
	if _, err := m.DeleteArchivedMessages(archiveGuild, tk.ID, []string{"m1"}, "7", "moderator", "   "); err == nil {
		t.Fatal("empty reason should be rejected")
	}
	if stored, _ := m.store.load(archiveGuild, tk.ID); stored == nil || len(stored.RemovedMessages) != 0 {
		t.Fatalf("rejected deletion changed stored tombstones: %+v", stored)
	}

	open := *tk
	open.ID = "support-2"
	open.Status = "open"
	open.ClosedAt = time.Time{}
	open.Log = []modules.LogEntry{{MsgID: "m1", Content: "still live"}}
	if err := m.store.save(&open); err != nil {
		t.Fatalf("save open ticket: %v", err)
	}
	if _, err := m.DeleteArchivedMessages(archiveGuild, open.ID, []string{"m1"}, "7", "moderator", "cleanup"); err == nil {
		t.Fatal("open ticket should not be editable as an archive")
	}
}

func TestDeleteArchivedTicketRemovesFiles(t *testing.T) {
	m, dataDir := archiveModule(t)
	tk, mediaRel, _ := archivedTicketFixture(t)
	if err := m.store.save(tk); err != nil {
		t.Fatalf("save ticket: %v", err)
	}
	mediaAbs := filepath.Join(dataDir, mediaRel)
	if err := os.MkdirAll(filepath.Dir(mediaAbs), 0o700); err != nil {
		t.Fatalf("mkdir media: %v", err)
	}
	if err := os.WriteFile(mediaAbs, []byte("bytes"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	htmlAbs := filepath.Join(dataDir, "tickets", archiveGuild, tk.ID+".html")
	if err := os.WriteFile(htmlAbs, []byte("<html>cache</html>"), 0o600); err != nil {
		t.Fatalf("write html: %v", err)
	}

	if err := m.DeleteArchivedTicket(archiveGuild, tk.ID, "7"); err != nil {
		t.Fatalf("DeleteArchivedTicket: %v", err)
	}
	if got, _ := m.store.load(archiveGuild, tk.ID); got != nil {
		t.Fatalf("ticket still loads after deletion: %+v", got)
	}
	for _, path := range []string{
		filepath.Join(dataDir, "tickets", archiveGuild, tk.ID+".json"),
		htmlAbs,
		filepath.Dir(filepath.Dir(mediaAbs)),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after ticket deletion (err=%v)", path, err)
		}
	}
}

func TestMergeEntriesSkipsRemovedTombstones(t *testing.T) {
	tk := &modules.Ticket{
		Log:             []modules.LogEntry{{MsgID: "m2"}},
		RemovedMessages: []string{"m1"},
	}
	if mergeEntriesInto(tk, []modules.LogEntry{{MsgID: "m1"}, {MsgID: "m3"}}) == false {
		t.Fatal("expected m3 to be merged")
	}
	for _, e := range tk.Log {
		if e.MsgID == "m1" {
			t.Fatal("merge resurrected a removed message")
		}
	}
	if len(tk.Log) != 2 {
		t.Fatalf("log = %+v, want m2 and m3", tk.Log)
	}
}
