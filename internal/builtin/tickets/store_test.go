package tickets

import (
	"encoding/json"
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

// TestStoreAtomicWriteNoTmpLeft pins the atomic tmp+rename contract for store
// writes: save() and the index rebuild (openIDsLocked) must both leave no
// .tmp file behind and must produce valid JSON.
func TestStoreAtomicWriteNoTmpLeft(t *testing.T) {
	dir := t.TempDir()
	st, _ := openStore(dir)
	gdir := filepath.Join(dir, "tickets", "g9")
	if err := st.save(mkTicket("staff-0003", "g9", "staff")); err != nil {
		t.Fatal(err)
	}
	assertNoTmpFiles(t, gdir)

	// Force the index REBUILD branch (the one that writes index.json) by
	// deleting the index a prior save wrote.
	idx := filepath.Join(gdir, "index.json")
	if err := os.Remove(idx); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	if ids := st.openIDs("g9"); len(ids) != 1 || ids[0] != "staff-0003" {
		t.Fatalf("index rebuild = %v, want [staff-0003]", ids)
	}
	assertNoTmpFiles(t, gdir)

	// A torn index.json must be replaced by valid JSON. The replacement must
	// be a RENAME (new inode), not an in-place rewrite: only rename makes a
	// concurrent reader's view atomic — an in-place os.WriteFile can be
	// observed half-written, which is exactly what this branch used to do.
	if err := os.WriteFile(idx, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	corrupt, err := os.Stat(idx)
	if err != nil {
		t.Fatalf("stat corrupt index: %v", err)
	}
	if ids := st.openIDs("g9"); len(ids) != 1 || ids[0] != "staff-0003" {
		t.Fatalf("rebuild after corruption = %v, want [staff-0003]", ids)
	}
	raw, err := os.ReadFile(idx)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("index.json not valid JSON after rebuild: %v (%q)", err, raw)
	}
	rebuilt, err := os.Stat(idx)
	if err != nil {
		t.Fatalf("stat rebuilt index: %v", err)
	}
	if os.SameFile(corrupt, rebuilt) {
		t.Fatal("index rebuild rewrote index.json in place; want a rename (atomic replacement)")
	}
	assertNoTmpFiles(t, gdir)
}

// assertNoTmpFiles fails if any atomic-write temp file survived in dir.
func assertNoTmpFiles(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("tmp file left behind: %s", filepath.Join(dir, e.Name()))
		}
	}
}

// TestCopyTicketDeepCopy is the regression guard for the shared-slice
// shallow-copy bug: copyTicket must deep-copy Members and per-entry Embeds so
// that mutation on a loaded copy (e.g. the mirror apply step writing LocalPath)
// cannot tear-write another goroutine's copy. Subtests so every shared slice
// is reported independently.
func TestCopyTicketDeepCopy(t *testing.T) {
	newTicket := func() *modules.Ticket {
		return &modules.Ticket{
			ID: "staff-0001", GuildID: "g1", Status: "open",
			Members: []string{"111", "222"},
			Log: []modules.LogEntry{{
				MsgID: "m1", Content: "hello",
				Attachments: []modules.Media{{URL: "https://a.png", Kind: "image"}},
				Embeds:      []modules.Media{{URL: "https://e.png", Kind: "image"}},
				Stickers:    []modules.Media{{URL: "https://s.png", Kind: "sticker"}},
			}},
		}
	}

	t.Run("members", func(t *testing.T) {
		tk := newTicket()
		cp := copyTicket(tk)
		cp.Members[0] = "333"
		if tk.Members[0] != "111" {
			t.Error("Members element shared with the store copy")
		}
		cp.Members = append(cp.Members, "444")
		if len(tk.Members) != 2 {
			t.Error("Members append grew the original")
		}
	})

	for _, tc := range []struct {
		name string
		get  func(*modules.LogEntry) []modules.Media
	}{
		{"attachments", func(e *modules.LogEntry) []modules.Media { return e.Attachments }},
		{"embeds", func(e *modules.LogEntry) []modules.Media { return e.Embeds }},
		{"stickers", func(e *modules.LogEntry) []modules.Media { return e.Stickers }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tk := newTicket()
			cp := copyTicket(tk)
			tc.get(&cp.Log[0])[0].LocalPath = "local/x"
			if got := tc.get(&tk.Log[0])[0].LocalPath; got != "" {
				t.Errorf("write to copy's %s[0].LocalPath leaked into the original (%q)", tc.name, got)
			}
			if n := len(tc.get(&tk.Log[0])); n != 1 {
				t.Errorf("%s append grew the original to %d", tc.name, n)
			}
			_ = append(tc.get(&cp.Log[0]), modules.Media{URL: "https://extra.png"})
			if n := len(tc.get(&tk.Log[0])); n != 1 {
				t.Errorf("%s append grew the original to %d", tc.name, n)
			}
		})
	}
}

// TestTicketByChannelRace is the regression guard for the store-owned
// ticketByChannel lookup. The old module-level version iterated
// store.tickets under the MODULE lock, which does NOT protect that map
// (save() mutates it under the store lock), so concurrent map read + write
// was a Go runtime FATAL. Run with -race.
func TestTicketByChannelRace(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dir, Logger: testLogger{}},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{},
		loaded: true,
	}
	const guild = "g1"
	seed := mkTicket("staff-0001", guild, "staff")
	seed.ChannelID = "chan-1"
	if err := st.save(seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer: continuously save tickets in the same guild. The ID cycles (so
	// the inner map is written) and the Log grows (so the ticket slices too);
	// closed tickets delete from the map, hitting every mutation shape.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 1; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			tk := mkTicket(fmt.Sprintf("staff-%04d", 2+n%8), guild, "staff")
			tk.ChannelID = fmt.Sprintf("chan-%d", n%4)
			if n%2 == 0 {
				tk.Status = "closed"
			}
			for i := range n % 5 {
				tk.Log = append(tk.Log, modules.LogEntry{MsgID: fmt.Sprintf("m%d", i), Content: "x"})
			}
			_ = st.save(tk)
		}
	}()

	const readers = 8
	wg.Add(readers)
	for range readers {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if got := m.ticketByChannel(guild, "chan-1"); got != nil && got.ChannelID != "chan-1" {
					t.Errorf("ticketByChannel returned channel %q, want chan-1", got.ChannelID)
					return
				}
			}
		}()
	}

	time.Sleep(250 * time.Millisecond)
	close(stop)
	wg.Wait()
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

// TestPruneClosed pins the retention sweep: a ticket closed past the window
// has its JSON, files dir, and transcript removed; a fresh one is kept.
func TestPruneClosed(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	old := mkTicket("staff-0001", "g1", "staff")
	old.Status = "closed"
	old.ClosedAt = time.Now().AddDate(0, 0, -2)
	fresh := mkTicket("staff-0002", "g1", "staff")
	fresh.Status = "closed"
	fresh.ClosedAt = time.Now().Add(-12 * time.Hour)
	for _, tk := range []*modules_Ticket{old, fresh} {
		if err := st.save(tk); err != nil {
			t.Fatalf("save %s: %v", tk.ID, err)
		}
	}
	// Seed the old ticket's files dir + transcript so the sweep must clean them.
	filesDir := filepath.Join(dir, "tickets", "g1", "staff-0001", "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, "a.png"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tickets", "g1", "staff-0001.html"), []byte("<html>"), 0644); err != nil {
		t.Fatal(err)
	}

	if n := st.pruneClosed(1); n != 1 {
		t.Fatalf("pruneClosed = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "tickets", "g1", "staff-0001.json")); !os.IsNotExist(err) {
		t.Fatal("old ticket JSON should be removed")
	}
	if _, err := os.Stat(filesDir); !os.IsNotExist(err) {
		t.Fatal("old ticket files dir should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "tickets", "g1", "staff-0001.html")); !os.IsNotExist(err) {
		t.Fatal("old ticket transcript should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "tickets", "g1", "staff-0002.json")); err != nil {
		t.Fatalf("fresh ticket JSON should be kept: %v", err)
	}
}

// TestMarkClosedIdempotent pins that closing an already-closed ticket is a
// no-op: the second call returns false and no duplicate system entry is added.
func TestMarkClosedIdempotent(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dir, Logger: testLogger{}},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		loaded: true,
	}
	tk := mkTicket("staff-0001", "g1", "staff")
	if !m.markClosed(tk, "42", "tester", "user") {
		t.Fatal("first markClosed should return true")
	}
	if m.markClosed(tk, "42", "tester", "user") {
		t.Fatal("second markClosed should return false")
	}
	closes := 0
	for _, e := range tk.Log {
		if e.MsgID == "system-close-staff-0001" {
			closes++
		}
	}
	if closes != 1 {
		t.Fatalf("system-close entries = %d, want 1", closes)
	}
	if tk.Status != "closed" || tk.CloseReason != "user" {
		t.Fatalf("close state = %q/%q, want closed/user", tk.Status, tk.CloseReason)
	}
}

// oldStyleSave reproduces the PRE-FIX mutation shape exactly: load a private
// copy of the ticket, apply fn to that copy, then save the whole copy back.
// Two callers doing this concurrently each hold their own copy, so the later
// save silently drops the earlier one's change — the lost-update defect that
// store.mutate replaces. Test-only: it exists for before/after evidence.
func oldStyleSave(st *store, guildID, ticketID string, fn func(*modules.Ticket) bool) error {
	tk, err := st.load(guildID, ticketID)
	if err != nil {
		return err
	}
	if tk == nil {
		return fmt.Errorf("ticket %s not found", ticketID)
	}
	fn(tk)
	return st.save(tk)
}

// logHasMsgID reports whether the ticket's log contains an entry with msgID.
func logHasMsgID(tk *modules.Ticket, msgID string) bool {
	if tk == nil {
		return false
	}
	for _, e := range tk.Log {
		if e.MsgID == msgID {
			return true
		}
	}
	return false
}

// logMsgIDs returns the log's MsgIDs in order (test failure messages).
func logMsgIDs(tk *modules.Ticket) []string {
	if tk == nil {
		return nil
	}
	out := make([]string, 0, len(tk.Log))
	for _, e := range tk.Log {
		out = append(out, e.MsgID)
	}
	return out
}

// mustLoad loads a ticket and fails the test on error or a missing ticket.
func mustLoad(t *testing.T, st *store, guildID, ticketID string) *modules.Ticket {
	t.Helper()
	tk, err := st.load(guildID, ticketID)
	if err != nil || tk == nil {
		t.Fatalf("load %s/%s = (%v, %v), want a ticket", guildID, ticketID, tk, err)
	}
	return tk
}

// TestMutatePreventsLostUpdate is the regression guard for the reported
// defect: every mutation was a load → modify → save read-modify-write, so two
// callers holding copies of the same ticket silently dropped each other's
// changes. The old shape is reproduced first (the stale save wins, the other
// entry is gone), then the same two appends go through store.mutate.
func TestMutatePreventsLostUpdate(t *testing.T) {
	newStore := func(t *testing.T) *store {
		t.Helper()
		st, err := openStore(t.TempDir())
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		tk := mkTicket("staff-0001", "g1", "staff")
		tk.Log = nil
		if err := st.save(tk); err != nil {
			t.Fatalf("seed save: %v", err)
		}
		return st
	}
	appendEntry := func(id string) func(*modules.Ticket) bool {
		return func(tk *modules.Ticket) bool {
			tk.Log = append(tk.Log, modules.LogEntry{MsgID: id, Content: id})
			return true
		}
	}

	t.Run("old pattern: a stale copy overwrites the other's entry", func(t *testing.T) {
		st := newStore(t)
		// Writer B loads its copy BEFORE writer A saves (the interleaving the
		// old shape allows: logMessageCreate held a load result across its
		// append while the mirror worker held a snapshot of the same ticket).
		staleB, err := st.load("g1", "staff-0001")
		if err != nil || staleB == nil {
			t.Fatalf("load b: %v", err)
		}
		// Writer A runs the whole pre-fix sequence: load → append → save.
		if err := oldStyleSave(st, "g1", "staff-0001", appendEntry("first")); err != nil {
			t.Fatalf("save a: %v", err)
		}
		if ids := logMsgIDs(mustLoad(t, st, "g1", "staff-0001")); len(ids) != 1 || ids[0] != "first" {
			t.Fatalf("after A: %v, want [first]", ids)
		}
		// Writer B saves its stale copy — A's entry is gone.
		appendEntry("second")(staleB)
		if err := st.save(staleB); err != nil {
			t.Fatalf("save b: %v", err)
		}
		if ids := logMsgIDs(mustLoad(t, st, "g1", "staff-0001")); len(ids) != 1 || ids[0] != "second" {
			t.Fatalf("stale-save shape stored %v, want exactly [second] (the lost-update bug)", ids)
		}
	})

	t.Run("mutate: both entries survive", func(t *testing.T) {
		st := newStore(t)
		for _, id := range []string{"first", "second"} {
			_, changed, err := st.mutate("g1", "staff-0001", appendEntry(id))
			if err != nil {
				t.Fatalf("mutate %s: %v", id, err)
			}
			if !changed {
				t.Fatalf("mutate %s reported no change", id)
			}
		}
		got, _ := st.load("g1", "staff-0001")
		if ids := logMsgIDs(got); len(ids) != 2 || ids[0] != "first" || ids[1] != "second" {
			t.Fatalf("mutate stored %v, want [first second]", ids)
		}
	})

	t.Run("mutate on a missing ticket reports not-found without writing", func(t *testing.T) {
		st := newStore(t)
		tk, changed, err := st.mutate("g1", "staff-9999", appendEntry("x"))
		if err != nil {
			t.Fatalf("mutate: %v", err)
		}
		if tk != nil || changed {
			t.Fatalf("missing ticket mutate = (%v, %v), want (nil, false)", tk, changed)
		}
	})
}

// TestMutateConcurrentNoLostUpdates is the concurrent stress proof: N writers
// append distinct entries while M media writers fill LocalPath on the seed
// entries. Under the old load/save shape most appends were lost; with mutate
// every entry and every LocalPath must survive. Run with -race.
func TestMutateConcurrentNoLostUpdates(t *testing.T) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	const seed = 8
	tk := mkTicket("staff-0001", "g1", "staff")
	tk.Log = nil
	for i := range seed {
		tk.Log = append(tk.Log, modules.LogEntry{
			MsgID:       fmt.Sprintf("seed-%d", i),
			Attachments: []modules.Media{{URL: "https://cdn/x.png", Kind: "image", Filename: "x.png"}},
		})
	}
	if err := st.save(tk); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for n := range perWriter {
				id := fmt.Sprintf("w%d-%d", w, n)
				_, _, err := st.mutate("g1", "staff-0001", func(tk *modules.Ticket) bool {
					if logHasMsgID(tk, id) {
						return false
					}
					tk.Log = append(tk.Log, modules.LogEntry{MsgID: id, Content: id})
					return true
				})
				if err != nil {
					t.Errorf("mutate append %s: %v", id, err)
					return
				}
			}
		}(w)
	}
	for i := range seed {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fixes := []mediaFix{{
				msgID: fmt.Sprintf("seed-%d", i), kind: "attachments", index: 0,
				localPath: fmt.Sprintf("files/seed-%d.png", i),
			}}
			if _, _, err := st.mutate("g1", "staff-0001", func(tk *modules.Ticket) bool {
				return applyMediaFixes(tk, fixes)
			}); err != nil {
				t.Errorf("mutate media %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got, err := st.load("g1", "staff-0001")
	if err != nil || got == nil {
		t.Fatalf("load after stress: %v", err)
	}
	count := map[string]int{}
	local := map[string]string{}
	for _, e := range got.Log {
		count[e.MsgID]++
		if len(e.Attachments) > 0 {
			local[e.MsgID] = e.Attachments[0].LocalPath
		}
	}
	for id, n := range count {
		if n != 1 {
			t.Fatalf("MsgID %s appears %d times, want 1", id, n)
		}
	}
	for w := range writers {
		for n := range perWriter {
			id := fmt.Sprintf("w%d-%d", w, n)
			if count[id] != 1 {
				t.Fatalf("lost update: appended entry %s present %d times, want 1", id, count[id])
			}
		}
	}
	for i := range seed {
		id := fmt.Sprintf("seed-%d", i)
		if want := fmt.Sprintf("files/seed-%d.png", i); local[id] != want {
			t.Fatalf("%s LocalPath = %q, want %q", id, local[id], want)
		}
	}
}

// TestMutateClaimCompareAndSet pins the claim path: the check and the write
// share one store mutation, so the second claimer sees the first winner
// instead of overwriting them (with the old lock-then-save shape both claims
// could pass and the later one won silently).
func TestMutateClaimCompareAndSet(t *testing.T) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	if err := st.save(mkTicket("staff-0001", "g1", "staff")); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	claim := func(userID string) (*modules.Ticket, bool) {
		t.Helper()
		tk, changed, err := st.mutate("g1", "staff-0001", func(cur *modules.Ticket) bool {
			if cur.Status != "open" || cur.ClaimerID != "" {
				return false
			}
			cur.ClaimerID = userID
			cur.ClaimedAt = time.Now().UTC()
			return true
		})
		if err != nil {
			t.Fatalf("mutate claim %s: %v", userID, err)
		}
		return tk, changed
	}

	first, changed := claim("111")
	if !changed || first == nil || first.ClaimerID != "111" {
		t.Fatalf("first claim = (%v, %v), want the caller to win", first, changed)
	}
	second, changed := claim("222")
	if changed {
		t.Fatal("second claim must not change the ticket")
	}
	if second == nil || second.ClaimerID != "111" {
		t.Fatalf("second claimer saw %+v, want the first winner 111", second)
	}
	got, _ := st.load("g1", "staff-0001")
	if got == nil || got.ClaimerID != "111" {
		t.Fatalf("persisted claimer = %+v, want 111", got)
	}
}

// TestMutateByChannelNeedsOpenTicket pins the channel-keyed variant: it
// resolves the OPEN ticket owning the channel inside the same lock, returns
// (nil, false) for unknown channels, and does not write when fn declines.
func TestMutateByChannelNeedsOpenTicket(t *testing.T) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	tk := mkTicket("staff-0001", "g1", "staff")
	tk.ChannelID = "chan-1"
	if err := st.save(tk); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	if got, changed, err := st.mutateByChannel("g1", "nope", func(*modules.Ticket) bool { return true }); err != nil || got != nil || changed {
		t.Fatalf("unknown channel = (%v, %v, %v), want (nil, false, nil)", got, changed, err)
	}
	if got, changed, err := st.mutateByChannel("g1", "chan-1", func(*modules.Ticket) bool { return false }); err != nil || got == nil || changed {
		t.Fatalf("declined fn = (%v, %v, %v), want the copy with changed=false", got, changed, err)
	}
	got, changed, err := st.mutateByChannel("g1", "chan-1", func(cur *modules.Ticket) bool {
		cur.Log = append(cur.Log, modules.LogEntry{MsgID: "m2"})
		return true
	})
	if err != nil || !changed || got == nil || !logHasMsgID(got, "m2") {
		t.Fatalf("append via channel = (%v, %v, %v)", got, changed, err)
	}
	persisted, _ := st.load("g1", "staff-0001")
	if !logHasMsgID(persisted, "m2") {
		t.Fatalf("channel mutation not persisted: %v", logMsgIDs(persisted))
	}

	// A closed ticket is not reachable by channel.
	closed := mkTicket("staff-0002", "g1", "staff")
	closed.ChannelID = "chan-2"
	closed.Status = "closed"
	if err := st.save(closed); err != nil {
		t.Fatalf("closed save: %v", err)
	}
	if got, changed, err := st.mutateByChannel("g1", "chan-2", func(*modules.Ticket) bool { return true }); err != nil || got != nil || changed {
		t.Fatalf("closed ticket by channel = (%v, %v, %v), want (nil, false, nil)", got, changed, err)
	}
}

// TestHasOpenTicketOnChannel pins the copy-free channel probe: it must return
// true for an open ticket's channel, false for anything else (unknown channel,
// closed ticket, empty channelID, same channelID in a different guild).
func TestHasOpenTicketOnChannel(t *testing.T) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	open := mkTicket("staff-0001", "g1", "staff")
	open.ChannelID = "chan-1"
	if err := st.save(open); err != nil {
		t.Fatalf("open save: %v", err)
	}
	// A closed ticket in the same guild (its channel must NOT match) and an
	// open ticket in another guild on its OWN channel.
	closed := mkTicket("staff-0002", "g1", "staff")
	closed.ChannelID = "chan-2"
	closed.Status = "closed"
	if err := st.save(closed); err != nil {
		t.Fatalf("closed save: %v", err)
	}
	other := mkTicket("staff-0003", "g2", "staff")
	other.ChannelID = "chan-3"
	if err := st.save(other); err != nil {
		t.Fatalf("other save: %v", err)
	}
	if !st.hasOpenTicketOnChannel("g1", "chan-1") {
		t.Error("open ticket's channel returned false")
	}
	if !st.hasOpenTicketOnChannel("g2", "chan-3") {
		t.Error("other guild's own open channel returned false")
	}
	if st.hasOpenTicketOnChannel("g1", "nope") {
		t.Error("unknown channel returned true")
	}
	if st.hasOpenTicketOnChannel("g1", "chan-2") {
		t.Error("closed ticket's channel returned true")
	}
	if st.hasOpenTicketOnChannel("g1", "") {
		t.Error("empty channelID returned true")
	}
	// Cross-guild scoping: a channel id that names a ticket in a DIFFERENT
	// guild must not match here, and a guild with no tickets never matches.
	if st.hasOpenTicketOnChannel("g2", "chan-1") {
		t.Error("another guild's channel returned true")
	}
	if st.hasOpenTicketOnChannel("g3", "chan-1") {
		t.Error("a guild with no tickets returned true")
	}

	// The open-status guard as defence in depth: saveLocked evicts closed
	// tickets from the map, so a non-open record only reaches this probe if a
	// future path inserts one — inject it directly to pin the check.
	st.tickets["g1"]["staff-0002"] = &modules.Ticket{
		ID: "staff-0002", GuildID: "g1", ChannelID: "chan-9", Status: "closed",
	}
	if st.hasOpenTicketOnChannel("g1", "chan-9") {
		t.Error("a non-open record in the map returned true")
	}
	delete(st.tickets["g1"], "staff-0002")

	// The probe must not disturb the store. It returns no ticket, so what is
	// asserted here is the observable invariant (the stored pointer is the
	// same object and the same answer still holds) rather than the copy-free
	// property itself, which is structural (the body never calls copyTicket).
	before := st.tickets["g1"]["staff-0001"]
	if !st.hasOpenTicketOnChannel("g1", "chan-1") {
		t.Fatal("probe became inconsistent across calls")
	}
	if after := st.tickets["g1"]["staff-0001"]; after != before {
		t.Error("probe replaced or rewrote the stored ticket")
	}
	if got, _ := st.load("g1", "staff-0001"); got == nil || got.Status != "open" || got.ChannelID != "chan-1" {
		t.Fatalf("probe mutated the stored ticket: %+v", got)
	}
}
