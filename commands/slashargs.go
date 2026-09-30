package commands

import (
	"strconv"

	"github.com/disgoorg/disgo/discord"
)

// SlashArgs builds the positional argument vector a prefix user typing the
// same command would produce, by walking the command's declared options in
// order and reading the interaction's values. It is the single source of
// truth for slash→prefix arg mapping: the dispatcher calls it once per
// interaction, and the command's Execute sees exactly the prefix tree's
// expected vector (group name, sub name, then leaf values in declared order).
//
// Options absent from the interaction are skipped, so an omitted optional
// argument shifts nothing — the prefix tree's arity checks see exactly what a
// prefix user typing the same command would produce. The returned slice is in
// DECLARED option order, never the interaction's map iteration order.
func SlashArgs(cmd SlashCommand, data discord.SlashCommandInteractionData) []string {
	var args []string
	for _, opt := range cmd.Options {
		switch o := opt.(type) {
		case discord.ApplicationCommandOptionSubCommandGroup:
			// Only emit this group if the interaction invoked it.
			if data.SubCommandGroupName == nil || o.Name != *data.SubCommandGroupName {
				continue
			}
			args = append(args, o.Name)
			if data.SubCommandName == nil {
				continue
			}
			subName := *data.SubCommandName
			for _, sub := range o.Options {
				if sub.OptionName() != subName {
					continue
				}
				args = append(args, subName)
				for _, leaf := range sub.Options {
					appendLeaf(&args, leaf, data)
				}
				break
			}
		case discord.ApplicationCommandOptionSubCommand:
			// Only emit this flat subcommand if the interaction invoked it
			// (and no group was invoked — leaf names repeat across groups).
			if data.SubCommandGroupName != nil || data.SubCommandName == nil || o.Name != *data.SubCommandName {
				continue
			}
			args = append(args, o.Name)
			for _, leaf := range o.Options {
				appendLeaf(&args, leaf, data)
			}
		default:
			// Top-level leaf option: only when no subcommand was invoked.
			if data.SubCommandName == nil {
				appendLeaf(&args, opt, data)
			}
		}
	}
	return args
}

// appendLeaf appends a leaf option's value (stringified by type) to args if
// the option is present in the interaction. Absent options are skipped.
func appendLeaf(args *[]string, opt discord.ApplicationCommandOption, data discord.SlashCommandInteractionData) {
	val, ok := data.Options[opt.OptionName()]
	if !ok {
		return
	}
	*args = append(*args, stringifyOption(val))
}

// stringifyOption renders a slash option value as the string the prefix tree
// expects, by declared type. It never panics: an unrecognised type yields "".
func stringifyOption(o discord.SlashCommandOption) string {
	switch o.Type {
	case discord.ApplicationCommandOptionTypeString:
		return o.String()
	case discord.ApplicationCommandOptionTypeInt:
		return strconv.Itoa(o.Int())
	case discord.ApplicationCommandOptionTypeBool:
		return strconv.FormatBool(o.Bool())
	case discord.ApplicationCommandOptionTypeFloat:
		return strconv.FormatFloat(o.Float(), 'f', -1, 64)
	case discord.ApplicationCommandOptionTypeUser,
		discord.ApplicationCommandOptionTypeChannel,
		discord.ApplicationCommandOptionTypeRole,
		discord.ApplicationCommandOptionTypeMentionable,
		discord.ApplicationCommandOptionTypeAttachment:
		return o.Snowflake().String()
	default:
		return ""
	}
}
