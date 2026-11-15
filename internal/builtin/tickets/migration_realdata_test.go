package tickets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/misfit/bot/internal/securefile"
)

// migrationSource points at a real ticket JSON for the migration test. Set
// MISFIT_MIGRATION_SAMPLE to a plaintext ticket file (copied from a deployed
// box) to exercise the upgrade path on real data; the test skips otherwise so
// the suite stays self-contained.
func migrationSamplePath() string { return os.Getenv("MISFIT_MIGRATION_SAMPLE") }

// TestMigratesRealProductionTicket proves the upgrade path on an actual
// deployed ticket file rather than a synthesized one: plaintext that is
// readable on a server today must load through the encrypted store and be
// rewritten as ciphertext.
func TestMigratesRealProductionTicket(t *testing.T) {
	path := migrationSamplePath()
	if path == "" {
		t.Skip("set MISFIT_MIGRATION_SAMPLE to a plaintext ticket JSON to run this")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("sample unreadable: %v", err)
	}
	if securefile.IsEncrypted(src) {
		t.Skip("sample is already encrypted")
	}

	dir := t.TempDir()
	key, err := securefile.NewKey(bytes.Repeat([]byte{11}, securefile.KeySize))
	if err != nil {
		t.Fatalf("new key: %v", err)
	}
	st, err := openStoreWithKey(dir, key)
	if err != nil {
		t.Fatalf("openStoreWithKey: %v", err)
	}

	// Place the real file where the store expects it: <dataDir>/tickets/<gid>/.
	guildDir := filepath.Join(dir, "tickets", "1459207208591032442")
	if err := os.MkdirAll(guildDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ticketID := "staff-0007"
	dest := filepath.Join(guildDir, ticketID+".json")
	if err := os.WriteFile(dest, src, 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	// It must load, with its conversation intact.
	tk, err := st.load("1459207208591032442", ticketID)
	if err != nil {
		t.Fatalf("load real ticket: %v", err)
	}
	if tk == nil {
		t.Fatal("real ticket did not load")
	}
	if len(tk.Log) == 0 {
		t.Fatal("real ticket lost its log on load")
	}
	firstContent := tk.Log[0].Content

	// Saving must encrypt it in place.
	if err := st.save(tk); err != nil {
		t.Fatalf("re-save real ticket: %v", err)
	}
	after, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read migrated: %v", err)
	}
	if !securefile.IsEncrypted(after) {
		t.Fatal("real ticket was not encrypted on save")
	}
	if firstContent != "" && bytes.Contains(after, []byte(firstContent)) {
		t.Fatal("migrated real ticket still exposes message text")
	}

	// And it must still read back with the same content.
	back, err := st.load("1459207208591032442", ticketID)
	if err != nil {
		t.Fatalf("reload migrated: %v", err)
	}
	if back == nil || len(back.Log) == 0 || back.Log[0].Content != firstContent {
		t.Fatalf("content changed after migration: %+v", back)
	}
}
