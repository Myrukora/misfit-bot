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

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/misfit/bot/modules"
)

// TestFinalizeDeletesChannelAfterArchive pins the close order: the ticket is
// archived (JSON + transcript file) before the redundant channel is deleted.
func TestFinalizeDeletesChannelAfterArchive(t *testing.T) {
	h := &wireHarness{}
	m, r := newWireModule(t, h, map[string]*Config{gateGuild: {Version: configVersion, Types: map[string]*TypeConfig{}}})
	tk := &modules.Ticket{
		ID: "staff-0001", GuildID: gateGuild, Type: "staff", OpenerID: "111",
		ChannelID: "555", Status: "open", OpenedAt: time.Now().UTC(),
	}
	if err := m.store.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}
	m.finalizeTicket(tk, TypeConfig{Key: "staff"}, "111", "user", closeOptions{skipLock: true, skipHistory: true}, false)

	deleted := false
	for _, c := range r.callsFiltered("/channels/555") {
		if c.method == "DELETE" {
			deleted = true
		}
	}
	if !deleted {
		t.Fatal("closed ticket channel was not deleted")
	}
	got, _ := m.store.load(gateGuild, "staff-0001")
	if got == nil || got.Status != "closed" || got.TranscriptPath == "" {
		t.Fatalf("archived ticket = %+v, want closed with a transcript path", got)
	}
}

// TestOpenMessageCarriesTicketButtons pins the in-channel entry point: the
// welcome message includes the Close action row and its message ID is
// recorded so a later close can disable the button.
func TestOpenMessageCarriesTicketButtons(t *testing.T) {
	h := &wireHarness{}
	m, r := newWireModule(t, h, map[string]*Config{"111": {Version: configVersion, Types: map[string]*TypeConfig{}}})
	g := TypeConfig{Key: "staff", Label: "Staff", Category: "222333444555666777", AllowClose: boolPtr(true)}
	tk, err := m.openTicket(g, discord.User{ID: 42, Username: "vixen"}, "111", "panel", nil)
	if err != nil {
		t.Fatalf("openTicket: %v", err)
	}
	if tk.MessageID != "456" {
		t.Errorf("ticket MessageID = %q, want the welcome message ID", tk.MessageID)
	}
	calls := r.callsFiltered("/channels/999/messages")
	if len(calls) != 1 {
		t.Fatalf("welcome posts = %d, want 1", len(calls))
	}
	if calls[0].components != 1 {
		t.Fatalf("welcome components = %d, want the Close action row", calls[0].components)
	}
}

// TestOpenTicketDeletesChannelOnSaveFailure is the regression guard for the
// orphaned-channel leak: when the channel was created but the ticket could not
// be persisted, the old code returned the error and left the channel behind —
// a channel whose topic ("misfit-ticket:<id>") points at a ticket that does
// not exist.
//
// The save failure is forced deterministically (no seam needed): <root>/111
// is created as a REGULAR FILE, so store.save's os.MkdirAll of the guild
// directory fails before anything is written.
func TestOpenTicketDeletesChannelOnSaveFailure(t *testing.T) {
	dataDir := t.TempDir()
	root := ticketsRoot(dataDir)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatalf("mkdir tickets root: %v", err)
	}
	// The guild directory cannot exist: a file already occupies its path.
	if err := os.WriteFile(filepath.Join(root, "111"), []byte("not a dir"), 0644); err != nil {
		t.Fatalf("plant file: %v", err)
	}

	var deletes int32
	var deletedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/channels/"):
			atomic.AddInt32(&deletes, 1)
			deletedPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/guilds/111/channels":
			// Enough of the channel object for UnmarshalChannel to decode.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"555","type":0,"guild_id":"111","name":"opener-09-29-26"}`))
		default:
			// Everything else (guild name, opener member, welcome post) is
			// tolerated by openTicket and simply degrades.
			http.Error(w, "not found", http.StatusNotFound)
		}
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
		guilds: map[string]*Config{},
		loaded: true,
	}

	g := TypeConfig{Key: "staff", Label: "Staff", Category: "222333444555666777"}
	opener := discord.User{ID: 999, Username: "opener"}

	tk, err := m.openTicket(g, opener, "111", "panel", nil)
	if err == nil {
		t.Fatal("openTicket should fail when the ticket cannot be persisted")
	}
	if tk != nil {
		t.Fatalf("openTicket returned a ticket on the failure path: %+v", tk)
	}
	if !strings.Contains(err.Error(), "failed to persist ticket") {
		t.Fatalf("error = %v, want a persist failure", err)
	}

	// The channel was created but has no ticket: it MUST have been deleted.
	if n := atomic.LoadInt32(&deletes); n != 1 {
		t.Fatalf("orphaned channel DELETE count = %d, want 1 (path=%q)", n, deletedPath)
	}
	if deletedPath != "/channels/555" {
		t.Fatalf("deleted %q, want /channels/555", deletedPath)
	}
}
