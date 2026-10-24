package moderation

import (
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"testing"

	"github.com/misfit/bot/modules"
)

func newHierarchyModule(t *testing.T) (*ModerationModule, *modFakeBot) {
	t.Helper()
	bot := &modFakeBot{
		selfID: 99,
		name:   "TestBot",
		guild:  &discord.Guild{ID: 1, Name: "Test Guild", OwnerID: 999},
		perms:  discord.PermissionBanMembers | discord.PermissionKickMembers | discord.PermissionModerateMembers,
		members: map[string]*discord.Member{
			"99": {User: discord.User{ID: 99}, RoleIDs: []snowflake.ID{50}},
			"10": {User: discord.User{ID: 10}, RoleIDs: []snowflake.ID{40}},
			"20": {User: discord.User{ID: 20}, RoleIDs: []snowflake.ID{10}},
		},
		roles: map[string]*discord.Role{
			"50": {ID: 50, Position: 50},
			"40": {ID: 40, Position: 40},
			"10": {ID: 10, Position: 10},
		},
	}
	return &ModerationModule{ctx: &modules.Context{Bot: bot}}, bot
}

func TestEnsureCanPunish(t *testing.T) {
	m, bot := newHierarchyModule(t)
	gid := snowflake.ID(1)

	if err := m.ensureCanPunish(gid, 10, 20); err != nil {
		t.Fatalf("lower target rejected: %v", err)
	}
	if err := m.ensureCanPunish(gid, 10, 10); err == nil {
		t.Fatal("self target should be rejected")
	}
	if err := m.ensureCanPunish(gid, 10, 99); err == nil {
		t.Fatal("bot target should be rejected")
	}
	if err := m.ensureCanPunish(gid, 10, 999); err == nil {
		t.Fatal("owner target should be rejected")
	}

	bot.roles["10"].Position = 40 // equal to the moderator
	if err := m.ensureCanPunish(gid, 10, 20); err == nil {
		t.Fatal("equal role should be rejected")
	}
	bot.roles["10"].Position = 45 // above the moderator
	if err := m.ensureCanPunish(gid, 10, 20); err == nil {
		t.Fatal("higher role should be rejected")
	}

	bot.roles["10"].Position = 10
	bot.roles["50"].Position = 5 // bot below the target
	if err := m.ensureCanPunish(gid, 10, 20); err == nil {
		t.Fatal("bot hierarchy should be enforced")
	}

	// Guild owner bypasses the moderator comparison, but the bot still needs
	// to be above the target.
	bot.guild.OwnerID = 10
	bot.roles["50"].Position = 50
	bot.roles["10"].Position = 1000
	if err := m.ensureCanPunish(gid, 10, 20); err == nil {
		t.Fatal("bot hierarchy should still apply")
	}
	bot.roles["10"].Position = 5
	if err := m.ensureCanPunish(gid, 10, 20); err != nil {
		t.Fatalf("owner moderator rejected: %v", err)
	}
}

func TestEnsureCanWarn(t *testing.T) {
	m, _ := newHierarchyModule(t)
	if err := m.ensureCanWarn(1, 10, 20); err != nil {
		t.Fatalf("warn target rejected: %v", err)
	}
	if err := m.ensureCanWarn(1, 10, 10); err == nil {
		t.Fatal("self-warn should fail")
	}
	if err := m.ensureCanWarn(1, 10, 99); err == nil {
		t.Fatal("warning the bot should fail")
	}
}

func TestMemberTopPosition(t *testing.T) {
	m, _ := newHierarchyModule(t)
	if got := m.memberTopPosition(1, &discord.Member{RoleIDs: []snowflake.ID{10, 40}}); got != 40 {
		t.Fatalf("top position = %d, want 40", got)
	}
	if got := m.memberTopPosition(1, nil); got != 0 {
		t.Fatalf("nil member top = %d", got)
	}
}
