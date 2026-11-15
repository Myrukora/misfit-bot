package tickets

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misfit/bot/internal/securefile"
	"github.com/misfit/bot/modules"
)

// newEncryptedStore builds a store whose key is real, mirroring production.
func newEncryptedStore(t *testing.T) (*store, string, *securefile.Key) {
	t.Helper()
	dir := t.TempDir()
	key, err := securefile.NewKey(bytes.Repeat([]byte{3}, securefile.KeySize))
	if err != nil {
		t.Fatalf("new key: %v", err)
	}
	st, err := openStoreWithKey(dir, key)
	if err != nil {
		t.Fatalf("openStoreWithKey: %v", err)
	}
	return st, dir, key
}

// seedTicket writes one ticket through the store, as production does.
func seedTicket(t *testing.T, st *store, secret string) *modules.Ticket {
	t.Helper()
	tk := &modules.Ticket{
		ID: "support-0001", Type: "support", GuildID: "111",
		ChannelID: "222", OpenerID: "333", Status: "open",
		Log: []modules.LogEntry{{
			MsgID: "555", AuthorID: "333", AuthorName: "someone",
			Content: secret,
		}},
	}
	if err := st.save(tk); err != nil {
		t.Fatalf("save: %v", err)
	}
	return tk
}

// The whole point: message text must NOT be readable on disk.
func TestTicketJSONIsEncryptedOnDisk(t *testing.T) {
	st, dir, _ := newEncryptedStore(t)
	const secret = "this is a private conversation about billing"

	seedTicket(t, st, secret)

	path := filepath.Join(dir, "tickets", "111", "support-0001.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ticket file: %v", err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("plaintext conversation is readable on disk")
	}
	if bytes.Contains(raw, []byte(`"author_id"`)) {
		t.Fatal("plaintext JSON structure is readable on disk")
	}
	if !securefile.IsEncrypted(raw) {
		t.Fatal("ticket file does not carry the encrypted header")
	}
}

// Reading back through the store must return the original content.
func TestTicketContentRoundTripsThroughEncryption(t *testing.T) {
	st, _, _ := newEncryptedStore(t)
	const secret = "please refund order 12345"
	seedTicket(t, st, secret)

	got, err := st.load("111", "support-0001")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil || len(got.Log) != 1 {
		t.Fatalf("load returned %+v", got)
	}
	if got.Log[0].Content != secret {
		t.Fatalf("content = %q, want %q", got.Log[0].Content, secret)
	}
}

// A restarted process must read tickets written by the previous one, so the
// key has to persist rather than be regenerated per store.
func TestTicketSurvivesReopenWithSameKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, dataKeyFile)

	key1, err := securefile.LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("provision key: %v", err)
	}
	st1, err := openStoreWithKey(dir, key1)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}
	const secret = "survives a restart"
	seedTicket(t, st1, secret)

	// Simulate a restart: fresh key load + fresh store over the same dir.
	key2, err := securefile.LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("reload key: %v", err)
	}
	st2, err := openStoreWithKey(dir, key2)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	got, err := st2.load("111", "support-0001")
	if err != nil {
		t.Fatalf("load after reopen: %v", err)
	}
	if got == nil || got.Log[0].Content != secret {
		t.Fatalf("content after reopen = %+v, want %q", got, secret)
	}
}

// A store opened with the WRONG key must refuse the file, never hand back
// garbage that a JSON decoder would treat as empty state.
func TestWrongKeyCannotReadTickets(t *testing.T) {
	st, dir, _ := newEncryptedStore(t)
	seedTicket(t, st, "secret")

	otherKey, err := securefile.NewKey(bytes.Repeat([]byte{8}, securefile.KeySize))
	if err != nil {
		t.Fatalf("new key: %v", err)
	}
	other, err := openStoreWithKey(dir, otherKey)
	if err != nil {
		t.Fatalf("open with other key: %v", err)
	}
	if _, err := other.load("111", "support-0001"); err == nil {
		t.Fatal("loading with the wrong key must fail")
	}
}

// Tickets written before encryption existed must keep loading, and the next
// save must upgrade them in place.
func TestLegacyPlaintextTicketIsReadableAndUpgraded(t *testing.T) {
	st, dir, _ := newEncryptedStore(t)
	const secret = "written by an older build"

	guildDir := filepath.Join(dir, "tickets", "111")
	if err := os.MkdirAll(guildDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := modules.Ticket{
		ID: "support-0002", Type: "support", GuildID: "111",
		ChannelID: "222", OpenerID: "333", Status: "open",
		Log: []modules.LogEntry{{MsgID: "9", Content: secret}},
	}
	raw, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	path := filepath.Join(guildDir, "support-0002.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	// Legible through the store...
	got, err := st.load("111", "support-0002")
	if err != nil {
		t.Fatalf("load legacy: %v", err)
	}
	if got == nil || got.Log[0].Content != secret {
		t.Fatalf("legacy load = %+v", got)
	}

	// ...and the next save encrypts it, so migration is lazy but automatic.
	if err := st.save(got); err != nil {
		t.Fatalf("re-save legacy: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read upgraded: %v", err)
	}
	if !securefile.IsEncrypted(after) {
		t.Fatal("legacy ticket was not upgraded to encrypted on save")
	}
	if bytes.Contains(after, []byte(secret)) {
		t.Fatal("upgraded ticket still exposes plaintext")
	}
}

// The transcript HTML carries the same conversation and must be encrypted too.
func TestTranscriptHTMLIsEncryptedOnDisk(t *testing.T) {
	st, dir, _ := newEncryptedStore(t)
	const secret = "conversation that must not leak"
	tk := seedTicket(t, st, secret)

	htmlPath := filepath.Join(dir, "tickets", "111", tk.ID+".html")
	if err := writePrivateFile(htmlPath, st.seal([]byte("<html>"+secret+"</html>"))); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	raw, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("transcript plaintext is readable on disk")
	}

	plain, err := st.open(raw)
	if err != nil {
		t.Fatalf("open transcript: %v", err)
	}
	if !strings.Contains(string(plain), secret) {
		t.Fatalf("decrypted transcript missing content: %q", plain)
	}
}

// index.json holds ticket IDs only, but it is written through the same helper,
// so make sure a nil key still produces loadable JSON (no accidental sealing).
func TestIndexStaysReadableWithNilKey(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	seedTicket(t, st, "content")

	raw, err := os.ReadFile(filepath.Join(dir, "tickets", "111", "index.json"))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		t.Fatalf("index is not plain JSON with a nil key: %v", err)
	}
	if len(ids) != 1 || ids[0] != "support-0001" {
		t.Fatalf("index = %v", ids)
	}
}
