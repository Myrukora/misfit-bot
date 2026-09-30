package tickets

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/misfit/bot/modules"
)

func htmlTicket() *modules.Ticket {
	t := &modules.Ticket{
		ID: "staff-0007", Type: "staff", Group: "staff", GuildID: "999",
		OpenerID: "42", Status: "open",
		OpenedAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
	t.Log = []modules.LogEntry{
		{MsgID: "1", AuthorID: "42", AuthorName: "vixen", Timestamp: time.Now().UTC(),
			Content: "My game **crashes** <script>alert(1)</script>"},
		{MsgID: "2", AuthorID: "7", AuthorName: "helper", Timestamp: time.Now().UTC(),
			Content: "try this", Attachments: []modules.Media{
				{URL: "https://cdn.discordapp.com/a.png", Kind: "image", Filename: "a.png"},
			}},
		{MsgID: "3", AuthorID: "42", AuthorName: "vixen", Timestamp: time.Now().UTC(),
			Content: "deleted msg", Deleted: true},
		{MsgID: "4", AuthorID: "42", AuthorName: "vixen", Timestamp: time.Now().UTC(),
			Content: "edited message", Edited: true},
	}
	return t
}

func TestTranscriptHTMLEscapesAndStructures(t *testing.T) {
	out := buildTranscriptHTML(htmlTicket(), "Test Guild")
	if !strings.Contains(out, "ticket-staff-0007") {
		t.Fatal("missing ticket id in title/body")
	}
	if strings.Contains(out, "<script>alert") {
		t.Fatal("XSS: raw script tag leaked into transcript")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatal("escaping sanity broken: escaped script tag missing")
	}
	for _, want := range []string{"crashes", "try this", "a.png"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in transcript", want)
		}
	}
	if !strings.Contains(out, "msg deleted") {
		t.Fatal("deleted message not marked")
	}
	if !strings.Contains(out, "<img class='attachment") {
		t.Fatal("image attachment not rendered as image")
	}
	// Edited marker: exactly one entry has Edited: true, so the marker
	// appears exactly once. Entries with Edited: false must not carry it.
	editedCount := strings.Count(out, "<span class='edited'>(edited)</span>")
	if editedCount != 1 {
		t.Fatalf("edited marker count = %d, want 1 (one edited entry, no false positives)", editedCount)
	}
}

func TestBuildChannelNameCollision(t *testing.T) {
	at := time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)
	taken := map[string]bool{}
	n1 := buildChannelName("Vixen Fox", at, taken)
	n2 := buildChannelName("Vixen Fox", at, taken)
	n3 := buildChannelName("Vixen Fox", at, taken)
	if n1 != "vixen-fox-08-25-26" {
		t.Fatalf("n1 = %q", n1)
	}
	if n2 != "vixen-fox-08-25-26-2" || n3 != "vixen-fox-08-25-26-3" {
		t.Fatalf("collision suffixes wrong: %q %q", n2, n3)
	}
}

func TestSanitizeChannelName(t *testing.T) {
	cases := map[string]string{
		"Vixen":        "vixen",
		"Cool User 77": "cool-user-77",
		"日本語user":      "user",
		"":             "ticket",
		"---":          "ticket",
	}
	for in, want := range cases {
		if got := sanitizeChannelName(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTranscriptRenderMediaCovers the download/durability surface:
// LocalPath preferred over CDN URL, sticker kind routing (png/gif → img,
// json → link), embed kinds (image/audio/link), and link-embed dedup
// against already-rendered media URLs.
func TestTranscriptRenderMedia(t *testing.T) {
	tk := &modules.Ticket{
		ID: "staff-0042", Type: "staff", Group: "staff", GuildID: "999",
		OpenerID: "42", Status: "closed",
		OpenedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
	tk.Log = []modules.LogEntry{
		{
			MsgID: "100", AuthorID: "42", AuthorName: "vixen",
			Timestamp: time.Now().UTC(), Content: "here are my files",
			Attachments: []modules.Media{
				// Image with LocalPath (mirrored) — must use LocalPath, not URL.
				{URL: "https://cdn.discordapp.com/att/img.png", LocalPath: "files/100-0-img.png", Kind: "image", Filename: "img.png"},
				// Video with LocalPath.
				{URL: "https://cdn.discordapp.com/att/vid.mp4", LocalPath: "files/100-1-vid.mp4", Kind: "video", Filename: "vid.mp4"},
				// Audio with no LocalPath — falls back to URL.
				{URL: "https://cdn.discordapp.com/att/trk.mp3", Kind: "audio", Filename: "trk.mp3"},
				// Default kind (no LocalPath) — link.
				{URL: "https://cdn.discordapp.com/att/doc.pdf", Filename: "doc.pdf"},
			},
			Stickers: []modules.Media{
				// .png sticker → <img>.
				{URL: "https://cdn.discordapp.com/stk/cool.png", LocalPath: "files/100-2-cool.png", Filename: "cool.png"},
				// .gif sticker → <img>.
				{URL: "https://cdn.discordapp.com/stk/wave.gif", Filename: "wave.gif"},
				// .json Lottie sticker → <a> (NOT <img>), carries filename.
				{URL: "https://cdn.discordapp.com/stk/lottie.json", Filename: "lottie.json"},
			},
			Embeds: []modules.Media{
				// Image embed.
				{URL: "https://cdn.discordapp.com/emb/pic.jpg", Kind: "image", Filename: "pic.jpg"},
				// Video embed.
				{URL: "https://cdn.discordapp.com/emb/clip.mp4", Kind: "video", Filename: "clip.mp4"},
				// Audio embed.
				{URL: "https://cdn.discordapp.com/emb/song.mp3", Kind: "audio", Filename: "song.mp3"},
				// Link embed whose URL duplicates the image attachment URL above → skipped.
				{URL: "https://cdn.discordapp.com/att/img.png", Kind: "link", Filename: "dup-link"},
				// Link embed with a unique URL → rendered as <a>.
				{URL: "https://example.com/page", Kind: "link", Filename: "example.com"},
			},
		},
	}

	out := buildTranscriptHTML(tk, "Test Guild")

	// LocalPath preferred over CDN URL for the image attachment.
	if !strings.Contains(out, "src='files/100-0-img.png'") {
		t.Fatal("image attachment did not use LocalPath")
	}
	if strings.Contains(out, "src='https://cdn.discordapp.com/att/img.png'") {
		t.Fatal("image attachment used CDN URL instead of LocalPath")
	}

	// Video attachment uses LocalPath and renders as a <video> element.
	if !strings.Contains(out, "<video class='attachment' controls preload='metadata' src='files/100-1-vid.mp4'></video>") {
		t.Fatal("video attachment not rendered as <video> with LocalPath")
	}

	// Audio attachment falls back to URL (no LocalPath) and renders as an <audio> element.
	if !strings.Contains(out, "<audio class='attachment' controls preload='metadata' src='https://cdn.discordapp.com/att/trk.mp3'></audio>") {
		t.Fatal("audio attachment not rendered as <audio> with URL fallback")
	}

	// Default-kind attachment renders as a link.
	if !strings.Contains(out, "href='https://cdn.discordapp.com/att/doc.pdf'") {
		t.Fatal("default attachment not rendered as link")
	}

	// .png sticker → <img>.
	if !strings.Contains(out, "<img class='attachment' alt='cool.png' src='files/100-2-cool.png'>") {
		t.Fatal(".png sticker not rendered as <img>")
	}

	// .gif sticker → <img> (no LocalPath, uses URL).
	if !strings.Contains(out, "<img class='attachment' alt='wave.gif' src='https://cdn.discordapp.com/stk/wave.gif'>") {
		t.Fatal(".gif sticker not rendered as <img>")
	}

	// .json Lottie sticker → <a> (NOT <img>), carries filename.
	if !strings.Contains(out, "<a class='attachment attlink' href='https://cdn.discordapp.com/stk/lottie.json'>🎫 lottie.json</a>") {
		t.Fatal(".json Lottie sticker not rendered as <a> with filename")
	}
	if strings.Contains(out, "<img class='attachment' alt='lottie.json'") {
		t.Fatal(".json Lottie sticker rendered as <img> (should be <a>)")
	}

	// Image embed.
	if !strings.Contains(out, "<img class='attachment img' loading='lazy' alt='pic.jpg' src='https://cdn.discordapp.com/emb/pic.jpg'>") {
		t.Fatal("image embed not rendered as <img>")
	}

	// Video embed → <video> element (not an anchor, not an img).
	if !strings.Contains(out, "<video class='attachment' controls preload='metadata' src='https://cdn.discordapp.com/emb/clip.mp4'></video>") {
		t.Fatal("video embed not rendered as <video>")
	}

	// Audio embed.
	if !strings.Contains(out, "<audio class='attachment' controls preload='metadata' src='https://cdn.discordapp.com/emb/song.mp3'></audio>") {
		t.Fatal("audio embed not rendered as <audio>")
	}

	// Link embed with unique URL → <a>.
	if !strings.Contains(out, "<a class='attachment attlink' href='https://example.com/page'>🔗 example.com</a>") {
		t.Fatal("unique link embed not rendered as <a>")
	}

	// Link embed duplicating the image attachment URL → skipped.
	// The link embed's filename should not appear in the HTML.
	if strings.Contains(out, "dup-link") {
		t.Fatal("duplicated link embed was rendered (should be skipped)")
	}
}

// TestMessageToEntry verifies that a discord.Message maps to a LogEntry with
// correct author attribution, bot flag, content, and media classification.
func TestMessageToEntry(t *testing.T) {
	fixture := `{
		"id": "555",
		"content": "hello world",
		"timestamp": "2026-09-01T12:00:00Z",
		"author": {"id": "42", "username": "vixen", "bot": true},
		"attachments": [
			{"id": "1", "url": "https://cdn.discordapp.com/att/img.png", "filename": "img.png", "content_type": "image/png", "size": 1024}
		],
		"sticker_items": [
			{"id": "99", "name": "cool", "format_type": 1}
		],
		"embeds": [
			{"url": "https://example.com/page", "title": "Example"}
		]
	}`
	var msg discord.Message
	if err := json.Unmarshal([]byte(fixture), &msg); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	e := messageToEntry(msg)

	// Author attribution.
	if e.AuthorID != "42" {
		t.Fatalf("AuthorID = %q, want 42", e.AuthorID)
	}
	if e.AuthorName != "vixen" {
		t.Fatalf("AuthorName = %q, want vixen", e.AuthorName)
	}
	if !e.IsBot {
		t.Fatal("IsBot = false, want true")
	}

	// Content.
	if e.Content != "hello world" {
		t.Fatalf("Content = %q, want 'hello world'", e.Content)
	}

	// Attachment classified as image.
	if len(e.Attachments) != 1 {
		t.Fatalf("Attachments len = %d, want 1", len(e.Attachments))
	}
	if e.Attachments[0].Kind != "image" {
		t.Fatalf("Attachment Kind = %q, want image", e.Attachments[0].Kind)
	}
	if e.Attachments[0].URL != "https://cdn.discordapp.com/att/img.png" {
		t.Fatalf("Attachment URL = %q", e.Attachments[0].URL)
	}

	// Sticker classified as sticker.
	if len(e.Stickers) != 1 {
		t.Fatalf("Stickers len = %d, want 1", len(e.Stickers))
	}
	if e.Stickers[0].Kind != "sticker" {
		t.Fatalf("Sticker Kind = %q, want sticker", e.Stickers[0].Kind)
	}

	// Embed classified as link with its URL.
	if len(e.Embeds) != 1 {
		t.Fatalf("Embeds len = %d, want 1", len(e.Embeds))
	}
	if e.Embeds[0].Kind != "link" {
		t.Fatalf("Embed Kind = %q, want link", e.Embeds[0].Kind)
	}
	if e.Embeds[0].URL != "https://example.com/page" {
		t.Fatalf("Embed URL = %q, want https://example.com/page", e.Embeds[0].URL)
	}
}

// TestSortLogByTime verifies that sortLogByTime produces chronological order
// and is stable for equal timestamps.
func TestSortLogByTime(t *testing.T) {
	t1 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 1, 12, 0, 1, 0, time.UTC)
	t3 := time.Date(2026, 9, 1, 12, 0, 2, 0, time.UTC)

	log := []modules.LogEntry{
		{MsgID: "c", Timestamp: t3},
		{MsgID: "a", Timestamp: t1},
		{MsgID: "b", Timestamp: t2},
	}

	sortLogByTime(log)

	if log[0].MsgID != "a" || log[1].MsgID != "b" || log[2].MsgID != "c" {
		t.Fatalf("order = %s %s %s, want a b c", log[0].MsgID, log[1].MsgID, log[2].MsgID)
	}

	// Stability: interleaved t1/t2 pairs (15 pairs = 30 entries). Equal keys
	// are non-adjacent, so an unstable sort (e.g. sort.Slice) reorders them.
	// A stable sort keeps the relative input order within each timestamp group.
	const pairs = 15
	log2 := make([]modules.LogEntry, pairs*2)
	for i := 0; i < pairs; i++ {
		log2[2*i] = modules.LogEntry{MsgID: fmt.Sprintf("a%02d", i), Timestamp: t1}
		log2[2*i+1] = modules.LogEntry{MsgID: fmt.Sprintf("b%02d", i), Timestamp: t2}
	}
	sortLogByTime(log2)

	// (1) Chronological: all t1 entries before all t2 entries.
	// (2) Within each group, relative input order is preserved.
	want := make([]string, 0, pairs*2)
	for i := 0; i < pairs; i++ {
		want = append(want, fmt.Sprintf("a%02d", i))
	}
	for i := 0; i < pairs; i++ {
		want = append(want, fmt.Sprintf("b%02d", i))
	}
	for i := 0; i < len(want); i++ {
		if log2[i].MsgID != want[i] {
			t.Fatalf("stable order[%d] = %q, want %q (interleaved t1/t2 must keep per-group input order)", i, log2[i].MsgID, want[i])
		}
	}
}

// TestTranscriptNoContentPlaceholder verifies that a message with no content,
// attachments, stickers, or embeds renders the "(no content)" marker.
func TestTranscriptNoContentPlaceholder(t *testing.T) {
	tk := &modules.Ticket{
		ID: "staff-0099", Type: "staff", Group: "staff", GuildID: "999",
		OpenerID: "42", Status: "open",
		OpenedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
	tk.Log = []modules.LogEntry{
		{MsgID: "1", AuthorID: "42", AuthorName: "vixen", Timestamp: time.Now().UTC()},
	}
	out := buildTranscriptHTML(tk, "Test Guild")
	if !strings.Contains(out, "(no content)") {
		t.Fatal("empty message did not render (no content) marker")
	}
}

// TestMessageToEntryMemberNickname verifies that when a message has both a
// member (with a nick) and a top-level author, the member's display name
// (nick) wins. When the member's nick is empty, the member's username is
// used, not the top-level author's name.
func TestMessageToEntryMemberNickname(t *testing.T) {
	// Fixture with member (nick set) + author: nick wins.
	fixture1 := `{
		"id": "666",
		"content": "hi",
		"timestamp": "2026-09-01T12:00:00Z",
		"author": {"id": "42", "username": "globalname", "bot": false},
		"member": {
			"user": {"id": "42", "username": "globalname", "bot": false},
			"nick": "servernick"
		}
	}`
	var msg1 discord.Message
	if err := json.Unmarshal([]byte(fixture1), &msg1); err != nil {
		t.Fatalf("json.Unmarshal fixture1: %v", err)
	}
	e1 := messageToEntry(msg1)
	if e1.AuthorName != "servernick" {
		t.Fatalf("AuthorName = %q, want servernick (member nick wins)", e1.AuthorName)
	}
	if e1.AuthorID != "42" {
		t.Fatalf("AuthorID = %q, want 42", e1.AuthorID)
	}

	// Fixture with member (nick empty) + author: member username wins, not author.
	fixture2 := `{
		"id": "667",
		"content": "hi",
		"timestamp": "2026-09-01T12:00:00Z",
		"author": {"id": "42", "username": "globalname", "bot": false},
		"member": {
			"user": {"id": "42", "username": "membername", "bot": false},
			"nick": null
		}
	}`
	var msg2 discord.Message
	if err := json.Unmarshal([]byte(fixture2), &msg2); err != nil {
		t.Fatalf("json.Unmarshal fixture2: %v", err)
	}
	e2 := messageToEntry(msg2)
	if e2.AuthorName != "membername" {
		t.Fatalf("AuthorName = %q, want membername (member username, not author)", e2.AuthorName)
	}
}

// TestMergeEntriesInto verifies that mergeEntriesInto dedups by MsgID, adds
// new entries, and sorts the result chronologically.
func TestMergeEntriesInto(t *testing.T) {
	t1 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 1, 12, 0, 1, 0, time.UTC)
	t3 := time.Date(2026, 9, 1, 12, 0, 2, 0, time.UTC)

	tk := &modules.Ticket{
		ID: "staff-0100", Type: "staff", Group: "staff", GuildID: "999",
		OpenerID: "42", Status: "open",
		OpenedAt: t1,
	}
	// Stored log: entries A and B.
	tk.Log = []modules.LogEntry{
		{MsgID: "A", Timestamp: t1},
		{MsgID: "B", Timestamp: t2},
	}

	// Fetched history: repeats B, adds C and D.
	history := []modules.LogEntry{
		{MsgID: "B", Timestamp: t2},
		{MsgID: "C", Timestamp: t3},
		{MsgID: "D", Timestamp: t1},
	}

	mergeEntriesInto(tk, history)

	// Each MsgID appears exactly once.
	seen := map[string]int{}
	for _, e := range tk.Log {
		seen[e.MsgID]++
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("MsgID %s appears %d times, want 1", id, count)
		}
	}

	// All four entries present.
	if len(tk.Log) != 4 {
		t.Fatalf("Log len = %d, want 4", len(tk.Log))
	}

	// Chronological order: D(t1), A(t1), B(t2), C(t3).
	// D and A share t1; D was added after A, so stability puts A before D.
	want := []string{"A", "D", "B", "C"}
	for i, id := range want {
		if tk.Log[i].MsgID != id {
			t.Fatalf("Log[%d].MsgID = %q, want %q (order: %v)", i, tk.Log[i].MsgID, id, want)
		}
	}
}

// TestFinalizeKeepsLateEntry is the close-tail variant of the lost-update
// regression: the close path starts from a caller snapshot, a live event hook
// appends one more log entry while the tail runs, and the finalize apply must
// merge onto the CURRENT stored copy rather than writing its snapshot back —
// so the late entry survives in the STORED log. Under the pre-fix
// load → modify → save tail the snapshot's save erased it.
func TestFinalizeKeepsLateEntry(t *testing.T) {
	dataDir := t.TempDir()
	// Every REST call 404s: no real Discord traffic (resolveGuildName just
	// returns "").
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	restClient := rest.New(rest.NewClient("fake-token", rest.WithURL(srv.URL)))
	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{}, Rest: restClient},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{
			"111": {Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}},
		},
		loaded: true,
	}

	seed := &modules.Ticket{
		ID: "222", GuildID: "111", Status: "open", OpenerID: "999",
		Log: []modules.LogEntry{{MsgID: "1", AuthorID: "999", AuthorName: "Sam", Content: "hello"}},
	}
	if err := st.save(seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	// The closer's snapshot of the open ticket (what CloseTicket passes in).
	stale, err := st.load("111", "222")
	if err != nil || stale == nil {
		t.Fatalf("load snapshot: %v", err)
	}
	// A last message lands while the close tail runs.
	if _, _, err := st.mutate("111", "222", func(cur *modules.Ticket) bool {
		cur.Log = append(cur.Log, modules.LogEntry{MsgID: "late", AuthorID: "7", AuthorName: "helper", Content: "bye"})
		return true
	}); err != nil {
		t.Fatalf("append late entry: %v", err)
	}

	m.finalizeTicket(stale, TypeConfig{}, "", "channel_deleted", closeOptions{skipLock: true, skipHistory: true}, false)

	got, err := st.load("111", "222")
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if !logHasMsgID(got, "late") {
		t.Fatalf("late entry lost by the close tail: stored log = %v", logMsgIDs(got))
	}
	if !logHasMsgID(got, "1") {
		t.Fatalf("pre-close entry lost: stored log = %v", logMsgIDs(got))
	}
	if !logHasMsgID(got, "system-close-222") {
		t.Fatalf("close marker missing: stored log = %v", logMsgIDs(got))
	}
	if got.Status != "closed" || got.CloseReason != "channel_deleted" {
		t.Fatalf("stored close state = %q/%q, want closed/channel_deleted", got.Status, got.CloseReason)
	}
	if got.TranscriptPath == "" {
		t.Fatal("TranscriptPath not persisted")
	}
	// The caller's copy is handed the persisted state back.
	if stale.TranscriptPath != got.TranscriptPath {
		t.Fatalf("caller copy TranscriptPath = %q, want %q", stale.TranscriptPath, got.TranscriptPath)
	}
}

// TestFinalizeTranscriptRepairsLateAppend drives the close tail end-to-end:
// a last message lands between the transcript render's store read and its
// file write (delivered through the render's guild-name REST fetch). The
// reconcile loop must re-render so the .html file — the artifact uploaded to
// the log channel — carries the late entry, not just the stored JSON.
func TestFinalizeTranscriptRepairsLateAppend(t *testing.T) {
	dataDir := t.TempDir()
	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	var lateOnce atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The render's guild-name fetch: land the late entry in the window
		// between the render's store read and its file write.
		if r.URL.Path == "/guilds/111" && lateOnce.CompareAndSwap(false, true) {
			if _, _, err := st.mutate("111", "222", func(cur *modules.Ticket) bool {
				cur.Log = append(cur.Log, modules.LogEntry{MsgID: "late", AuthorID: "7", AuthorName: "helper", Content: "late append"})
				return true
			}); err != nil {
				t.Errorf("late append: %v", err)
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	restClient := rest.New(rest.NewClient("fake-token", rest.WithURL(srv.URL)))
	var warns warnCapture
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{warns: &warns}, Rest: restClient},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{
			"111": {Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}},
		},
		loaded: true,
	}

	seed := &modules.Ticket{
		ID: "222", GuildID: "111", Status: "open", OpenerID: "999",
		Log: []modules.LogEntry{{MsgID: "1", AuthorID: "999", AuthorName: "Sam", Content: "hello"}},
	}
	if err := st.save(seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	snap, err := st.load("111", "222")
	if err != nil || snap == nil {
		t.Fatalf("load snapshot: %v", err)
	}

	m.finalizeTicket(snap, TypeConfig{}, "", "channel_deleted", closeOptions{skipLock: true, skipHistory: true}, false)

	// The repair converged within the bound, so the close tail must be
	// silent: no "stale after the reconcile bound" warning (the repaired
	// transcript is fresh — telling the operator to regenerate would be a
	// false alarm).
	for _, msg := range warns.messages() {
		if strings.Contains(msg, "stale after the reconcile bound") {
			t.Fatalf("repaired close must not warn stale, got %q", msg)
		}
	}

	// The FILE is the artifact: it must carry both the seed entry and the
	// late append, not just the pre-render snapshot.
	raw, err := os.ReadFile(filepath.Join(dataDir, "tickets", "111", "222.html"))
	if err != nil {
		t.Fatalf("read transcript file: %v", err)
	}
	for _, want := range []string{"hello", "late append"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("transcript file missing %q", want)
		}
	}
	got, err := st.load("111", "222")
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if !logHasMsgID(got, "late") {
		t.Fatalf("late entry lost from stored log: %v", logMsgIDs(got))
	}
	if got.TranscriptPath == "" {
		t.Fatal("TranscriptPath not persisted")
	}
}

// TestFinalizeTranscriptWarnsWhenStillDivergent pins the bound-exceeded
// warning: when the log keeps growing faster than the reconcile loop can
// re-render (the guild-name fetch appends a fresh entry on EVERY call, so
// each pass re-renders from a snapshot that is already stale), the loop
// exhausts its 3 passes still divergent and MUST warn — the operator is told
// the cache is stale and regenerable via RefreshTranscript. This is the
// negative counterpart to TestFinalizeTranscriptRepairsLateAppend, which
// converges and must stay silent.
func TestFinalizeTranscriptWarnsWhenStillDivergent(t *testing.T) {
	dataDir := t.TempDir()
	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every guild-name fetch appends a fresh entry, so the stored log
		// always outpaces the render: the reconcile loop never converges.
		if r.URL.Path == "/guilds/111" {
			if _, _, err := st.mutate("111", "222", func(cur *modules.Ticket) bool {
				cur.Log = append(cur.Log, modules.LogEntry{MsgID: fmt.Sprintf("late-%d", len(cur.Log)), AuthorID: "7", AuthorName: "helper", Content: "late append"})
				return true
			}); err != nil {
				t.Errorf("late append: %v", err)
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	restClient := rest.New(rest.NewClient("fake-token", rest.WithURL(srv.URL)))
	var warns warnCapture
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{warns: &warns}, Rest: restClient},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{
			"111": {Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}},
		},
		loaded: true,
	}

	seed := &modules.Ticket{
		ID: "222", GuildID: "111", Status: "open", OpenerID: "999",
		Log: []modules.LogEntry{{MsgID: "1", AuthorID: "999", AuthorName: "Sam", Content: "hello"}},
	}
	if err := st.save(seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	snap, err := st.load("111", "222")
	if err != nil || snap == nil {
		t.Fatalf("load snapshot: %v", err)
	}

	m.finalizeTicket(snap, TypeConfig{}, "", "channel_deleted", closeOptions{skipLock: true, skipHistory: true}, false)

	var found bool
	for _, msg := range warns.messages() {
		if strings.Contains(msg, "stale after the reconcile bound") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a stale-after-bound warning, got %v", warns.messages())
	}
}

// TestRefreshTranscriptSelfHeals pins the on-demand regeneration: an entry
// appended after the close tail is picked up by RefreshTranscript in BOTH
// the returned bytes and the on-disk file, and TranscriptPath stays set.
func TestRefreshTranscriptSelfHeals(t *testing.T) {
	dataDir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	restClient := rest.New(rest.NewClient("fake-token", rest.WithURL(srv.URL)))
	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{}, Rest: restClient},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{
			"111": {Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}},
		},
		loaded: true,
	}

	seed := &modules.Ticket{
		ID: "support-0007", GuildID: "111", Status: "closed", OpenerID: "999",
		Log: []modules.LogEntry{{MsgID: "1", AuthorID: "999", AuthorName: "Sam", Content: "hello"}},
	}
	if err := st.save(seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	// The close tail's render (the file exists, as at close time).
	if _, _, _, err := m.renderTranscriptToDisk("111", "support-0007"); err != nil {
		t.Fatalf("initial render: %v", err)
	}
	// A post-close edit lands.
	if _, _, err := st.mutate("111", "support-0007", func(cur *modules.Ticket) bool {
		cur.Log = append(cur.Log, modules.LogEntry{MsgID: "late", AuthorID: "7", AuthorName: "helper", Content: "late append"})
		return true
	}); err != nil {
		t.Fatalf("append late entry: %v", err)
	}

	data, err := m.RefreshTranscript("111", "support-0007")
	if err != nil {
		t.Fatalf("RefreshTranscript: %v", err)
	}
	if !strings.Contains(string(data), "late append") {
		t.Fatal("returned bytes missing the late entry")
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "tickets", "111", "support-0007.html"))
	if err != nil {
		t.Fatalf("read transcript file: %v", err)
	}
	if !strings.Contains(string(raw), "late append") {
		t.Fatal("on-disk file missing the late entry")
	}
	got, err := st.load("111", "support-0007")
	if err != nil || got == nil {
		t.Fatalf("reload: %v", err)
	}
	if got.TranscriptPath == "" {
		t.Fatal("TranscriptPath not set")
	}
}

// TestRefreshTranscriptGuards pins the ID validation at the trust boundary:
// an unloaded module, malformed guild IDs, a traversal-shaped ticket ID, and
// an unknown ticket all error without writing any file.
func TestRefreshTranscriptGuards(t *testing.T) {
	dataDir := t.TempDir()
	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{}},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{},
	}

	// Unloaded: error, no panic, no file.
	if _, err := m.RefreshTranscript("111", "222"); err == nil {
		t.Fatal("unloaded module must error")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "tickets", "111", "222.html")); !os.IsNotExist(err) {
		t.Fatal("file written for an unloaded module")
	}

	m.loaded = true
	for _, g := range []string{"null", "0", ""} {
		if _, err := m.RefreshTranscript(g, "222"); err == nil {
			t.Fatalf("guild %q must be rejected", g)
		}
	}
	if _, err := m.RefreshTranscript("111", "../x"); err == nil {
		t.Fatal("traversal ticket ID must be rejected")
	}
	if _, err := m.RefreshTranscript("111", "nope-0001"); err == nil {
		t.Fatal("unknown ticket must error")
	}
}
