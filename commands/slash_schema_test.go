package commands

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
)

// findSlashOpts returns the options of the core slash command with the given
// name (auto-generated twins from registerCoreSlashCommands).
func findSlashOpts(t *testing.T, name string) []discord.ApplicationCommandOption {
	t.Helper()
	for _, sc := range CoreSlashCommands {
		if sc.Name == name {
			return sc.Options
		}
	}
	t.Fatalf("core slash command %q not registered", name)
	return nil
}

// subOptByName returns the subcommand option with the given name.
func subOptByName(t *testing.T, opts []discord.ApplicationCommandOption, name string) discord.ApplicationCommandOptionSubCommand {
	t.Helper()
	for _, o := range opts {
		if s, ok := o.(discord.ApplicationCommandOptionSubCommand); ok && s.Name == name {
			return s
		}
	}
	t.Fatalf("subcommand %q not found", name)
	return discord.ApplicationCommandOptionSubCommand{}
}

// TestCoreSlashHelpOptional pins /help's optional command argument.
func TestCoreSlashHelpOptional(t *testing.T) {
	opts := findSlashOpts(t, "help")
	if len(opts) != 1 || opts[0].OptionName() != "command" {
		t.Fatalf("help options = %v, want [command]", opts)
	}
	if optReq(opts[0]) {
		t.Fatal("help command must be optional")
	}
}

// optReq extracts the Required flag from a typed option (mirrors the
// dashboard's optionRequired helper; kept local to avoid a cross-package dep).
func optReq(o discord.ApplicationCommandOption) bool {
	switch t := o.(type) {
	case discord.ApplicationCommandOptionString:
		return t.Required
	case discord.ApplicationCommandOptionInt:
		return t.Required
	case discord.ApplicationCommandOptionFloat:
		return t.Required
	case discord.ApplicationCommandOptionBool:
		return t.Required
	case discord.ApplicationCommandOptionChannel:
		return t.Required
	case discord.ApplicationCommandOptionRole:
		return t.Required
	case discord.ApplicationCommandOptionUser:
		return t.Required
	case discord.ApplicationCommandOptionMentionable:
		return t.Required
	default:
		return false
	}
}
