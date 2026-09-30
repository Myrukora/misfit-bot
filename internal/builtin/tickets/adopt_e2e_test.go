package tickets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/misfit/bot/modules"
)

// TestAdoptionEndToEnd proves the point of the whole change: a 0.1.0 install
// keeps its tickets data under modules/Go/tickets/; the adoption moves it to
// modules/tickets/ (the new DataDir) BEFORE migrateToV3 runs, so the legacy v2
// single-file config is migrated to the per-guild v3 layout and the ticket is
// visible via the store. Without the adoption, migrateToV3 would see an empty
// DataDir and the v2 config + ticket would be stranded.
func TestAdoptionEndToEnd(t *testing.T) {
	const guildA = "111222333444555666"
	const ticketID = "staff-1"

	base := t.TempDir() // stands in for <repo>/modules
	legacy := filepath.Join(base, "Go", "tickets")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}

	// Legacy v2 single-file config (the 0.1.0 layout).
	legacyCfg := `
version: 2
storage_retention_days: 14
types:
  staff:
    key: staff
    label: Staff
    enabled: true
    category: "` + guildA + `"
`
	if err := os.WriteFile(filepath.Join(legacy, "config.yml"), []byte(legacyCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// A ticket under the legacy tickets/<guild>/<id>.json.
	ticketDir := filepath.Join(legacy, "tickets", guildA)
	if err := os.MkdirAll(ticketDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ticketJSON := `{"id":"` + ticketID + `","type":"staff","guild_id":"` + guildA + `","status":"open"}`
	if err := os.WriteFile(filepath.Join(ticketDir, ticketID+".json"), []byte(ticketJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	// Adopt: modules/Go/tickets/ → modules/tickets/.
	modules.AdoptLegacyBuiltinData(base, "tickets", testLogger{})
	newDir := filepath.Join(base, "tickets")
	if _, err := os.Stat(filepath.Join(newDir, "config.yml")); err != nil {
		t.Fatalf("adoption did not move the config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "tickets", guildA, ticketID+".json")); err != nil {
		t.Fatalf("adoption did not move the ticket: %v", err)
	}

	// The OnLoad migration + store path. (The goroutine/event-hook registration
	// is orthogonal to data migration and is covered by TestBackgroundLoopsStop.)
	m := &TicketsModule{
		ctx:          &modules.Context{DataDir: newDir, Logger: testLogger{}},
		channelGuild: func(channelID string) (string, bool) { return guildA, true },
	}
	if err := m.migrateToV3(newDir); err != nil {
		t.Fatalf("migrateToV3: %v", err)
	}
	st, err := openStore(newDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}

	// The v2 config is migrated to the per-guild v3 layout.
	ga := loadGuildConfig(newDir, guildA, nil)
	if _, ok := ga.Types["staff"]; !ok {
		t.Fatalf("guild A missing staff type after migration: %+v", ga.Types)
	}
	if _, err := os.Stat(moduleConfigPath(newDir) + ".v2-migrated"); err != nil {
		t.Fatalf("legacy config not renamed: %v", err)
	}

	// The ticket is visible via the store.
	tk, err := st.load(guildA, ticketID)
	if err != nil {
		t.Fatalf("store.load: %v", err)
	}
	if tk == nil {
		t.Fatal("ticket not visible via the store after adoption + migration")
	}
}
