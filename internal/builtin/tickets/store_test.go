package tickets

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

// modules_Ticket aliases the shared contract type for readability in tests.
type modules_Ticket = modules.Ticket

func mkTicket(id, guild, group string) *modules_Ticket {
	return &modules_Ticket{
		ID: id, Group: group, GuildID: guild,
		OpenerID: "42", Status: "open", OpenedAt: time.Now(),
		Log: []modules.LogEntry{{
			MsgID: "m1", AuthorID: "42", AuthorName: "tester",
			Timestamp: time.Now(), Content: "hello",
			Attachments: []modules.Media{{URL: "https://cdn.discordapp.com/a.png", Kind: "image", Filename: "a.png"}},
		}},
	}
}

func TestStoreSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	tk := mkTicket("staff-0001", "g1", "staff")
	if err := st.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := st.load("g1", "staff-0001")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || got.ID != "staff-0001" || len(got.Log) != 1 || got.Log[0].Attachments[0].Kind != "image" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStoreIndexRebuildFromScan(t *testing.T) {
	dir := t.TempDir()
	st, _ := openStore(dir)
	_ = st.save(mkTicket("staff-0001", "g1", "staff"))
	_ = st.save(func() *modules_Ticket { tk := mkTicket("apps-0002", "g1", "apps"); tk.Status = "closed"; return tk }())

	// Fresh store instance over the same dir must rebuild the index from
	// files. Delete index.json first to actually exercise the rebuild branch
	// (a prior save() already wrote it).
	if err := os.Remove(filepath.Join(dir, "tickets", "g1", "index.json")); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	st2, err := openStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	open := st2.openIDs("g1")
	if len(open) != 1 || open[0] != "staff-0001" {
		t.Fatalf("want only staff-0001 open, got %v", open)
	}
}

func TestStoreNextSeqZeroPadded(t *testing.T) {
	dir := t.TempDir()
	st, _ := openStore(dir)
	for i := 1; i <= 7; i++ {
		seq := st.nextSeq("g1", "staff")
		if seq != i {
			t.Fatalf("seq #%d: got %d", i, seq)
		}
		_ = st.save(mkTicket(fmt.Sprintf("staff-%04d", i), "g1", "staff"))
	}
	if got := st.nextSeq("g2", "staff"); got != 1 {
		t.Fatalf("per-guild seq reset expected, got %d", got)
	}
}

func TestStoreAtomicWriteNoTmpLeft(t *testing.T) {
	dir := t.TempDir()
	st, _ := openStore(dir)
	if err := st.save(mkTicket("staff-0003", "g9", "staff")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "tickets", "g9"))
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("tmp file left behind: %s", e.Name())
		}
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	st, _ := openStore(dir)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			tk := mkTicket(fmt.Sprintf("staff-%04d", n), "gc", "staff")
			_ = st.save(tk)
			_, _ = st.load("gc", tk.ID)
			_ = st.nextSeq("gc", "staff")
		}(i)
	}
	wg.Wait()
}

// TestValidTicketID pins the trust-boundary check on ticket IDs (they become
// file names under ticketsRoot/<guildID>/). Traversal and junk must be
// rejected; the real "<group>-<digits>" scheme must pass.
func TestValidTicketID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"staff-0007", true},
		{"room-applications-0042", true}, // group keys may contain '-'
		{"a-1", true},
		{"staff_2-0003", true}, // underscore allowed in group keys
		{"", false},
		{"staff-", false},
		{"-0007", false},
		{"0007", false},          // no group part
		{"staff", false},         // no seq
		{"../etc-passwd", false}, // traversal via slash
		{"..-0007", false},       // dot not in charset
		{"staff/../../x-1", false},
		{"staff-1/extra", false}, // separator inside
		{"staff-7x", false},      // non-digit seq
		{"staff-1 2", false},     // whitespace
		{"staff-0007 ", false},   // trailing space breaks round-trip
		{"/etc/passwd-1", false}, // absolute path shape
	}
	for _, c := range cases {
		if got := validTicketID(c.id); got != c.want {
			t.Errorf("validTicketID(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}
