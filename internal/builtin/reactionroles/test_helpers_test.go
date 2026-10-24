package reactionroles

import (
	"sync"
	"testing"

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

type rrFakeBot struct {
	commands.Interface
	selfID  snowflake.ID
	name    string
	members map[string]*discord.Member
	roles   map[string]*discord.Role
	perms   discord.Permissions
}

func (f *rrFakeBot) GetSelfUserID() string { return f.selfID.String() }
func (f *rrFakeBot) GetName() string       { return f.name }
func (f *rrFakeBot) GetCachedMember(_, userID string) *discord.Member {
	if f.members == nil {
		return nil
	}
	return f.members[userID]
}
func (f *rrFakeBot) GetCachedRole(_, roleID string) *discord.Role {
	if f.roles == nil {
		return nil
	}
	return f.roles[roleID]
}
func (f *rrFakeBot) GetUserPermissions(userID, _ string) discord.Permissions {
	if userID == f.selfID.String() {
		return f.perms
	}
	return 0
}
func (f *rrFakeBot) GetCachedGuild(string) *discord.Guild { return nil }

type rrSent struct {
	channelID snowflake.ID
	message   discord.MessageCreate
}

type rrReaction struct {
	messageID snowflake.ID
	emoji     string
}

type rrRoleChange struct {
	guildID snowflake.ID
	userID  snowflake.ID
	roleID  snowflake.ID
}

type rrFakeRest struct {
	rest.Rest
	mu          sync.Mutex
	nextMessage snowflake.ID
	sent        []rrSent
	updated     []discord.Embed
	deleted     []rrReaction
	addedReact  []rrReaction
	removedRct  []rrReaction
	roleAdds    []rrRoleChange
	roleRemoves []rrRoleChange
}

func (f *rrFakeRest) CreateMessage(channelID snowflake.ID, message discord.MessageCreate, _ ...rest.RequestOpt) (*discord.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextMessage++
	if f.nextMessage == 0 {
		f.nextMessage = 500
	}
	f.sent = append(f.sent, rrSent{channelID: channelID, message: message})
	return &discord.Message{ID: f.nextMessage}, nil
}

func (f *rrFakeRest) UpdateMessage(channelID, messageID snowflake.ID, update discord.MessageUpdate, _ ...rest.RequestOpt) (*discord.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if update.Embeds != nil && len(*update.Embeds) > 0 {
		f.updated = append(f.updated, (*update.Embeds)[0])
	}
	return &discord.Message{ID: messageID}, nil
}

func (f *rrFakeRest) DeleteMessage(channelID, messageID snowflake.ID, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, rrReaction{messageID: messageID})
	return nil
}

func (f *rrFakeRest) AddReaction(_ snowflake.ID, messageID snowflake.ID, emoji string, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addedReact = append(f.addedReact, rrReaction{messageID: messageID, emoji: emoji})
	return nil
}

func (f *rrFakeRest) RemoveOwnReaction(_ snowflake.ID, messageID snowflake.ID, emoji string, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removedRct = append(f.removedRct, rrReaction{messageID: messageID, emoji: emoji})
	return nil
}

func (f *rrFakeRest) AddMemberRole(guildID, userID, roleID snowflake.ID, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roleAdds = append(f.roleAdds, rrRoleChange{guildID: guildID, userID: userID, roleID: roleID})
	return nil
}

func (f *rrFakeRest) RemoveMemberRole(guildID, userID, roleID snowflake.ID, _ ...rest.RequestOpt) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.roleRemoves = append(f.roleRemoves, rrRoleChange{guildID: guildID, userID: userID, roleID: roleID})
	return nil
}

func (f *rrFakeRest) GetRole(guildID, roleID snowflake.ID, _ ...rest.RequestOpt) (*discord.Role, error) {
	return &discord.Role{ID: roleID, GuildID: guildID}, nil
}

func (f *rrFakeRest) GetMember(guildID, userID snowflake.ID, _ ...rest.RequestOpt) (*discord.Member, error) {
	return &discord.Member{User: discord.User{ID: userID}}, nil
}

func (f *rrFakeRest) reactions() []rrReaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rrReaction(nil), f.addedReact...)
}

func (f *rrFakeRest) sentMessages() []rrSent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rrSent(nil), f.sent...)
}

func (f *rrFakeRest) roleGrants() []rrRoleChange {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rrRoleChange(nil), f.roleAdds...)
}

func (f *rrFakeRest) roleRemovals() []rrRoleChange {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rrRoleChange(nil), f.roleRemoves...)
}

func newRRTestModule(t *testing.T) (*ReactionRolesModule, *rrFakeBot, *rrFakeRest) {
	t.Helper()
	botID := snowflake.ID(99)
	bot := &rrFakeBot{
		selfID: botID,
		name:   "TestBot",
		perms:  discord.PermissionManageRoles,
		members: map[string]*discord.Member{
			"99": {User: discord.User{ID: botID}, RoleIDs: []snowflake.ID{50}},
			"10": {User: discord.User{ID: 10, Username: "member"}, RoleIDs: []snowflake.ID{10}},
		},
		roles: map[string]*discord.Role{
			"50": {ID: 50, Position: 50, Name: "Bot"},
			"20": {ID: 20, Position: 20, Name: "Verified"},
			"10": {ID: 10, Position: 10, Name: "Member"},
			"60": {ID: 60, Position: 60, Name: "Above bot"},
		},
	}
	restFake := &rrFakeRest{nextMessage: 499}
	m := New().(*ReactionRolesModule)
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

func validPanel() modules.ReactionPanel {
	return modules.ReactionPanel{
		Name:            "Pick roles",
		Enabled:         true,
		ChannelID:       "2",
		RemoveOnUnreact: true,
		Embed: modules.ReactionRoleEmbed{
			Title:       "Pick your roles",
			Description: "React to get a role.",
			Color:       "#5865f2",
		},
		Mappings: []modules.ReactionRoleMapping{{Emoji: "👍", RoleID: "20"}},
	}
}
