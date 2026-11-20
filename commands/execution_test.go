package commands

import (
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
)

// TestCanExecuteRemote pins the non-Discord execution permission gate — the
// exact rules the MCP dispatcher enforces for every command.
func TestCanExecuteRemote(t *testing.T) {
	const admin = discord.PermissionAdministrator

	cases := []struct {
		name                        string
		owner, elevated, guildOwner bool
		userPerms                   discord.Permissions
		superOwnerOnly, ownerOnly   bool
		requiredPerm                discord.Permissions
		wantErr, wantInsufficient   bool
	}{
		{"secret is never web-reachable", false, false, false, admin, true, true, admin, true, false},
		{"secret blocked even for owner", true, false, false, admin, true, true, admin, true, false},
		{"owner bypasses OwnerOnly", true, false, false, 0, false, true, 0, false, false},
		{"elevated bypasses OwnerOnly", false, true, false, 0, false, true, 0, false, false},
		{"normal user blocked by OwnerOnly", false, false, false, admin, false, true, 0, true, true},
		{"guild owner does NOT bypass OwnerOnly", false, false, true, admin, false, true, 0, true, true},
		{"owner bypasses RequiredPerm", true, false, false, 0, false, false, admin, false, false},
		{"guild owner bypasses RequiredPerm", false, false, true, 0, false, false, admin, false, false},
		{"normal user with perm passes", false, false, false, admin, false, false, admin, false, false},
		{"normal user without perm blocked", false, false, false, 0, false, false, admin, true, true},
		{"no required perm allows everyone", false, false, false, 0, false, false, 0, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CanExecuteRemote(c.owner, c.elevated, c.guildOwner, c.userPerms,
				c.superOwnerOnly, c.ownerOnly, c.requiredPerm)
			if c.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				if c.wantInsufficient && !strings.Contains(err.Error(), "insufficient permissions") {
					t.Fatalf("want insufficient-permissions error, got %q", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}

// TestCanExecuteRemoteSuperOwnerMessage pins the wording shown for a command
// that can never run outside Discord.
func TestCanExecuteRemoteSuperOwnerMessage(t *testing.T) {
	err := CanExecuteRemote(true, false, false, 0, true, true, 0)
	if err == nil || !strings.Contains(err.Error(), "cannot be executed remotely") {
		t.Fatalf("unexpected error: %v", err)
	}
}
