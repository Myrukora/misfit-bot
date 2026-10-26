package commands

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/internal/util"
	"github.com/misfit/bot/updater"
)

var startTime = time.Now()

// StartTime returns the time the bot core package was initialized (process
// start). Used by the dashboard for an honest bot-uptime source.
func StartTime() time.Time { return startTime }

// short7 abbreviates a commit SHA to 7 characters for display.
func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// plural marks an English count as plural ("1 commit" / "3 commits").
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// valueOrDash renders an embed field value, substituting an em-dash for an
// empty string so a missing value reads as "none" instead of a blank.
func valueOrDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "—"
	}
	return v
}

// displayVersion renders a build version for an embed. Release versions get the
// conventional "v" prefix; an unstamped build says so instead of pretending to
// be a version (see VERSION + the -ldflags stamp in README → Versioning).
func displayVersion(v string) string {
	switch v {
	case "":
		return "unknown"
	case "dev":
		return "dev (unstamped build)"
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

func title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func init() {
	RegisterCoreSlash(SlashCommand{
		Name:        "ping",
		Description: "Check how quickly the bot responds, with its gateway and API latency.",
		Usage:       "ping",
		Category:    "general",
		Execute: func(ctx *Context) error {
			start := time.Now()
			latency := ctx.Bot.GetLatency()
			apiMs := time.Since(start).Round(time.Millisecond)
			e := embed.Info("Pong!", fmt.Sprintf("**Gateway:** %s\n**API:** %s", latency, apiMs))
			return ctx.Respond(e)
		},
	})

	RegisterCoreSlash(SlashCommand{
		Name:        "uptime",
		Description: "See how long the bot has been running.",
		Usage:       "uptime",
		Category:    "general",
		Execute: func(ctx *Context) error {
			d := time.Since(startTime).Round(time.Second)
			return ctx.Respond(embed.Info("Uptime", fmt.Sprintf("Running for %s", d)))
		},
	})

	RegisterCoreSlash(SlashCommand{
		Name:        "info",
		Description: "Show the bot's name, version, author, and where it runs.",
		Usage:       "info",
		Category:    "general",
		Execute: func(ctx *Context) error {
			fields := []discord.EmbedField{
				{Name: "Version", Value: displayVersion(ctx.Bot.GetVersion()), Inline: util.PtrBool(true)},
				{Name: "Creator", Value: fmt.Sprintf("<@%s>", ctx.Bot.GetOwnerID()), Inline: util.PtrBool(true)},
				{Name: "Go Version", Value: runtime.Version(), Inline: util.PtrBool(true)},
				{Name: "OS/Arch", Value: fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH), Inline: util.PtrBool(true)},
			}

			// The updater caches the newest release tag it has seen on the tracked
			// branch, so a pending release is visible right next to the version —
			// no extra network call here, and nothing shown until there is one.
			if upd, ok := ctx.Bot.GetUpdater().(*updater.Manager); ok && upd != nil {
				if latest := upd.LatestVersion(); updater.Ahead(ctx.Bot.GetVersion(), latest) {
					fields = append(fields, discord.EmbedField{
						Name: "Update Available", Value: "v" + latest, Inline: util.PtrBool(true),
					})
				}
			}

			if ctx.Bot.GetToS() != "" {
				fields = append(fields, discord.EmbedField{
					Name:   "Terms of Service",
					Value:  fmt.Sprintf("[Link](%s)", ctx.Bot.GetToS()),
					Inline: util.PtrBool(true),
				})
			}
			if ctx.Bot.GetPrivacy() != "" {
				fields = append(fields, discord.EmbedField{
					Name:   "Privacy Policy",
					Value:  fmt.Sprintf("[Link](%s)", ctx.Bot.GetPrivacy()),
					Inline: util.PtrBool(true),
				})
			}

			e := embed.New().
				WithTitle(ctx.Bot.GetName()).
				WithDescription("Community bot for this server. See /help for commands.").
				WithColor(embed.ColorPurple).
				WithFields(fields...).
				WithTimestamp(time.Now())

			return ctx.Respond(e)
		},
	})

	RegisterCoreSlash(SlashCommand{
		Name:        "help",
		Description: "List the commands you can use, grouped by category.",
		Usage:       "help [command]",
		Category:    "core",
		Options: []discord.ApplicationCommandOption{
			strOpt("command", "Command name to show details for", false),
		},
		Execute: func(ctx *Context) error {
			userID := ctx.Author.ID.String()
			userPerms := ctx.Bot.GetUserPermissions(userID, ctx.GuildID)
			guildOwnerID := ctx.Bot.GetGuildOwnerID(ctx.GuildID)

			canUse := func(cmd SlashCommand) bool {
				if cmd.SuperOwnerOnly && !ctx.Bot.IsOwner(userID) {
					return false
				}
				if ov := ctx.Bot.CommandOverrides(); ov != nil && ov.IsDisabled(cmd.Name, ctx.GuildID) {
					return false
				}
				return ctx.Bot.CanUse(userID, userPerms, cmd.RequiredPerm, cmd.OwnerOnly, guildOwnerID)
			}
			moduleGroups := ctx.Bot.GetAllModuleSlashCommandsByModule()

			// Single command help
			if len(ctx.Args) > 0 {
				cmdName := strings.ToLower(ctx.Args[0])
				allCmds := append([]SlashCommand{}, CoreSlashCommands...)
				for _, mc := range moduleGroups {
					allCmds = append(allCmds, mc.Commands...)
				}
				for _, cmd := range allCmds {
					if cmd.Name != cmdName {
						continue
					}
					if ov := ctx.Bot.CommandOverrides(); ov != nil && ov.IsDisabled(cmd.Name, ctx.GuildID) {
						continue
					}
					if !canUse(cmd) {
						return ctx.Respond(embed.Error("Permission Denied", fmt.Sprintf("You don't have permission to use `/%s`.", ctx.Args[0])))
					}
					usage := cmd.Usage
					if usage == "" {
						usage = cmd.Name
					}
					fields := []discord.EmbedField{{
						Name:   "Usage",
						Value:  "`/" + usage + "`",
						Inline: util.PtrBool(false),
					}}
					if cmd.Category != "" {
						fields = append(fields, discord.EmbedField{
							Name:   "Category",
							Value:  cmd.Category,
							Inline: util.PtrBool(true),
						})
					}
					e := embed.New().
						WithTitle(cmd.Name).
						WithDescription(cmd.Description).
						WithFields(fields...).
						WithColor(embed.ColorInfo).
						WithTimestamp(time.Now())
					return ctx.Respond(e)
				}
				return ctx.Respond(embed.Error("Not Found", fmt.Sprintf("Command `/%s` not found.", ctx.Args[0])))
			}

			// Group all commands by category
			categories := make(map[string][]SlashCommand)
			for _, cmd := range CoreSlashCommands {
				if !canUse(cmd) {
					continue
				}
				cat := cmd.Category
				if cat == "" {
					cat = "core"
				}
				categories[cat] = append(categories[cat], cmd)
			}
			// Module commands are grouped under a category named after the owning
			// module (e.g. cleanup's commands appear under "Cleanup"), regardless
			// of the Category field the module author set.
			for _, mc := range moduleGroups {
				cat := mc.Name
				if cat == "" {
					cat = "modules"
				}
				for _, cmd := range mc.Commands {
					if !canUse(cmd) {
						continue
					}
					categories[cat] = append(categories[cat], cmd)
				}
			}

			// Build embed
			e := embed.New().
				WithTitle(fmt.Sprintf("%s Help", ctx.Bot.GetName())).
				WithColor(embed.ColorInfo).
				WithTimestamp(time.Now())

			var fields []discord.EmbedField
			// Sort categories: core first, then alphabetically
			var catNames []string
			for cat := range categories {
				catNames = append(catNames, cat)
			}
			sort.Strings(catNames)
			// Move "core" to front
			for i, cat := range catNames {
				if cat == "core" {
					catNames = append([]string{"core"}, append(catNames[:i], catNames[i+1:]...)...)
					break
				}
			}

			for _, cat := range catNames {
				cmds := categories[cat]
				var lines []string
				for _, cmd := range cmds {
					lines = append(lines, fmt.Sprintf("`/%s`: %s", cmd.Name, cmd.Description))
				}
				fields = append(fields, discord.EmbedField{
					Name:   fmt.Sprintf("▸ %s", title(cat)),
					Value:  strings.Join(lines, "\n"),
					Inline: util.PtrBool(false),
				})
			}

			e = e.WithFields(fields...)
			return ctx.Respond(e)
		},
	})
}

func strOpt(name, desc string, required bool) discord.ApplicationCommandOptionString {
	return discord.ApplicationCommandOptionString{
		Name:        name,
		Description: desc,
		Required:    required,
	}
}

// strOptChoices builds a string option restricted to the given choices
// (dropdown in Discord's UI and in the dashboard's command runner).
func strOptChoices(name, desc string, required bool, choices ...string) discord.ApplicationCommandOptionString {
	opts := strOpt(name, desc, required)
	for _, c := range choices {
		opts.Choices = append(opts.Choices, discord.ApplicationCommandOptionChoiceString{Name: c, Value: c})
	}
	return opts
}

// userOpt builds a USER-type option (member picker in the UI).
func userOpt(name, desc string, required bool) discord.ApplicationCommandOptionUser {
	return discord.ApplicationCommandOptionUser{
		Name:        name,
		Description: desc,
		Required:    required,
	}
}

// boolOpt builds a BOOLEAN option (yes/no switch in the UI).
func boolOpt(name, desc string, required bool) discord.ApplicationCommandOptionBool {
	return discord.ApplicationCommandOptionBool{
		Name:        name,
		Description: desc,
		Required:    required,
	}
}
