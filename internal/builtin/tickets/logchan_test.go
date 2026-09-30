package tickets

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

	if !m.markClosed(tk, "", "", "channel_deleted") {
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
	tk := &modules.Ticket{
		ID: "222", GuildID: "111", ChannelID: "333", OpenerID: "999",
	}
	g := TypeConfig{Key: "support", Label: "Support"}

	// With panel name.
	title, desc := openLogEmbed(tk, g, "help-desk")
	if title != "🎫 Ticket opened" {
		t.Fatalf("title = %q, want 🎫 Ticket opened", title)
	}
	want := "**Support** (`222`) · <#333> · opened by <@999>\nPanel: `help-desk`"
	if desc != want {
		t.Fatalf("desc = %q, want %q", desc, want)
	}

	// Without panel name.
	title, desc = openLogEmbed(tk, g, "")
	if title != "🎫 Ticket opened" {
		t.Fatalf("title = %q, want 🎫 Ticket opened", title)
	}
	want = "**Support** (`222`) · <#333> · opened by <@999>"
	if desc != want {
		t.Fatalf("desc = %q, want %q", desc, want)
	}
}

func TestCloseLogEmbed(t *testing.T) {
	tk := &modules.Ticket{
		ID: "222", GuildID: "111", OpenerID: "999", ClaimerID: "888",
		OpenedAt: time.Now().UTC(),
	}
	g := TypeConfig{Key: "support", Label: "Support"}

	// Normal close (closed by user).
	title, desc := closeLogEmbed(tk, g, "777")
	if title != "Ticket closed" {
		t.Fatalf("title = %q, want Ticket closed", title)
	}
	if !strings.Contains(desc, "**Support** (`222`)") {
		t.Fatalf("desc missing label: %q", desc)
	}
	if !strings.Contains(desc, " · claimed by <@888>") {
		t.Fatalf("desc missing claimer: %q", desc)
	}
	if !strings.Contains(desc, " · closed by <@777>") {
		t.Fatalf("desc missing closed-by: %q", desc)
	}

	// Channel-deleted close.
	tk2 := &modules.Ticket{
		ID: "222", GuildID: "111", OpenerID: "999",
		OpenedAt:    time.Now().UTC(),
		CloseReason: "channel_deleted",
	}
	title, desc = closeLogEmbed(tk2, g, "")
	if title != "Ticket closed" {
		t.Fatalf("title = %q, want Ticket closed", title)
	}
	if !strings.Contains(desc, " · channel deleted") {
		t.Fatalf("desc missing channel-deleted: %q", desc)
	}
	if strings.Contains(desc, "closed by") {
		t.Fatalf("desc should not contain 'closed by' for channel_deleted: %q", desc)
	}
}
