package tickets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/misfit/bot/internal/securefile"
	"github.com/misfit/bot/modules"
)

// Mirrored ticket attachments are API Data too, so they must not sit on disk
// in readable form.
func TestMirroredMediaIsEncryptedOnDisk(t *testing.T) {
	st, dir, _ := newEncryptedStore(t)
	const secret = "PNG-CONTENT-THAT-MUST-NOT-LEAK"

	filesDir := filepath.Join(dir, "tickets", "111", "support-0001", "files")
	if err := ensurePrivateDir(filesDir); err != nil {
		t.Fatalf("mkdir files: %v", err)
	}
	name := "555-att0-secret.png"
	path := filepath.Join(filesDir, name)
	if err := writePrivateFile(path, st.seal([]byte(secret))); err != nil {
		t.Fatalf("write media: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read media: %v", err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("mirrored attachment content is readable on disk")
	}
	if !securefile.IsEncrypted(raw) {
		t.Fatal("mirrored attachment is not encrypted")
	}
}

// The dashboard-facing reader must hand back plaintext, because the browser
// cannot render ciphertext.
func TestTicketFileContentReturnsPlaintext(t *testing.T) {
	dir := t.TempDir()
	key, err := securefile.NewKey(bytes.Repeat([]byte{4}, securefile.KeySize))
	if err != nil {
		t.Fatalf("new key: %v", err)
	}
	st, err := openStoreWithKey(dir, key)
	if err != nil {
		t.Fatalf("openStoreWithKey: %v", err)
	}
	m := &TicketsModule{store: st, loaded: true}
	m.ctx = &modules.Context{DataDir: dir, Logger: testLogger{}}
	seedTicket(t, st, "hello")

	const secret = "decrypted-bytes-for-the-browser"
	filesDir := filepath.Join(ticketsRoot(dir), "111", "support-0001", "files")
	if err := ensurePrivateDir(filesDir); err != nil {
		t.Fatalf("mkdir files: %v", err)
	}
	name := "555-att0-pic.png"
	if err := writePrivateFile(filepath.Join(filesDir, name), st.seal([]byte(secret))); err != nil {
		t.Fatalf("write media: %v", err)
	}

	got, err := m.TicketFileContent("111", "support-0001", name)
	if err != nil {
		t.Fatalf("TicketFileContent: %v", err)
	}
	if string(got) != secret {
		t.Fatalf("content = %q, want %q", got, secret)
	}
}

// A legacy plaintext attachment must still be served (lazy migration), so an
// archive mirrored before this change does not lose its media previews.
func TestTicketFileContentServesLegacyPlaintext(t *testing.T) {
	dir := t.TempDir()
	key, err := securefile.NewKey(bytes.Repeat([]byte{6}, securefile.KeySize))
	if err != nil {
		t.Fatalf("new key: %v", err)
	}
	st, err := openStoreWithKey(dir, key)
	if err != nil {
		t.Fatalf("openStoreWithKey: %v", err)
	}
	m := &TicketsModule{store: st, loaded: true}
	m.ctx = &modules.Context{DataDir: dir, Logger: testLogger{}}
	seedTicket(t, st, "hello")

	const legacy = "plain-old-media"
	filesDir := filepath.Join(ticketsRoot(dir), "111", "support-0001", "files")
	if err := ensurePrivateDir(filesDir); err != nil {
		t.Fatalf("mkdir files: %v", err)
	}
	name := "555-att1-old.png"
	if err := os.WriteFile(filepath.Join(filesDir, name), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy media: %v", err)
	}

	got, err := m.TicketFileContent("111", "support-0001", name)
	if err != nil {
		t.Fatalf("TicketFileContent: %v", err)
	}
	if string(got) != legacy {
		t.Fatalf("content = %q, want %q", got, legacy)
	}
}

// Path validation must still apply through the reader (it delegates).
func TestTicketFileContentRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	st, err := openStoreWithKey(dir, nil)
	if err != nil {
		t.Fatalf("openStoreWithKey: %v", err)
	}
	m := &TicketsModule{store: st, loaded: true}
	m.ctx = &modules.Context{DataDir: dir, Logger: testLogger{}}

	for _, name := range []string{"../escape.png", "sub/dir.png", "..", ""} {
		if _, err := m.TicketFileContent("111", "support-0001", name); err == nil {
			t.Fatalf("TicketFileContent(%q) should fail", name)
		}
	}
}
