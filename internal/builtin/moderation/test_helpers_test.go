package moderation

import (
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

type testLogger struct{}

func (testLogger) Debug(string, ...any) {}
func (testLogger) Info(string, ...any)  {}
func (testLogger) Warn(string, ...any)  {}
func (testLogger) Error(string, ...any) {}

type modFakeBot struct {
	commands.Interface
	selfID  snowflake.ID
	guild   *discord.Guild
	members map[string]*discord.Member
	roles   map[string]*discord.Role
	perms   discord.Permissions
	name    string
}

func (f *modFakeBot) GetSelfUserID() string { return f.selfID.String() }
func (f *modFakeBot) GetName() string       { return f.name }
func (f *modFakeBot) GetCachedGuild(string) *discord.Guild {
	return f.guild
}
func (f *modFakeBot) GetCachedMember(_, userID string) *discord.Member {
	if f.members == nil {
		return nil
	}
	return f.members[userID]
}
func (f *modFakeBot) GetCachedRole(_, roleID string) *discord.Role {
	if f.roles == nil {
		return nil
	}
	return f.roles[roleID]
}
func (f *modFakeBot) GetUserPermissions(userID, _ string) discord.Permissions {
	if userID == f.selfID.String() {
		return f.perms
	}
	return 0
}

type modSent struct {
	channelID snowflake.ID
	message   discord.MessageCreate
}

type modUpdate struct {
	guildID  snowflake.ID
	userID   snowflake.ID
	disabled *time.Time
}

type modFakeRest struct {
	rest.Rest
	mu       sync.Mutex
	sent     []modSent
	bans     []snowflake.ID
	unbans   []snowflake.ID
	removes  []snowflake.ID
	updates  []modUpdate
	userTags map[string]string
	sendErr  error
}

func (f *modFakeRest) CreateMessage(channelID snowflake.ID, message discord.MessageCreate, _ ...rest.RequestOpt) (*discord.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.sent = append(f.sent, modSent{channelID: channelID, message: message})
	return &discord.Message{ID: snowflake.ID(len(f.sent) + 100)}, nil
}

func (f *modFakeRest) AddBan(guildID, userID snowflake.ID, _ time.Duration, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bans = append(f.bans, userID)
	return nil
}

func (f *modFakeRest) DeleteBan(guildID, userID snowflake.ID, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unbans = append(f.unbans, userID)
	return nil
}

func (f *modFakeRest) RemoveMember(guildID, userID snowflake.ID, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removes = append(f.removes, userID)
	return nil
}

func (f *modFakeRest) UpdateMember(guildID, userID snowflake.ID, update discord.MemberUpdate, _ ...rest.RequestOpt) (*discord.Member, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, modUpdate{guildID: guildID, userID: userID, disabled: update.CommunicationDisabledUntil.Value})
	return &discord.Member{User: discord.User{ID: userID}}, nil
}

func (f *modFakeRest) GetUser(userID snowflake.ID, _ ...rest.RequestOpt) (*discord.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tag := ""
	if f.userTags != nil {
		tag = f.userTags[userID.String()]
	}
	if tag == "" {
		tag = "user-" + userID.String()
	}
	return &discord.User{ID: userID, Username: tag}, nil
}

func (f *modFakeRest) messages() []modSent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]modSent(nil), f.sent...)
}

// newModTestModule builds a module with stores in a temp dir and the standard
// fake hierarchy: bot (99, role 50) > moderator (10, role 40) > target (20, role 10).
func newModTestModule(t *testing.T) (*ModerationModule, *modFakeBot, *modFakeRest) {
	t.Helper()
	botID := snowflake.ID(99)
	bot := &modFakeBot{
		selfID:  botID,
		name:    "TestBot",
		guild:   &discord.Guild{ID: 1, Name: "Test Guild", OwnerID: 999},
		perms:   discord.PermissionBanMembers | discord.PermissionKickMembers | discord.PermissionModerateMembers,
		members: map[string]*discord.Member{},
		roles: map[string]*discord.Role{
			"50": {ID: 50, Position: 50},
			"40": {ID: 40, Position: 40},
			"10": {ID: 10, Position: 10},
		},
	}
	bot.members[botID.String()] = &discord.Member{User: discord.User{ID: botID, Username: "TestBot"}, RoleIDs: []snowflake.ID{50}}
	bot.members["10"] = &discord.Member{User: discord.User{ID: 10, Username: "Mod"}, RoleIDs: []snowflake.ID{40}}
	bot.members["20"] = &discord.Member{User: discord.User{ID: 20, Username: "Target"}, RoleIDs: []snowflake.ID{10}}
	restFake := &modFakeRest{userTags: map[string]string{"20": "Target#0001"}}
	m := New().(*ModerationModule)
	ctx := &modules.Context{
		DataDir: t.TempDir(),
		Logger:  testLogger{},
		Rest:    restFake,
		Bot:     bot,
	}
	if err := m.OnLoad(ctx); err != nil {
		t.Fatalf("OnLoad: %v", err)
	}
	return m, bot, restFake
}

func testModConfig() modules.ModerationConfig {
	return modules.ModerationConfig{
		Enabled:        true,
		AuditChannel:   "3",
		WarnChannel:    "2",
		WarnNotify:     true,
		WarnExpiryDays: 30,
	}
}
