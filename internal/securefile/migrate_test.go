package securefile

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func migrateKey(t *testing.T) *Key {
	t.Helper()
	return testKey(t)
}

// The whole reason the backfill exists: a file written once and never saved
// again would otherwise stay plaintext forever.
func TestMigrateEncryptsPlaintextFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ticket.json")
	const secret = `{"log":[{"content":"closed ticket conversation"}]}`
	if err := os.WriteFile(path, []byte(secret), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	key := migrateKey(t)
	res, err := Migrate(dir, key, 0)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.Encrypted != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v, want 1 encrypted 0 failed", res)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if bytes.Contains(raw, []byte("closed ticket conversation")) {
		t.Fatal("plaintext survived migration")
	}
	if !IsEncrypted(raw) {
		t.Fatal("file not encrypted after migration")
	}
	// And it must still be readable through the same key.
	plain, err := key.Open(raw)
	if err != nil {
		t.Fatalf("Open after migrate: %v", err)
	}
	if string(plain) != secret {
		t.Fatalf("content changed: %q", plain)
	}
}

// Running it on every boot must be cheap and harmless.
func TestMigrateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	key := migrateKey(t)

	first, err := Migrate(dir, key, 0)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.Encrypted != 1 {
		t.Fatalf("first pass = %+v", first)
	}
	afterFirst, _ := os.ReadFile(path)

	second, err := Migrate(dir, key, 0)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.Encrypted != 0 || second.Skipped != 1 {
		t.Fatalf("second pass = %+v, want 0 encrypted 1 skipped", second)
	}
	afterSecond, _ := os.ReadFile(path)
	if !bytes.Equal(afterFirst, afterSecond) {
		t.Fatal("second pass rewrote an already-encrypted file")
	}
}

// Nested trees (tickets/<guild>/<id>/files/*) must all be covered.
func TestMigrateRecursesIntoSubdirectories(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "111", "support-0001", "files")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := []string{
		filepath.Join(dir, "111", "support-0001.json"),
		filepath.Join(dir, "111", "support-0001.html"),
		filepath.Join(deep, "555-att0-shot.png"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("secret-"+filepath.Base(f)), 0o600); err != nil {
			t.Fatalf("seed %s: %v", f, err)
		}
	}

	key := migrateKey(t)
	res, err := Migrate(dir, key, 0)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.Encrypted != len(files) {
		t.Fatalf("encrypted %d, want %d (result %+v)", res.Encrypted, len(files), res)
	}
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		if !IsEncrypted(raw) {
			t.Fatalf("%s not encrypted", f)
		}
	}
}

// The key file itself must never be encrypted (that would be unrecoverable).
func TestMigrateSkipsKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "data.key")
	key, err := LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	before, _ := os.ReadFile(keyPath)

	if _, err := Migrate(dir, key, 0); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	after, _ := os.ReadFile(keyPath)
	if !bytes.Equal(before, after) {
		t.Fatal("migration modified the key file")
	}
}

// A nil key must be a no-op, never a destructive pass.
func TestMigrateNilKeyDoesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	if err := os.WriteFile(path, []byte("plain"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := Migrate(dir, nil, 0)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.Encrypted != 0 || res.Scanned != 0 {
		t.Fatalf("nil-key pass did work: %+v", res)
	}
	raw, _ := os.ReadFile(path)
	if IsEncrypted(raw) {
		t.Fatal("nil key encrypted a file")
	}
}

// A missing directory means nothing to migrate, not an error.
func TestMigrateMissingDirIsFine(t *testing.T) {
	res, err := Migrate(filepath.Join(t.TempDir(), "nope"), migrateKey(t), 0)
	if err != nil {
		t.Fatalf("Migrate on missing dir: %v", err)
	}
	if res.Scanned != 0 {
		t.Fatalf("result = %+v", res)
	}
}

// Large files are skipped by the size bound so a big attachment cannot make
// startup buffer gigabytes.
func TestMigrateSkipsOversizeFiles(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 2048), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	small := filepath.Join(dir, "small.json")
	if err := os.WriteFile(small, []byte("tiny"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res, err := Migrate(dir, migrateKey(t), 1024)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if res.Encrypted != 1 {
		t.Fatalf("encrypted %d, want 1: %+v", res.Encrypted, res)
	}
	braw, _ := os.ReadFile(big)
	if IsEncrypted(braw) {
		t.Fatal("oversize file was encrypted despite the bound")
	}
	sraw, _ := os.ReadFile(small)
	if !IsEncrypted(sraw) {
		t.Fatal("small file not encrypted")
	}
}

// Symlinks must be ignored, not followed: a link pointing outside the tree
// must never cause a rewritten foreign file.
func TestMigrateIgnoresSymlinks(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("do not touch"), 0o600); err != nil {
		t.Fatalf("seed outside: %v", err)
	}
	dir := t.TempDir()
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := Migrate(dir, migrateKey(t), 0); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	raw, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if IsEncrypted(raw) {
		t.Fatal("migration followed a symlink and encrypted a foreign file")
	}
}

// The file mode must survive the rewrite: state files stay 0600.
func TestMigratePreservesPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := Migrate(dir, migrateKey(t), 0); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
}
