package commands

import (
	"errors"

	"github.com/disgoorg/disgo/discord"
)

// ErrRemoteForbidden reports a command that can never run outside Discord
// (SuperOwnerOnly commands such as eval).
var ErrRemoteForbidden = errors.New("command cannot be executed remotely")

// ErrInsufficientPerm reports a caller failing the OwnerOnly / RequiredPerm
// gate for remote execution.
var ErrInsufficientPerm = errors.New("insufficient permissions")

// ErrCommandDisabled reports a command disabled or restricted for the target
// guild by the per-command override store. Both dispatchers (Discord and MCP)
// must enforce this after the permission gate.
var ErrCommandDisabled = errors.New("command is disabled or restricted in this server")

// CanExecuteRemote is the pure permission gate for non-Discord command
// execution (the MCP server). It mirrors the Discord dispatcher's checks:
//
//   - SuperOwnerOnly commands are ALWAYS denied remotely (eval executes
//     `sh -c` and must never be reachable outside Discord)
//   - owner/elevated bypass everything
//   - OwnerOnly blocks everyone else
//   - the guild owner bypasses RequiredPerm
//   - otherwise RequiredPerm is checked against the user's perms
//
// The caller computes owner/elevated/guildOwner from the authenticated
// control-plane identity, not from a Discord interaction.
func CanExecuteRemote(owner, elevated, guildOwner bool, userPerms discord.Permissions, superOwnerOnly, ownerOnly bool, requiredPerm discord.Permissions) error {
	if superOwnerOnly {
		return ErrRemoteForbidden
	}
	if owner || elevated {
		return nil
	}
	if ownerOnly {
		return ErrInsufficientPerm
	}
	if guildOwner {
		return nil
	}
	if requiredPerm == 0 {
		return nil
	}
	if !userPerms.Has(requiredPerm) {
		return ErrInsufficientPerm
	}
	return nil
}
