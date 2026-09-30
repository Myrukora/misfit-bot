package tickets

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/misfit/bot/modules"
)

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
