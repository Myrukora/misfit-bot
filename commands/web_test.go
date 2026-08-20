package commands

import (
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
)

// TestCanExecuteWeb pins the web execution permission gate — the exact rules
// the dashboard's /api/exec enforces for every command.
func TestCanExecuteWeb(t *testing.T) {
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
			err := CanExecuteWeb(c.owner, c.elevated, c.guildOwner, c.userPerms,
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

// TestCanExecuteWebSuperOwnerMessage pins the wording the dashboard shows.
func TestCanExecuteWebSuperOwnerMessage(t *testing.T) {
	err := CanExecuteWeb(true, false, false, 0, true, true, 0)
	if err == nil || !strings.Contains(err.Error(), "cannot be executed from the web") {
		t.Fatalf("unexpected error: %v", err)
	}
}
