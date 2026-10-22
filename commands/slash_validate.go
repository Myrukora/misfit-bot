package commands

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
)

// ValidateSlashOptions checks a slash command's option tree against Discord's
// structural rules and returns every violation it finds.
//
// Discord rejects the ENTIRE SetGlobalCommands batch with 50035 when any one
// command is malformed, so a single bad tree silently unregisters every slash
// command on the bot (the failure is only visible as one log line). The rule
// this catches most often:
//
//	APPLICATION_COMMAND_OPTIONS_REQUIRED_INVALID — "Required options must be
//	placed before non-required options"
//
// Discord requires that, within any one option list, all required options
// precede all optional ones. A schema like
//
//	subCmd("create", ..., name(required), channel(optional), type(required))
//
// is therefore invalid even though it reads naturally.
//
// Also enforced: subcommands/subcommand-groups cannot be mixed with plain
// options in the same list, and a subcommand group may only contain
// subcommands.
func ValidateSlashOptions(opts []discord.ApplicationCommandOption) []string {
	return validateOptionList(opts, "")
}

// validateOptionList walks one option list. path is a human-readable prefix
// like "tickets > panel > create" used in the error messages.
func validateOptionList(opts []discord.ApplicationCommandOption, path string) []string {
	var errs []string
	seenOptional := ""
	var haveLeaf, haveSub, haveGroup bool

	for _, o := range opts {
		name := o.OptionName()
		here := name
		if path != "" {
			here = path + " > " + name
		}
		switch t := o.(type) {
		case discord.ApplicationCommandOptionSubCommandGroup:
			haveGroup = true
			errs = append(errs, validateSubGroup(t, here)...)
			continue
		case discord.ApplicationCommandOptionSubCommand:
			haveSub = true
			errs = append(errs, validateOptionList(t.Options, here)...)
			continue
		}

		haveLeaf = true
		if !slashOptionRequired(o) {
			if seenOptional == "" {
				seenOptional = name
			}
			continue
		}
		if seenOptional != "" {
			errs = append(errs, fmt.Sprintf(
				"%s: required option %q comes after optional option %q — Discord requires required options first (50035 APPLICATION_COMMAND_OPTIONS_REQUIRED_INVALID)",
				displayPath(path), name, seenOptional))
		}
	}

	if haveLeaf && (haveSub || haveGroup) {
		errs = append(errs, fmt.Sprintf(
			"%s: subcommands/subcommand groups cannot be mixed with plain options in the same list",
			displayPath(path)))
	}
	if haveSub && haveGroup {
		errs = append(errs, fmt.Sprintf(
			"%s: subcommands and subcommand groups cannot be mixed in the same list",
			displayPath(path)))
	}
	return errs
}

// validateSubGroup enforces that a subcommand group contains only subcommands
// (the disgo typed field already guarantees this; the recursion validates each
// subcommand's own option list).
func validateSubGroup(g discord.ApplicationCommandOptionSubCommandGroup, path string) []string {
	var errs []string
	for _, sub := range g.Options {
		errs = append(errs, validateOptionList(sub.Options, path+" > "+sub.Name)...)
	}
	return errs
}

// displayPath renders a root-level option path readably.
func displayPath(path string) string {
	if path == "" {
		return "(root)"
	}
	return strings.ReplaceAll(path, " > ", " > ")
}

// slashOptionRequired reports the Required flag of a leaf option. The disgo
// ApplicationCommandOption interface does not expose it, so this mirrors the
// type switch the dashboard uses.
func slashOptionRequired(o discord.ApplicationCommandOption) bool {
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
	case discord.ApplicationCommandOptionAttachment:
		return t.Required
	default:
		return false
	}
}
