package tickets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/misfit/bot/internal/securefile"
)

// TestMigrateRealProductionTree runs the startup backfill against a COPY of
// production ticket data and asserts every file becomes ciphertext while the
// conversations stay readable. Gated on MISFIT_MIGRATION_TREE so the suite
// stays self-contained; this is the check that proves the backfill works on
// real deployed state rather than fixtures.
func TestMigrateRealProductionTree(t *testing.T) {
	root := os.Getenv("MISFIT_MIGRATION_TREE")
	if root == "" {
		t.Skip("set MISFIT_MIGRATION_TREE to a copied modules/tickets dir to run this")
	}
	// Work on a copy so a re-run starts from plaintext again and the caller's
	// sample tree is never mutated.
	sample := t.TempDir()
	if err := os.CopyFS(sample, os.DirFS(root)); err != nil {
		t.Fatalf("copy sample tree: %v", err)
	}
	root = sample

	countPlaintext := func() int {
		n := 0
		_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || filepath.Base(p) == "data.key" {
				return nil
			}
			if info.Size() > maxAttachmentBytes {
				return nil
			}
			raw, rerr := os.ReadFile(p)
			if rerr == nil && !securefile.IsEncrypted(raw) {
				n++
			}
			return nil
		})
		return n
	}

	before := countPlaintext()
	if before == 0 {
		t.Skip("sample tree is already fully encrypted")
	}

	key, err := securefile.LoadOrCreateKey(filepath.Join(root, dataKeyFile))
	if err != nil {
		t.Fatalf("load key: %v", err)
	}
	res, err := securefile.Migrate(root, key, maxAttachmentBytes)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Logf("before=%d scanned=%d encrypted=%d skipped=%d failed=%d",
		before, res.Scanned, res.Encrypted, res.Skipped, res.Failed)

	if res.Failed != 0 {
		t.Fatalf("%d file(s) failed to migrate", res.Failed)
	}
	if after := countPlaintext(); after != 0 {
		t.Fatalf("%d file(s) still plaintext after migration", after)
	}

	// The data must still be readable with the key: scan for any ticket JSON
	// and confirm it decrypts to parseable state.
	guildDir := filepath.Join(root, "1459207208591032442")
	entries, rerr := os.ReadDir(guildDir)
	if rerr != nil {
		t.Fatalf("read guild dir: %v", rerr)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" || e.Name() == "index.json" {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(guildDir, e.Name()))
		if rerr != nil {
			t.Fatalf("read %s: %v", e.Name(), rerr)
		}
		plain, oerr := key.Open(raw)
		if oerr != nil {
			t.Fatalf("%s does not decrypt: %v", e.Name(), oerr)
		}
		if len(plain) == 0 || plain[0] != '{' {
			t.Fatalf("%s decrypted to non-JSON: %q", e.Name(), plain[:min(40, len(plain))])
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no ticket files checked")
	}
	t.Logf("verified %d ticket file(s) decrypt to JSON", checked)
}
