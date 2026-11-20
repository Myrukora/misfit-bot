package dashboard

import (
	"errors"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
)

// roleTestBot answers the identity calls checkRoleGrant makes.
type roleTestBot struct {
	commands.Interface
	owner      string
	guildOwner string
}

func (b *roleTestBot) IsOwner(id string) bool        { return id == b.owner }
func (b *roleTestBot) GetGuildOwnerID(string) string { return b.guildOwner }
func (b *roleTestBot) IsElevated(string) bool        { return false }
func (b *roleTestBot) MemberIntentEnabled() bool     { return true }

// roleRestStub serves the two REST lookups the fail-closed path uses and counts
// calls so tests can prove a cache hit does not hit the network.
type roleRestStub struct {
	rest.Rest

	member    *discord.Member
	memberErr error
	roles     []discord.Role
	rolesErr  error

	memberCalls int
	rolesCalls  int
}

func (s *roleRestStub) GetMember(guildID, userID snowflake.ID, _ ...rest.RequestOpt) (*discord.Member, error) {
	s.memberCalls++
	return s.member, s.memberErr
}

func (s *roleRestStub) GetRoles(guildID snowflake.ID, _ ...rest.RequestOpt) ([]discord.Role, error) {
	s.rolesCalls++
	return s.roles, s.rolesErr
}

func roleTestModule(b commands.Interface, stub *roleRestStub) *DashboardModule {
	return &DashboardModule{
		bot:    b,
		client: &bot.Client{Caches: cache.New(), Rest: stub},
	}
}

// A cache miss must not allow the grant: the hierarchy is resolved over REST
// and compared. Here the staff member outranks the target role, so it passes.
func TestCheckRoleGrantResolvesCacheMissOverREST(t *testing.T) {
	stub := &roleRestStub{
		member: &discord.Member{RoleIDs: []snowflake.ID{10}},
		roles: []discord.Role{
			{ID: 10, Position: 10},
			{ID: 20, Position: 5},
		},
	}
	m := roleTestModule(&roleTestBot{}, stub)
	us := &userSession{userID: snowflake.ID(2)}

	if err := m.checkRoleGrant(us, "100", "20"); err != nil {
		t.Fatalf("grant below the caller's top role should pass: %v", err)
	}
	if stub.memberCalls != 1 || stub.rolesCalls != 1 {
		t.Fatalf("expected one member + one role REST lookup, got %d/%d", stub.memberCalls, stub.rolesCalls)
	}
}

// The regression this fixes: an uncached member used to return nil (allow).
// Now the REST comparison still rejects a role at or above their highest.
func TestCheckRoleGrantCacheMissStillRejectsHigherRole(t *testing.T) {
	stub := &roleRestStub{
		member: &discord.Member{RoleIDs: []snowflake.ID{10}},
		roles: []discord.Role{
			{ID: 10, Position: 1},
			{ID: 20, Position: 50},
		},
	}
	m := roleTestModule(&roleTestBot{}, stub)
	err := m.checkRoleGrant(&userSession{userID: snowflake.ID(2)}, "100", "20")
	if err == nil || !strings.Contains(err.Error(), "at or above") {
		t.Fatalf("expected hierarchy rejection, got %v", err)
	}
}

// An unavailable member lookup must deny, not fall through.
func TestCheckRoleGrantMemberLookupFailureDenies(t *testing.T) {
	stub := &roleRestStub{
		memberErr: errors.New("discord unavailable"),
		roles:     []discord.Role{{ID: 20, Position: 5}},
	}
	m := roleTestModule(&roleTestBot{}, stub)
	err := m.checkRoleGrant(&userSession{userID: snowflake.ID(2)}, "100", "20")
	if err == nil {
		t.Fatal("missing member data must fail closed")
	}
}

// An unavailable role lookup must deny too.
func TestCheckRoleGrantRoleLookupFailureDenies(t *testing.T) {
	stub := &roleRestStub{
		member:   &discord.Member{RoleIDs: []snowflake.ID{10}},
		rolesErr: errors.New("discord unavailable"),
	}
	m := roleTestModule(&roleTestBot{}, stub)
	err := m.checkRoleGrant(&userSession{userID: snowflake.ID(2)}, "100", "20")
	if err == nil {
		t.Fatal("missing role data must fail closed")
	}
}

// Owner and guild-owner bypasses must survive a dead REST client.
func TestCheckRoleGrantOwnerBypasses(t *testing.T) {
	stub := &roleRestStub{memberErr: errors.New("discord unavailable"), rolesErr: errors.New("discord unavailable")}

	m := roleTestModule(&roleTestBot{owner: "2"}, stub)
	if err := m.checkRoleGrant(&userSession{userID: snowflake.ID(2)}, "100", "20"); err != nil {
		t.Fatalf("bot owner should bypass: %v", err)
	}

	m = roleTestModule(&roleTestBot{guildOwner: "3"}, stub)
	if err := m.checkRoleGrant(&userSession{userID: snowflake.ID(3)}, "100", "20"); err != nil {
		t.Fatalf("guild owner should bypass: %v", err)
	}
}
