package moderation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/misfit/bot/internal/securefile"
	"github.com/misfit/bot/modules"
)

func newEncryptedCaseStore(t *testing.T) (*caseStore, string) {
	t.Helper()
	dir := t.TempDir()
	key, err := securefile.NewKey(bytes.Repeat([]byte{5}, securefile.KeySize))
	if err != nil {
		t.Fatalf("new key: %v", err)
	}
	st, err := openCaseStoreWithKey(dir, key)
	if err != nil {
		t.Fatalf("openCaseStoreWithKey: %v", err)
	}
	return st, dir
}

// Case records name targets, staff and reasons: none of that may be readable
// on disk.
func TestCaseFileIsEncryptedOnDisk(t *testing.T) {
	st, dir := newEncryptedCaseStore(t)
	const reason = "harassing a member in general"

	if _, err := st.Add(modules.ModerationCase{
		GuildID: "111", Type: modules.ModerationTypeWarn,
		TargetID: "444", ModeratorID: "555", Reason: reason,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	path := filepath.Join(dir, "111.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read case file: %v", err)
	}
	if bytes.Contains(raw, []byte(reason)) {
		t.Fatal("case reason is readable on disk")
	}
	if bytes.Contains(raw, []byte("444")) {
		t.Fatal("target user ID is readable on disk")
	}
	if !securefile.IsEncrypted(raw) {
		t.Fatal("case file missing encrypted header")
	}
}

// The full case must round-trip through the store after encryption.
func TestCaseRoundTripsThroughEncryption(t *testing.T) {
	st, _ := newEncryptedCaseStore(t)
	const reason = "spamming invite links"

	added, err := st.Add(modules.ModerationCase{
		GuildID: "111", Type: modules.ModerationTypeWarn,
		TargetID: "444", ModeratorID: "555", Reason: reason,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, err := st.Get("111", added.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Reason != reason {
		t.Fatalf("reason = %q, want %q", got.Reason, reason)
	}
	if got.TargetID != "444" {
		t.Fatalf("target = %q", got.TargetID)
	}
}

// A restarted process reloads cases from disk, so the key must persist and the
// decrypt path must run on load (not just on write).
func TestCasesSurviveReopenWithSameKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, dataKeyFile)

	key1, err := securefile.LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("provision key: %v", err)
	}
	st1, err := openCaseStoreWithKey(dir, key1)
	if err != nil {
		t.Fatalf("store 1: %v", err)
	}
	const reason = "survives a restart"
	if _, err := st1.Add(modules.ModerationCase{
		GuildID: "111", Type: modules.ModerationTypeWarn,
		TargetID: "444", Reason: reason,
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	key2, err := securefile.LoadOrCreateKey(keyPath)
	if err != nil {
		t.Fatalf("reload key: %v", err)
	}
	st2, err := openCaseStoreWithKey(dir, key2)
	if err != nil {
		t.Fatalf("store 2: %v", err)
	}
	list, err := st2.List("111", modules.ModerationCaseFilter{})
	if err != nil {
		t.Fatalf("List after reopen: %v", err)
	}
	if len(list) != 1 || list[0].Reason != reason {
		t.Fatalf("cases after reopen = %+v", list)
	}
}

// Plaintext case files from an older build must keep working, and the next
// save must upgrade them.
func TestLegacyCasesReadableAndUpgraded(t *testing.T) {
	st, dir := newEncryptedCaseStore(t)
	const reason = "written before encryption"

	legacy := caseFile{
		NextID: 2,
		Cases: []modules.ModerationCase{{
			ID: 1, GuildID: "111", Type: modules.ModerationTypeWarn,
			TargetID: "444", Reason: reason,
		}},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	path := filepath.Join(dir, "111.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	list, err := st.List("111", modules.ModerationCaseFilter{})
	if err != nil {
		t.Fatalf("List legacy: %v", err)
	}
	if len(list) != 1 || list[0].Reason != reason {
		t.Fatalf("legacy list = %+v", list)
	}

	// Any save rewrites the whole file, so it becomes encrypted.
	if _, err := st.Add(modules.ModerationCase{
		GuildID: "111", Type: modules.ModerationTypeBan, TargetID: "777",
	}); err != nil {
		t.Fatalf("Add after legacy: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read upgraded: %v", err)
	}
	if !securefile.IsEncrypted(after) {
		t.Fatal("legacy case file was not upgraded on save")
	}
	if bytes.Contains(after, []byte(reason)) {
		t.Fatal("upgraded case file still exposes plaintext")
	}
}

// A nil key must keep the historical plaintext behaviour (existing tests and
// any unprovisioned deployment rely on it).
func TestNilKeyCasesStayPlaintext(t *testing.T) {
	dir := t.TempDir()
	st, err := openCaseStore(dir)
	if err != nil {
		t.Fatalf("openCaseStore: %v", err)
	}
	if _, err := st.Add(modules.ModerationCase{
		GuildID: "111", Type: modules.ModerationTypeWarn,
		TargetID: "444", Reason: "plain",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "111.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if securefile.IsEncrypted(raw) {
		t.Fatal("nil-key store must not encrypt")
	}
	var f caseFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("nil-key file is not plain JSON: %v", err)
	}
}
