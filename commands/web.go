package commands

import (
	"errors"

	"github.com/disgoorg/disgo/discord"
)

// CanExecuteWeb is the pure permission gate for web (dashboard) command
// execution. It mirrors the Discord dispatcher's checks exactly:
//
//   - SuperOwnerOnly commands are ALWAYS denied from the web (eval executes
//     `sh -c` and must never be web-reachable)
//   - owner/elevated bypass everything
//   - OwnerOnly blocks everyone else
//   - the guild owner bypasses RequiredPerm
//   - otherwise RequiredPerm is checked against the user's perms
//
// The caller computes owner/elevated/guildOwner from the requesting web
// session (not from Discord message context).
func CanExecuteWeb(owner, elevated, guildOwner bool, userPerms discord.Permissions, superOwnerOnly, ownerOnly bool, requiredPerm discord.Permissions) error {
	if superOwnerOnly {
		return errors.New("command cannot be executed from the web")
	}
	if owner || elevated {
		return nil
	}
	if ownerOnly {
		return errors.New("insufficient permissions")
	}
	if guildOwner {
		return nil
	}
	if requiredPerm == 0 {
		return nil
	}
	if !userPerms.Has(requiredPerm) {
		return errors.New("insufficient permissions")
	}
	return nil
}
