package dashboard

import (
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
)

// gatedTestBot answers the identity/capability calls the ACL makes.
type gatedTestBot struct {
	commands.Interface
	owner, elevated string
	member          bool
}

func (b *gatedTestBot) IsOwner(id string) bool    { return id == b.owner }
func (b *gatedTestBot) IsElevated(id string) bool { return id == b.elevated }
func (b *gatedTestBot) MemberIntentEnabled() bool { return b.member }

// TestGatedModeRestrictsDashboardToOwnerAndElevated pins the policy: without
// the Server Members intent only the owner and elevated users get in, even
// when OAuth reports manageable guilds for everyone else.
func TestGatedModeRestrictsDashboardToOwnerAndElevated(t *testing.T) {
	m := &DashboardModule{bot: &gatedTestBot{owner: "1", elevated: "2", member: false}}

	if got := m.resolveLevel(&userSession{userID: snowflake.ID(1)}); got != lvlOwner {
		t.Errorf("owner level = %q, want %q", got, lvlOwner)
	}
	if got := m.resolveLevel(&userSession{userID: snowflake.ID(2)}); got != lvlElevated {
		t.Errorf("elevated level = %q, want %q", got, lvlElevated)
	}
	if got := m.resolveLevel(&userSession{userID: snowflake.ID(3)}); got != lvlRegular {
		t.Errorf("regular user level = %q, want %q (gated)", got, lvlRegular)
	}
	if ids := m.mutualGuildIDs(&userSession{userID: snowflake.ID(3)}); len(ids) != 0 {
		t.Errorf("gated regular user sees %v, want no guilds", ids)
	}
	if m.canManageGuild(&userSession{userID: snowflake.ID(3)}, "10") {
		t.Error("gated regular user must not manage any guild")
	}
}

// TestGatedModeKeepsOwnerAccess pins that the gate does not lock the owner out.
func TestGatedModeKeepsOwnerAccess(t *testing.T) {
	m := &DashboardModule{bot: &gatedTestBot{owner: "1", member: false}}
	us := &userSession{userID: snowflake.ID(1)}
	if got := m.resolveLevel(us); got != lvlOwner {
		t.Fatalf("owner level = %q, want %q", got, lvlOwner)
	}
	// mutualGuildIDs must not short-circuit the owner (it still filters by the
	// OAuth list, which is empty in this fixture).
	if ids := m.mutualGuildIDs(us); len(ids) != 0 {
		t.Errorf("unexpected guilds for owner fixture: %v", ids)
	}
}

// TestUngatedModeUsesOAuthTiers pins the normal path is unchanged.
func TestUngatedModeUsesOAuthTiers(t *testing.T) {
	m := &DashboardModule{bot: &gatedTestBot{owner: "1", member: true}}
	if got := m.resolveLevel(&userSession{userID: snowflake.ID(1)}); got != lvlOwner {
		t.Fatalf("owner level = %q, want %q", got, lvlOwner)
	}
}
