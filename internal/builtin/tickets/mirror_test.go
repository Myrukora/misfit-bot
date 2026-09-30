package tickets

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/modules"
)

// TestClassifyEmbeds pins the embed→media mapping: video, image, thumbnail
// (→image), and link, plus the filename fallback (title, then provider name).
func TestClassifyEmbeds(t *testing.T) {
	embeds := []discord.Embed{
		{Video: &discord.EmbedResource{URL: "https://cdn/v.mp4"}},
		{Image: &discord.EmbedResource{URL: "https://cdn/i.png"}, Title: "My Image"},
		{Thumbnail: &discord.EmbedResource{URL: "https://cdn/t.png"}},
		{URL: "https://example.com/page", Provider: &discord.EmbedProvider{Name: "Example"}},
		{URL: "ftp://not-http"}, // non-http → skipped
	}
	got := classifyEmbeds(embeds)
	if len(got) != 4 {
		t.Fatalf("got %d media, want 4 (ftp skipped): %+v", len(got), got)
	}
	if got[0].Kind != "video" || got[0].URL != "https://cdn/v.mp4" {
		t.Fatalf("video: %+v", got[0])
	}
	if got[1].Kind != "image" || got[1].Filename != "My Image" {
		t.Fatalf("image: %+v", got[1])
	}
	if got[2].Kind != "image" {
		t.Fatalf("thumbnail should map to image: %+v", got[2])
	}
	if got[3].Kind != "link" || got[3].Filename != "Example" {
		t.Fatalf("link: %+v", got[3])
	}
}

// TestPlanEntryMediaAndApplyFixes pins the download path: a non-link
// attachment is fetched into <filesDir>/<msgID>-att0-<name>, LocalPath is
// stored relative to dataDir, and a second pass is a no-op (idempotent).
func TestPlanEntryMediaAndApplyFixes(t *testing.T) {
	const body = "hello-mirror"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	filesDir := filepath.Join(dataDir, "tickets", "111", "222", "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		t.Fatal(err)
	}
	entry := modules.LogEntry{
		MsgID: "555",
		Attachments: []modules.Media{
			{URL: srv.URL, Kind: "image", Filename: "shot.png"},
		},
	}

	client := srv.Client()
	logs := []modules.LogEntry{entry}
	fixes := planEntryMedia(client, &logs[0], filesDir, dataDir, testLogger{})
	if len(fixes) != 1 || !applyFixesToLog(logs, fixes) {
		t.Fatalf("plan/apply reported no change: %d fixes", len(fixes))
	}
	entry = logs[0]
	want := filepath.Join(filesDir, "555-att0-shot.png")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if got, _ := os.ReadFile(want); string(got) != body {
		t.Fatalf("file content = %q, want %q", got, body)
	}
	if entry.Attachments[0].LocalPath != filepath.Join("tickets", "111", "222", "files", "555-att0-shot.png") {
		t.Fatalf("LocalPath not relative to dataDir: %q", entry.Attachments[0].LocalPath)
	}

	// Re-run: already mirrored → no fixes planned, nothing applied.
	logs = []modules.LogEntry{entry}
	if fixes := planEntryMedia(client, &logs[0], filesDir, dataDir, testLogger{}); len(fixes) != 0 {
		t.Fatalf("second plan produced %d fixes, want 0", len(fixes))
	}
	if applyFixesToLog(logs, nil) {
		t.Fatal("second run should be a no-op")
	}
}

// TestEntryHasMirrorableMedia pins the enqueue gate: only non-link media with a
// URL and no LocalPath trigger a mirror job.
func TestEntryHasMirrorableMedia(t *testing.T) {
	if entryHasMirrorableMedia(modules.LogEntry{
		Attachments: []modules.Media{{URL: "https://x/a.png", Kind: "image"}},
	}) != true {
		t.Fatal("unmirrored image should be mirrorable")
	}
	if entryHasMirrorableMedia(modules.LogEntry{
		Attachments: []modules.Media{{URL: "https://x/a.png", Kind: "image", LocalPath: "tickets/1/2/files/a.png"}},
	}) != false {
		t.Fatal("already-mirrored image should not be mirrorable")
	}
	if entryHasMirrorableMedia(modules.LogEntry{
		Embeds: []modules.Media{{URL: "https://x/page", Kind: "link"}},
	}) != false {
		t.Fatal("link media should not be mirrorable")
	}
}

// TestEnqueueNonBlocking pins that a full queue drops the job (WARN) instead of
// blocking or panicking.
func TestEnqueueNonBlocking(t *testing.T) {
	q := &mirrorQueue{ch: make(chan mirrorJob, 1), logger: testLogger{}}
	q.enqueue(mirrorJob{guildID: "1", ticketID: "a"}) // fills the buffer
	q.enqueue(mirrorJob{guildID: "1", ticketID: "b"}) // overflow → dropped, no block
	if len(q.ch) != 1 {
		t.Fatalf("queue length = %d, want 1 (overflow dropped)", len(q.ch))
	}

	// A closed stop channel makes enqueue a no-op.
	stop := make(chan struct{})
	close(stop)
	q.setStop(stop)
	q.enqueue(mirrorJob{guildID: "1", ticketID: "c"})
	if len(q.ch) != 1 {
		t.Fatalf("enqueue after stop should be a no-op; len = %d", len(q.ch))
	}
}

// TestMirrorRunWorker pins the worker end-to-end: a queued job is drained, the
// attachment downloaded, and the ticket saved with the relative LocalPath.
func TestMirrorRunWorker(t *testing.T) {
	const body = "worker-body"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	tk := &modules.Ticket{
		ID: "222", GuildID: "111",
		Log: []modules.LogEntry{{
			MsgID:       "555",
			Attachments: []modules.Media{{URL: srv.URL, Kind: "image", Filename: "shot.png"}},
		}},
	}
	if err := st.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}

	q := newMirrorQueue(testLogger{})
	stop := make(chan struct{})
	go q.run(dataDir, st, stop)
	q.enqueue(mirrorJob{guildID: "111", ticketID: "222"})

	// Wait for the worker to drain the job (poll up to 3s).
	var got *modules.Ticket
	for range 60 {
		got, _ = st.load("111", "222")
		if got != nil && len(got.Log) > 0 && got.Log[0].Attachments[0].LocalPath != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(stop)

	got, _ = st.load("111", "222")
	if got == nil || len(got.Log) == 0 {
		t.Fatal("ticket not loaded after worker")
	}
	lp := got.Log[0].Attachments[0].LocalPath
	if !strings.Contains(lp, "555-att0-shot.png") {
		t.Fatalf("LocalPath = %q, want 555-att0-shot.png", lp)
	}
	if _, err := os.Stat(filepath.Join(dataDir, lp)); err != nil {
		t.Fatalf("mirrored file missing: %v", err)
	}
}

// TestMirrorApplyKeepsConcurrentAppend is the literal reported scenario: the
// mirror worker holds a moment-old snapshot of the ticket while a new message
// arrives through the live hook. Planning the downloads from that stale
// snapshot and applying them through store.mutate keeps BOTH the appended
// entry and the mirrored LocalPath — whereas the pre-fix "modify the snapshot
// and save it back" shape dropped the append entirely (first subtest).
func TestMirrorApplyKeepsConcurrentAppend(t *testing.T) {
	const body = "mirror-body"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	seedStore := func(t *testing.T) (*store, string) {
		t.Helper()
		dataDir := t.TempDir()
		st, err := openStore(dataDir)
		if err != nil {
			t.Fatalf("openStore: %v", err)
		}
		tk := &modules.Ticket{
			ID: "222", GuildID: "111", Status: "open",
			Log: []modules.LogEntry{{
				MsgID:       "555",
				Attachments: []modules.Media{{URL: srv.URL, Kind: "image", Filename: "shot.png"}},
			}},
		}
		if err := st.save(tk); err != nil {
			t.Fatalf("seed save: %v", err)
		}
		return st, dataDir
	}
	appendLate := func(t *testing.T, st *store) {
		t.Helper()
		if _, _, err := st.mutate("111", "222", func(cur *modules.Ticket) bool {
			cur.Log = append(cur.Log, modules.LogEntry{MsgID: "late", Content: "arrived mid-mirror"})
			return true
		}); err != nil {
			t.Fatalf("append late entry: %v", err)
		}
	}
	planFixes := func(t *testing.T, dataDir string, snap *modules.Ticket) []mediaFix {
		t.Helper()
		filesDir := filepath.Join(ticketsRoot(dataDir), "111", "222", "files")
		if err := os.MkdirAll(filesDir, 0755); err != nil {
			t.Fatalf("mkdir files: %v", err)
		}
		fixes := planTicketMedia(&http.Client{Timeout: 60 * time.Second}, snap, filesDir, dataDir, testLogger{})
		if len(fixes) != 1 {
			t.Fatalf("planned %d fixes, want 1", len(fixes))
		}
		return fixes
	}

	t.Run("old shape: saving the stale snapshot drops the append", func(t *testing.T) {
		st, dataDir := seedStore(t)
		snap, err := st.load("111", "222")
		if err != nil || snap == nil {
			t.Fatalf("load snapshot: %v", err)
		}
		appendLate(t, st)
		applyMediaFixes(snap, planFixes(t, dataDir, snap))
		if err := st.save(snap); err != nil {
			t.Fatalf("save snapshot: %v", err)
		}
		got, _ := st.load("111", "222")
		if logHasMsgID(got, "late") {
			t.Fatal("stale save kept the append; this subtest no longer demonstrates the bug")
		}
		if got.Log[0].Attachments[0].LocalPath == "" {
			t.Fatal("stale save did not record the mirrored LocalPath either")
		}
	})

	t.Run("mutate: appended entry and LocalPath both survive", func(t *testing.T) {
		st, dataDir := seedStore(t)
		snap, err := st.load("111", "222")
		if err != nil || snap == nil {
			t.Fatalf("load snapshot: %v", err)
		}
		appendLate(t, st)
		fixes := planFixes(t, dataDir, snap)
		if _, _, err := st.mutate("111", "222", func(cur *modules.Ticket) bool {
			return applyMediaFixes(cur, fixes)
		}); err != nil {
			t.Fatalf("apply fixes: %v", err)
		}
		got, _ := st.load("111", "222")
		if !logHasMsgID(got, "late") {
			t.Fatal("concurrent append lost by the mirror apply")
		}
		if lp := got.Log[0].Attachments[0].LocalPath; !strings.Contains(lp, "555-att0-shot.png") {
			t.Fatalf("LocalPath = %q, want 555-att0-shot.png", lp)
		}
	})
}
