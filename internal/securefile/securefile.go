// Package securefile encrypts on-disk state with AES-256-GCM.
//
// Purpose: bot state on disk holds data we promised to protect (ticket
// transcripts, moderation records). Filesystem permissions alone only stop
// other users on the same box; they do nothing for a stolen disk, a snapshot,
// or a backup copied off the host. Encrypting the bytes at rest covers those.
//
// Design notes:
//
//   - One operator key, loaded from <botDir>/data.key (0600, generated on
//     first run). It never lives in config.yml, .env, or the repo.
//   - Each file gets a random 96-bit nonce, so identical plaintexts encrypt
//     to different ciphertexts and no nonce is ever reused under the key.
//   - Encrypted files carry a 5-byte magic header (see magic). Anything
//     without it is treated as legacy plaintext and returned unchanged, which
//     makes migration free: old files keep loading and become encrypted the
//     next time they are written.
//   - A nil *Key disables encryption. That is the test/default path; callers
//     in production always hold a real key.
package securefile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// magic marks an encrypted blob. The trailing byte is a format version so a
// future change to the envelope can be detected instead of misparsed.
var magic = []byte{'M', 'F', 'A', 'T', 1}

// ErrWrongKey is returned when a blob carries our header but will not
// authenticate: a different key, a truncated file, or tampering. GCM cannot
// tell those apart, and all three must fail closed.
var ErrWrongKey = errors.New("securefile: cannot decrypt (wrong key, truncated, or tampered)")

// Key seals and opens state files. A nil *Key is valid and disables
// encryption (plaintext in, plaintext out).
//
// raw is kept solely so LoadOrCreateKey can persist a freshly generated key;
// nothing else reads it.
type Key struct {
	aead cipher.AEAD
	raw  []byte
}

// NewKey builds a Key from raw key material, which must be KeySize bytes.
func NewKey(raw []byte) (*Key, error) {
	if len(raw) != KeySize {
		return nil, fmt.Errorf("securefile: key must be %d bytes, got %d", KeySize, len(raw))
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("securefile: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("securefile: new gcm: %w", err)
	}
	return &Key{aead: aead, raw: append([]byte(nil), raw...)}, nil
}

// GenerateKey returns a fresh random Key.
func GenerateKey() (*Key, error) {
	raw := make([]byte, KeySize)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("securefile: read random: %w", err)
	}
	return NewKey(raw)
}

// LoadOrCreateKey reads the key at path, generating and persisting one at
// 0600 when the file is absent or empty. A malformed key file is an error,
// never silently regenerated: regenerating would make every encrypted file
// on disk permanently unreadable.
func LoadOrCreateKey(path string) (*Key, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(raw) == KeySize {
			return NewKey(raw)
		}
		// An empty file is treated as "not yet provisioned" (an interrupted
		// first run); anything else is corruption.
		if len(raw) == 0 {
			break
		}
		return nil, fmt.Errorf("securefile: %s is %d bytes, want %d (refusing to overwrite an existing key)", path, len(raw), KeySize)
	case os.IsNotExist(err):
		// fall through to provisioning
	default:
		return nil, fmt.Errorf("securefile: read %s: %w", path, err)
	}

	key, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := writeKeyFile(path, key.reveal()); err != nil {
		return nil, err
	}
	return key, nil
}

// writeKeyFile persists key material atomically at 0600, so an interrupted
// write cannot leave a truncated key behind.
func writeKeyFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("securefile: create key dir: %w", err)
	}
	f, err := os.CreateTemp(dir, ".data-key-*")
	if err != nil {
		return fmt.Errorf("securefile: create temp key: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("securefile: chmod temp key: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return fmt.Errorf("securefile: write temp key: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("securefile: sync temp key: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("securefile: close temp key: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("securefile: install key: %w", err)
	}
	return nil
}

// reveal returns the raw key bytes for persistence. Kept unexported so the
// material cannot be pulled out of a Key by arbitrary callers.
func (k *Key) reveal() []byte {
	if k == nil {
		return nil
	}
	// Re-deriving is impossible from cipher.AEAD, so the raw bytes ride along
	// in the struct via this small indirection.
	return k.raw
}

// Seal encrypts plaintext into our envelope. A nil Key returns plaintext
// unchanged. The result is magic || nonce || ciphertext+tag.
func (k *Key) Seal(plaintext []byte) []byte {
	if k == nil {
		return plaintext
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		// crypto/rand failing means the process cannot safely encrypt. Rather
		// than emit a deterministic nonce (which would destroy GCM), panic:
		// callers persist user data and silent corruption is worse.
		panic("securefile: random nonce: " + err.Error())
	}
	out := make([]byte, 0, len(magic)+len(nonce)+len(plaintext)+k.aead.Overhead())
	out = append(out, magic...)
	out = append(out, nonce...)
	return k.aead.Seal(out, nonce, plaintext, magic)
}

// Open decrypts a blob produced by Seal. Data without our magic header is
// returned unchanged, which is how legacy plaintext files keep working.
func (k *Key) Open(data []byte) ([]byte, error) {
	if !IsEncrypted(data) {
		return data, nil
	}
	if k == nil {
		return nil, errors.New("securefile: encrypted file but no key is loaded")
	}
	header := len(magic) + k.aead.NonceSize()
	if len(data) < header {
		return nil, ErrWrongKey
	}
	nonce := data[len(magic):header]
	plain, err := k.aead.Open(nil, nonce, data[header:], magic)
	if err != nil {
		return nil, ErrWrongKey
	}
	return plain, nil
}

// IsEncrypted reports whether data carries the encrypted envelope header.
func IsEncrypted(data []byte) bool {
	if len(data) < len(magic) {
		return false
	}
	for i, b := range magic {
		if data[i] != b {
			return false
		}
	}
	return true
}
