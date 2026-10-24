package leveling

import (
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// TestStackRewardsAndReset pins the stacking rule: a multi-level jump grants
// every reward at or below the new level, is tracked, and reset revokes
// exactly what was granted.
func TestStackRewardsAndReset(t *testing.T) {
	m, r, b := newHarness(t)
	const guildID, userID = "1", "3"
	cfg := enabledConfig()
	cfg.Rewards = []modules.LevelingReward{
		{Level: 1, RoleID: "11"},
		{Level: 2, RoleID: "12"},
		{Level: 5, RoleID: "13"},
	}
	saveConfig(t, m, guildID, cfg)
	b.members[guildID+"/"+userID] = &discord.Member{User: discord.User{ID: 3}}

	// 0 XP -> 500 XP is level 3: roles 11 and 12 unlock, 13 does not.
	m.handleLevelUps(guildID, userID, 0, 500, cfg, false)

	added := r.addedRoles()
	if len(added) != 2 {
		t.Fatalf("granted %d roles, want 2: %+v", len(added), added)
	}
	if added[0].RoleID != "11" || added[1].RoleID != "12" {
		t.Fatalf("granted roles = %+v, want 11 then 12", added)
	}
	entry, ok, err := m.store.GetEntry(guildID, userID)
	if err != nil || !ok {
		t.Fatalf("GetEntry = (%+v, %v, %v)", entry, ok, err)
	}
	if len(entry.TrackedRoles) != 2 {
		t.Fatalf("tracked roles = %v, want the two granted roles", entry.TrackedRoles)
	}

	// Staying on the same level must not re-grant anything.
	m.handleLevelUps(guildID, userID, 500, 600, cfg, false)
	if len(r.addedRoles()) != 2 {
		t.Fatalf("a same-level update re-granted roles: %+v", r.addedRoles())
	}

	if err := m.ResetEntry(guildID, userID); err != nil {
		t.Fatalf("ResetEntry: %v", err)
	}
	removed := r.removedRoles()
	if len(removed) != 2 {
		t.Fatalf("removed %d roles, want 2: %+v", len(removed), removed)
	}
	if removed[0].RoleID != "11" || removed[1].RoleID != "12" {
		t.Fatalf("removed roles = %+v, want 11 then 12", removed)
	}
	if _, ok, _ := m.store.GetEntry(guildID, userID); ok {
		t.Fatal("entry still present after reset")
	}
}

// TestRewardSyncGrantsMissingRoles pins the backfill: an eligible member only
// receives roles they do not already hold.
func TestRewardSyncGrantsMissingRoles(t *testing.T) {
	m, r, _ := newHarness(t)
	const guildID = "1"
	cfg := enabledConfig()
	cfg.Rewards = []modules.LevelingReward{{Level: 1, RoleID: "11"}, {Level: 2, RoleID: "12"}}
	saveConfig(t, m, guildID, cfg)
	if _, err := m.store.SetXP(guildID, "3", 300); err != nil {
		t.Fatalf("SetXP: %v", err)
	}
	// The member already holds role 11, so only 12 is granted.
	r.members[3] = &discord.Member{User: discord.User{ID: 3}, RoleIDs: []snowflake.ID{11}}

	queued, err := m.StartRewardSync(guildID)
	if err != nil {
		t.Fatalf("StartRewardSync: %v", err)
	}
	if queued != 1 {
		t.Fatalf("queued = %d, want 1", queued)
	}
	waitForSync(t, m, guildID)

	added := r.addedRoles()
	if len(added) != 1 || added[0].RoleID != "12" {
		t.Fatalf("granted = %+v, want only role 12", added)
	}
	entry, _, err := m.store.GetEntry(guildID, "3")
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if len(entry.TrackedRoles) != 1 || entry.TrackedRoles[0] != "12" {
		t.Fatalf("tracked = %v, want [12]", entry.TrackedRoles)
	}
}

// TestRewardSyncRequiresRewards pins the operator-facing error.
func TestRewardSyncRequiresRewards(t *testing.T) {
	m, _, _ := newHarness(t)
	saveConfig(t, m, "1", enabledConfig())
	if _, err := m.StartRewardSync("1"); err == nil {
		t.Fatal("sync without configured rewards must fail")
	}
}

// TestAnnouncementPlaceholders pins the template rendering.
func TestAnnouncementPlaceholders(t *testing.T) {
	got := renderAnnouncement("{user} reached {level} in {server}", "42", 7, "Guild HQ")
	want := "<@42> reached 7 in Guild HQ"
	if got != want {
		t.Fatalf("renderAnnouncement = %q, want %q", got, want)
	}
	if got := renderAnnouncement("", "42", 3, ""); got != "<@42> reached **level 3**!" {
		t.Fatalf("empty template = %q, want the default message", got)
	}
}

// TestIgnoredChannelBlocksAward pins that an ignored channel still advances the
// checkpoint but grants no XP.
func TestIgnoredChannelBlocksAward(t *testing.T) {
	m, _, _ := newHarness(t)
	cfg := enabledConfig()
	cfg.IgnoredChannels = []string{"2"}
	ts := time.Now().UTC()
	m.awardMessage(cfg, "1", "2", "3", nil, 500, ts, false)

	if entry, ok, _ := m.store.GetEntry("1", "3"); ok {
		t.Fatalf("ignored channel created an entry: %+v", entry)
	}
	cp, err := m.store.Checkpoint("1", "2")
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if cp.LastMessageID != 500 {
		t.Fatalf("checkpoint = %d, want 500 (the checkpoint must still advance)", cp.LastMessageID)
	}
}

// TestCooldownBlocksRapidMessages pins the per-member cooldown shared by live
// and catch-up awards.
func TestCooldownBlocksRapidMessages(t *testing.T) {
	m, _, _ := newHarness(t)
	cfg := enabledConfig()
	base := time.Now().UTC()
	m.awardMessage(cfg, "1", "2", "3", nil, 100, base, false)
	m.awardMessage(cfg, "1", "2", "3", nil, 101, base.Add(30*time.Second), false)
	entry, ok, err := m.store.GetEntry("1", "3")
	if err != nil || !ok {
		t.Fatalf("GetEntry = (%+v, %v, %v)", entry, ok, err)
	}
	if entry.Messages != 1 {
		t.Fatalf("messages = %d, want 1 (the second message is inside the cooldown)", entry.Messages)
	}
	// Past the cooldown the next message awards again.
	m.awardMessage(cfg, "1", "2", "3", nil, 102, base.Add(61*time.Second), false)
	if entry, _, _ := m.store.GetEntry("1", "3"); entry.Messages != 2 {
		t.Fatalf("messages = %d, want 2 after the cooldown", entry.Messages)
	}
}

// waitForSync waits for the background reward sync to finish.
func waitForSync(t *testing.T, m *LevelingModule, guildID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		st := m.sync[guildID]
		running := st != nil && st.running
		m.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("reward sync did not finish")
}
