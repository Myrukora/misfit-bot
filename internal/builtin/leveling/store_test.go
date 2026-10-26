package leveling

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

// TestOpenStoreCreatesDataDirectory pins the fresh-install case: the builtin's
// modules/leveling/ directory does not exist yet, and SQLite needs it before
// it can create leveling.db.
func TestOpenStoreCreatesDataDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "modules", "leveling")
	store, err := openStore(filepath.Join(dir, "leveling.db"), nil)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer store.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("data directory missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("data directory mode = %o, want 700", perm)
	}
	if _, err := os.Stat(filepath.Join(dir, "leveling.db")); err != nil {
		t.Fatalf("database missing: %v", err)
	}
}

// TestAwardMessagePersistence pins the durable round-trip: award XP, reopen the
// database, and read back the exact entry, checkpoint and watermark.
func TestAwardMessagePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leveling.db")
	store, err := openStore(path, nil)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	ts := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	prev, cur, awarded, err := store.AwardMessage("1", "2", "3", 500, ts, 25)
	if err != nil {
		t.Fatalf("AwardMessage: %v", err)
	}
	if !awarded || prev != 0 || cur.XP != 25 || cur.Messages != 1 {
		t.Fatalf("first award = (prev %d, cur %+v, awarded %v), want (0, XP 25, true)", prev, cur, awarded)
	}
	if err := store.TouchSeen("1", ts); err != nil {
		t.Fatalf("TouchSeen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := openStore(path, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	entry, ok, err := reopened.GetEntry("1", "3")
	if err != nil || !ok {
		t.Fatalf("GetEntry after reopen = (%+v, %v, %v), want a hit", entry, ok, err)
	}
	if entry.XP != 25 || entry.Messages != 1 || !entry.UpdatedAt.After(ts.Add(-time.Minute)) {
		t.Fatalf("entry after reopen = %+v, want XP 25 / 1 message", entry)
	}
	cp, err := reopened.Checkpoint("1", "2")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if cp.LastMessageID != 500 {
		t.Fatalf("checkpoint = %d, want 500", cp.LastMessageID)
	}
	meta, err := reopened.Meta("1")
	if err != nil {
		t.Fatalf("Meta: %v", err)
	}
	if !meta.LastSeenAt.Equal(ts) {
		t.Fatalf("last_seen_at = %v, want %v", meta.LastSeenAt, ts)
	}
}

// TestCheckpointNeverRewinds pins that an older message ID cannot move a
// channel checkpoint backwards.
func TestCheckpointNeverRewinds(t *testing.T) {
	m, _, _ := newHarness(t)
	ts := time.Now().UTC()
	if _, _, _, err := m.store.AwardMessage("1", "2", "3", 900, ts, 0); err != nil {
		t.Fatalf("AwardMessage: %v", err)
	}
	if _, _, _, err := m.store.AwardMessage("1", "2", "3", 100, ts, 0); err != nil {
		t.Fatalf("AwardMessage: %v", err)
	}
	cp, err := m.store.Checkpoint("1", "2")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if cp.LastMessageID != 900 {
		t.Fatalf("checkpoint = %d, want 900", cp.LastMessageID)
	}
}

func TestRankingOrder(t *testing.T) {
	m, _, _ := newHarness(t)
	for userID, xp := range map[string]int64{"10": 500, "11": 500, "12": 900, "13": 100} {
		if _, err := m.store.SetXP("1", userID, xp); err != nil {
			t.Fatalf("SetXP: %v", err)
		}
	}
	entries, err := m.store.ListEntries("1", 0, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	want := []string{"12", "10", "11", "13"}
	if len(entries) != len(want) {
		t.Fatalf("got %d entries, want %d", len(entries), len(want))
	}
	for i, id := range want {
		if entries[i].UserID != id {
			t.Fatalf("entry %d = %s, want %s", i, entries[i].UserID, id)
		}
	}
	rank, ok, err := m.store.EntryRank("1", "11")
	if err != nil || !ok {
		t.Fatalf("EntryRank = (%d, %v, %v)", rank, ok, err)
	}
	if rank != 3 {
		t.Fatalf("rank for the tied, higher-ID member = %d, want 3", rank)
	}
}

func TestResetEntryRemovesRewardRows(t *testing.T) {
	m, r, _ := newHarness(t)
	cfg := enabledConfig()
	cfg.Rewards = modulesReward(2, "21", 5, "22")
	saveConfig(t, m, "1", cfg)
	if err := m.store.SetTrackedRoles("1", "3", []string{"21", "22"}); err != nil {
		t.Fatalf("SetTrackedRoles: %v", err)
	}
	if err := m.ResetEntry("1", "3"); err != nil {
		t.Fatalf("ResetEntry: %v", err)
	}
	if added, removed := r.counts(); added != 0 || removed != 2 {
		t.Fatalf("roles removed = %d, want 2", removed)
	}
	if _, ok, _ := m.store.GetEntry("1", "3"); ok {
		t.Fatal("entry still present after reset")
	}
}

func TestAwardVoiceCountsMinutes(t *testing.T) {
	m, _, _ := newHarness(t)
	for range 3 {
		if _, _, err := m.store.AwardVoice("1", "7", 10); err != nil {
			t.Fatalf("AwardVoice: %v", err)
		}
	}
	entry, ok, err := m.store.GetEntry("1", "7")
	if err != nil || !ok {
		t.Fatalf("GetEntry = (%+v, %v, %v)", entry, ok, err)
	}
	if entry.XP != 30 || entry.VoiceSeconds != 180 || entry.Messages != 0 {
		t.Fatalf("voice entry = %+v, want 30 XP / 180s / 0 messages", entry)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	m, _, _ := newHarness(t)
	cfg := enabledConfig()
	cfg.MaxLevel = 42
	cfg.AnnounceChannel = "55"
	cfg.AnnounceMessage = "{user} hit {level} in {server}"
	cfg.IgnoredChannels = []string{"2", "3"}
	cfg.IgnoredRoles = []string{"9"}
	cfg.Rewards = modulesReward(30, "77", 10, "78")
	saveConfig(t, m, "1", cfg)

	got, err := m.store.GetConfig("1")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.MaxLevel != 42 || got.AnnounceChannel != "55" || got.AnnounceMessage != cfg.AnnounceMessage {
		t.Fatalf("config round-trip lost settings: %+v", got)
	}
	if len(got.IgnoredChannels) != 2 || len(got.IgnoredRoles) != 1 {
		t.Fatalf("ignored lists = %v / %v", got.IgnoredChannels, got.IgnoredRoles)
	}
	if len(got.Rewards) != 2 || got.Rewards[0].Level != 10 || got.Rewards[0].RoleID != "78" {
		t.Fatalf("rewards = %+v, want sorted level 10 then 30", got.Rewards)
	}

	// A missing guild still yields defaults, with leveling off.
	other, err := m.store.GetConfig("999")
	if err != nil {
		t.Fatalf("GetConfig(default): %v", err)
	}
	if other.Enabled {
		t.Fatal("a guild with no saved config must be disabled")
	}
}

func TestConfigValidationRejectsBadValues(t *testing.T) {
	m, _, _ := newHarness(t)
	cfg := enabledConfig()
	cfg.MessageXPMin = 50
	cfg.MessageXPMax = 10
	if err := m.store.SaveConfig("1", cfg); err == nil {
		t.Fatal("min > max must be rejected")
	}
	cfg = enabledConfig()
	cfg.Rewards = modulesReward(5, "not-a-snowflake")
	if err := m.store.SaveConfig("1", cfg); err == nil {
		t.Fatal("an invalid reward role ID must be rejected")
	}
	cfg = enabledConfig()
	cfg.MaxLevel = 0
	if err := m.store.SaveConfig("1", cfg); err == nil {
		t.Fatal("max level 0 must be rejected")
	}
}

func TestListEntriesAtLeastXP(t *testing.T) {
	m, _, _ := newHarness(t)
	for userID, xp := range map[string]int64{"1": 50, "2": 500, "3": 5000} {
		if _, err := m.store.SetXP("1", userID, xp); err != nil {
			t.Fatalf("SetXP: %v", err)
		}
	}
	entries, err := m.store.ListEntriesAtLeastXP("1", levelFloor[2])
	if err != nil {
		t.Fatalf("ListEntriesAtLeastXP: %v", err)
	}
	if len(entries) != 2 || entries[0].UserID != "3" {
		t.Fatalf("entries = %+v, want members 3 and 2 only", entries)
	}
}

// modulesReward builds the alternating level/roleID pairs used in tests.
func modulesReward(pairs ...any) []modules.LevelingReward {
	out := make([]modules.LevelingReward, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, modules.LevelingReward{
			Level:  pairs[i].(int),
			RoleID: pairs[i+1].(string),
		})
	}
	return out
}
