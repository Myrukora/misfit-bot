package tickets

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/misfit/bot/modules"
)

// ticket is marked closed with the reason, the HTML transcript is written next
// to the ticket file, and TranscriptPath is stored relative to the DataDir.
// History fetch and the log-channel post are skipped.
func TestFinalizeTicketChannelDeleted(t *testing.T) {
	dataDir := t.TempDir()
	// A REST client pointed at a local 404 server: resolveGuildName gets an
	// error and returns "" (no real Discord call).
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

	tk := &modules.Ticket{
		ID: "222", GuildID: "111", Status: "open", OpenerID: "999",
		Log: []modules.LogEntry{{MsgID: "1", AuthorID: "999", AuthorName: "Sam", Content: "hello"}},
	}
	if err := st.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}

	if !m.markClosed(tk, "", "", "channel_deleted", "") {
		t.Fatal("markClosed should return true for an open ticket")
	}
	m.finalizeTicket(tk, TypeConfig{}, "", "channel_deleted", closeOptions{skipLock: true, skipHistory: true}, false)

	if tk.Status != "closed" {
		t.Fatalf("status = %q, want closed", tk.Status)
	}
	if tk.CloseReason != "channel_deleted" {
		t.Fatalf("CloseReason = %q, want channel_deleted", tk.CloseReason)
	}
	want := filepath.Join(dataDir, "tickets", "111", "222.html")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("transcript not written: %v", err)
	}
	if tk.TranscriptPath != filepath.Join("tickets", "111", "222.html") {
		t.Fatalf("TranscriptPath = %q, want relative tickets/111/222.html", tk.TranscriptPath)
	}
	html, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if !strings.Contains(string(html), "hello") {
		t.Fatal("transcript missing conversation content")
	}

	// Re-load from disk: the persisted ticket carries the close state.
	got, _ := st.load("111", "222")
	if got == nil || got.Status != "closed" || got.CloseReason != "channel_deleted" {
		t.Fatalf("persisted close state lost: %+v", got)
	}
}

// TestFinalizeDeletedTicketAlreadyClosed is the regression guard for the
// duplicate-close race: when a user close already won, finalizeDeletedTicket
// must NOT edit buttons, save, or spawn the close tail — otherwise the
// transcript is written twice and the log channel gets two "Ticket closed"
// posts. The module lock serializes markClosed, so its bool is the arbiter.
func TestFinalizeDeletedTicketAlreadyClosed(t *testing.T) {
	dataDir := t.TempDir()
	// Every REST call 404s: no real Discord traffic, and any UPDATE attempt
	// (button greying) is observable as a request.
	var updates int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			atomic.AddInt32(&updates, 1)
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	restClient := rest.New(rest.NewClient("fake-token", rest.WithURL(srv.URL)))

	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	guilds := map[string]*Config{
		"111": {Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}},
	}
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{}, Rest: restClient},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: guilds,
		loaded: true,
	}

	// The ticket was already closed by the user path; its channel is gone.
	tk := &modules.Ticket{
		ID: "222", GuildID: "111", ChannelID: "333", MessageID: "444",
		Status: "closed", OpenerID: "999", CloseReason: "user",
		ClosedAt: time.Now().UTC(),
		Log: []modules.LogEntry{{
			MsgID: "system-close-222", AuthorName: "", IsBot: true,
			Content: "_Ticket closed._",
		}},
	}
	if err := st.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}

	m.finalizeDeletedTicket(tk, "channel_deleted")

	// The tail runs in a goroutine on the (buggy) path — give it time to
	// land before asserting it did not.
	time.Sleep(300 * time.Millisecond)

	htmlPath := filepath.Join(dataDir, "tickets", "111", "222.html")
	if _, err := os.Stat(htmlPath); !os.IsNotExist(err) {
		t.Fatalf("close tail ran on an already-closed ticket: transcript %s exists", htmlPath)
	}
	if n := atomic.LoadInt32(&updates); n != 0 {
		t.Fatalf("close tail edited the ticket message %d time(s); want 0", n)
	}
	if tk.Status != "closed" || tk.CloseReason != "user" {
		t.Fatalf("close state = %q/%q, want closed/user (unchanged)", tk.Status, tk.CloseReason)
	}
	closes := 0
	for _, e := range tk.Log {
		if strings.HasPrefix(e.MsgID, "system-close-") {
			closes++
		}
	}
	if closes != 1 {
		t.Fatalf("system-close entries = %d, want 1", closes)
	}
}

func TestOpenLogEmbed(t *testing.T) {
	opened := time.Unix(1763128800, 0).UTC()
	tk := &modules.Ticket{
		ID: "222", GuildID: "111", ChannelID: "333", OpenerID: "999",
		PanelName: "help-desk", OpenedAt: opened,
	}
	g := TypeConfig{Key: "support", Label: "Support"}
	opener := discord.User{ID: 999, Username: "nikapoy", Discriminator: "0"}

	e := openedLogEmbed(tk, g, opener)
	if e.Title != "Ticket Opened" {
		t.Fatalf("title = %q, want Ticket Opened", e.Title)
	}
	for _, want := range []string{
		"Ticket created by **nikapoy** (`222`) in <#333>.",
		"**Panel:** `help-desk`",
		fmt.Sprintf("**Opened on:** <t:%d:F>", opened.Unix()),
	} {
		if !strings.Contains(e.Description, want) {
			t.Fatalf("desc missing %q: %q", want, e.Description)
		}
	}
	if e.Color != logColorOpened {
		t.Fatalf("color = %#x, want %#x", e.Color, logColorOpened)
	}

	// An opener that cannot be resolved falls back to the raw mention, and a
	// ticket with no panel falls back to its type key.
	e = openedLogEmbed(tk, g, discord.User{})
	if !strings.Contains(e.Description, "Ticket created by <@999>") {
		t.Fatalf("desc missing the opener mention: %q", e.Description)
	}
	if !strings.Contains(e.Description, "**Panel:** `help-desk`") {
		t.Fatalf("panel line should still come from the ticket: %q", e.Description)
	}
	noPanel := *tk
	noPanel.PanelName = ""
	if e = openedLogEmbed(&noPanel, g, opener); !strings.Contains(e.Description, "**Panel:** `support`") {
		t.Fatalf("panel line should fall back to the type key: %q", e.Description)
	}
}

func TestCloseLogEmbed(t *testing.T) {
	opened := time.Unix(1763128800, 0).UTC()
	closed := opened.Add(68 * time.Minute)
	tk := &modules.Ticket{
		ID: "222", GuildID: "111", OpenerID: "999", PanelName: "staff-contact",
		OpenedAt: opened, ClosedAt: closed, CloseReason: "user",
	}
	g := TypeConfig{Key: "staff", Label: "Staff"}
	opener := discord.User{ID: 999, Username: "nikapoy", Discriminator: "0"}

	e := closedLogEmbed(tk, g, "777", opener)
	if e.Title != "Ticket Closed" {
		t.Fatalf("title = %q, want Ticket Closed", e.Title)
	}
	for _, want := range []string{
		"Ticket created by **nikapoy** (`222`) has been closed.",
		"**Panel:** `staff-contact`",
		fmt.Sprintf("**Opened on:** <t:%d:F>", opened.Unix()),
		fmt.Sprintf("**Closed on:** <t:%d:F>", closed.Unix()),
		"**Closed by:** <@777>",
		"**Reason:** None",
	} {
		if !strings.Contains(e.Description, want) {
			t.Fatalf("desc missing %q: %q", want, e.Description)
		}
	}
	if e.Color != logColorClosed {
		t.Fatalf("color = %#x, want %#x", e.Color, logColorClosed)
	}
	if e.Thumbnail == nil || e.Thumbnail.URL == "" {
		t.Fatal("closed record should carry the opener avatar thumbnail")
	}

	// A channel-deleted close names the cause instead of a closer.
	del := *tk
	del.CloseReason = "channel_deleted"
	e = closedLogEmbed(&del, g, "", opener)
	if !strings.Contains(e.Description, "**Closed by:** channel deleted") {
		t.Fatalf("desc missing channel-deleted wording: %q", e.Description)
	}
	if strings.Contains(e.Description, "<@") {
		t.Fatalf("channel-deleted record should not mention a closer: %q", e.Description)
	}

	// No closer ID and no channel-delete cause: say unknown, never a dangling
	// empty mention.
	del.CloseReason = "user"
	e = closedLogEmbed(&del, g, "", opener)
	if !strings.Contains(e.Description, "**Closed by:** unknown") {
		t.Fatalf("desc missing the unknown-closer fallback: %q", e.Description)
	}

	// A closer value that is not a snowflake is printed as-is (the raw-ID
	// fallback), never as a broken mention.
	e = closedLogEmbed(&del, g, "bob#0001", opener)
	if !strings.Contains(e.Description, "**Closed by:** bob#0001") {
		t.Fatalf("desc missing the raw closer fallback: %q", e.Description)
	}
}

// TestPostTranscriptReplacesOpenedRecord pins the single-entry log flow: the
// closed record and the transcript go out as ONE message, and the earlier
// "Ticket Opened" message is deleted afterwards with its stored ID cleared.
func TestPostTranscriptReplacesOpenedRecord(t *testing.T) {
	dataDir := t.TempDir()
	var mu sync.Mutex
	var creates, deletes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
			creates = append(creates, r.URL.Path)
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"id":"9001","channel_id":"555"}`))
		case r.Method == http.MethodDelete:
			deletes = append(deletes, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := &TicketsModule{
		ctx: &modules.Context{
			DataDir: dataDir, Logger: testLogger{},
			Rest: rest.New(rest.NewClient("fake-token", rest.WithURL(srv.URL))),
		},
		store:  st,
		module: &ModuleConfig{Version: configVersion},
		guilds: map[string]*Config{
			"111": {Version: configVersion, Types: map[string]*TypeConfig{}, Panels: map[string]PanelConfig{}},
		},
		loaded: true,
	}
	cfg := m.guildConfig("111")
	m.mu.Lock()
	cfg.LogChannel = "555"
	m.mu.Unlock()

	tk := &modules.Ticket{
		ID: "222", GuildID: "111", OpenerID: "999", LogMsgID: "444",
		OpenedAt: time.Now().UTC(), ClosedAt: time.Now().UTC(), CloseReason: "user",
	}
	if err := st.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}
	htmlPath := filepath.Join(dataDir, "tickets", "111", "222.html")
	if err := os.MkdirAll(filepath.Dir(htmlPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(htmlPath, []byte("<html>transcript</html>"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	m.postTranscriptToLogChannel(tk, TypeConfig{Key: "staff", Label: "Staff"}, htmlPath, "777")

	mu.Lock()
	gotCreates := append([]string{}, creates...)
	gotDeletes := append([]string{}, deletes...)
	mu.Unlock()
	if len(gotCreates) != 1 || !strings.HasSuffix(gotCreates[0], "/channels/555/messages") {
		t.Fatalf("creates = %v, want one post to the log channel", gotCreates)
	}
	if len(gotDeletes) != 1 || !strings.HasSuffix(gotDeletes[0], "/messages/444") {
		t.Fatalf("deletes = %v, want the opened record (444) removed", gotDeletes)
	}
	stored, err := st.load("111", "222")
	if err != nil || stored == nil {
		t.Fatalf("load: %v", err)
	}
	if stored.LogMsgID != "" {
		t.Fatalf("LogMsgID = %q, want cleared after the delete", stored.LogMsgID)
	}
}
