package securefile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(t *testing.T) *Key {
	t.Helper()
	k, err := NewKey(bytes.Repeat([]byte{7}, KeySize))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return k
}

// A nil Key must be a pass-through: tests and any not-yet-provisioned path
// depend on plaintext surviving unchanged.
func TestNilKeyIsPassthrough(t *testing.T) {
	var k *Key
	in := []byte("hello ticket")
	if got := k.Seal(in); !bytes.Equal(got, in) {
		t.Fatalf("nil Seal = %q, want unchanged", got)
	}
	out, err := k.Open(in)
	if err != nil {
		t.Fatalf("nil Open: %v", err)
	}
	if !bytes.Equal(out, in) {
		t.Fatalf("nil Open = %q, want unchanged", out)
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := testKey(t)
	plain := []byte(`{"log":[{"content":"secret transcript"}]}`)
	sealed := k.Seal(plain)

	if bytes.Contains(sealed, []byte("secret transcript")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	if !IsEncrypted(sealed) {
		t.Fatal("sealed blob missing magic header")
	}
	got, err := k.Open(sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip = %q, want %q", got, plain)
	}
}

// Identical plaintexts must produce different ciphertexts: a fixed nonce
// would break GCM catastrophically and also leak equality between files.
func TestSealUsesFreshNonce(t *testing.T) {
	k := testKey(t)
	plain := []byte("same bytes every time")
	a := k.Seal(plain)
	b := k.Seal(plain)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same plaintext are identical (nonce reuse)")
	}
	for _, blob := range [][]byte{a, b} {
		if got, err := k.Open(blob); err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("Open(%x) = %q, %v", blob[:6], got, err)
		}
	}
}

// Legacy plaintext files predate the envelope; they must keep loading so
// migration is lazy and needs no offline step.
func TestOpenLegacyPlaintext(t *testing.T) {
	k := testKey(t)
	legacy := []byte(`{"next_id":1,"cases":[]}`)
	got, err := k.Open(legacy)
	if err != nil {
		t.Fatalf("Open(legacy): %v", err)
	}
	if !bytes.Equal(got, legacy) {
		t.Fatalf("legacy passthrough = %q, want %q", got, legacy)
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	sealed := testKey(t).Seal([]byte("secret"))
	other, err := NewKey(bytes.Repeat([]byte{9}, KeySize))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("Open with the wrong key must fail")
	} else if !strings.Contains(err.Error(), "cannot decrypt") {
		t.Fatalf("wrong-key error = %v, want ErrWrongKey text", err)
	}
}

// Tampering must be detected, not silently decrypted to garbage.
func TestOpenDetectsTampering(t *testing.T) {
	k := testKey(t)
	sealed := k.Seal([]byte("secret payload"))
	sealed[len(sealed)-1] ^= 0xff
	if _, err := k.Open(sealed); err == nil {
		t.Fatal("flipped ciphertext bit must fail authentication")
	}
}

// A truncated file must fail closed rather than slice out of range.
func TestOpenTruncated(t *testing.T) {
	k := testKey(t)
	if _, err := k.Open(magic); err == nil {
		t.Fatal("header-only blob must fail, not panic")
	}
}

// Encrypted data with no key loaded is an error, never a silent passthrough:
// that would hand ciphertext to a JSON decoder as if it were state.
func TestOpenEncryptedWithoutKey(t *testing.T) {
	sealed := testKey(t).Seal([]byte("secret"))
	var k *Key
	if _, err := k.Open(sealed); err == nil {
		t.Fatal("encrypted blob with nil key must error")
	}
}

func TestNewKeyRejectsBadLength(t *testing.T) {
	if _, err := NewKey([]byte("too short")); err == nil {
		t.Fatal("short key must be rejected")
	}
	if _, err := NewKey(make([]byte, KeySize+1)); err == nil {
		t.Fatal("overlong key must be rejected")
	}
}

// First run provisions a 0600 key; the second load must return the SAME key
// rather than regenerating, or every encrypted file would be orphaned.
func TestLoadOrCreateKeyGeneratesThenReuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.key")

	first, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("first LoadOrCreateKey: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key mode = %o, want 600", perm)
	}

	second, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("second LoadOrCreateKey: %v", err)
	}

	// The same key must open what the first one sealed.
	sealed := first.Seal([]byte("persisted state"))
	got, err := second.Open(sealed)
	if err != nil {
		t.Fatalf("second key cannot open first key's data: %v", err)
	}
	if string(got) != "persisted state" {
		t.Fatalf("round trip across loads = %q", got)
	}
}

// An empty key file is an interrupted first run and may be reprovisioned.
func TestLoadOrCreateKeyReprovisionsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.key")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("seed empty key: %v", err)
	}
	k, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreateKey on empty file: %v", err)
	}
	if k == nil {
		t.Fatal("no key returned")
	}
	info, _ := os.Stat(path)
	if info.Size() != KeySize {
		t.Fatalf("key size = %d, want %d", info.Size(), KeySize)
	}
}

// A corrupted (wrong-length) key must be a hard error. Regenerating it would
// silently destroy every encrypted file on disk.
func TestLoadOrCreateKeyRefusesCorruptKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.key")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatalf("seed corrupt key: %v", err)
	}
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("corrupt key file must error, not regenerate")
	}
	// The file must be left alone.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key back: %v", err)
	}
	if string(raw) != "short" {
		t.Fatalf("corrupt key was overwritten: %q", raw)
	}
}

// Empty input round-trips (an empty moderation case list is legitimate).
func TestEmptyPayload(t *testing.T) {
	k := testKey(t)
	sealed := k.Seal(nil)
	got, err := k.Open(sealed)
	if err != nil {
		t.Fatalf("Open(empty): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty round trip = %q", got)
	}
}
