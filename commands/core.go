package commands

import (
	"context"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/internal/util"
	"github.com/misfit/bot/updater"
)

var mentionRegex = regexp.MustCompile(`^<@!?(\d+)>$`)

var startTime = time.Now()

// StartTime returns the time the bot core package was initialized (process
// start). Used by the dashboard for an honest bot-uptime source.
func StartTime() time.Time { return startTime }

func fmtErr(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

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
	RegisterCoreCommand(Command{
		Name:        "ping",
		Description: "Check how quickly the bot responds, with its gateway and API latency.",
		Usage:       "ping",
		Category:    "general",
		Execute: func(ctx *Context) error {
			start := time.Now()
			latency := ctx.Bot.GetLatency()
			apiMs := time.Since(start).Round(time.Millisecond)
			e := embed.Info("🏓 Pong!", fmt.Sprintf("**Gateway:** %s\n**API:** %s", latency, apiMs))
			return ctx.Respond(e)
		},
	})

	RegisterCoreCommand(Command{
		Name:        "uptime",
		Description: "See how long the bot has been running.",
		Usage:       "uptime",
		Category:    "general",
		Execute: func(ctx *Context) error {
			d := time.Since(startTime).Round(time.Second)
			return ctx.Respond(embed.Info("⏱️ Uptime", fmt.Sprintf("Running for %s", d)))
		},
	})

	RegisterCoreCommand(Command{
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
				WithTitle(fmt.Sprintf("🤖 %s", ctx.Bot.GetName())).
				WithDescription("A custom Discord bot with modular architecture.").
				WithColor(embed.ColorPurple).
				WithFields(fields...).
				WithTimestamp(time.Now())

			return ctx.Respond(e)
		},
	})

	RegisterCoreCommand(Command{
		Name:        "help",
		Description: "List the commands you can use, grouped by category.",
		Usage:       "help [command]",
		Category:    "core",
		Execute: func(ctx *Context) error {
			p := ctx.Bot.GetPrefix()
			userID := ctx.Author.ID.String()
			userPerms := ctx.Bot.GetUserPermissions(userID, ctx.GuildID)
			guildOwnerID := ctx.Bot.GetGuildOwnerID(ctx.GuildID)

			canUse := func(cmd Command) bool {
				if cmd.SuperOwnerOnly && !ctx.Bot.IsOwner(userID) {
					return false
				}
				if ov := ctx.Bot.CommandOverrides(); ov != nil && ov.IsDisabled(cmd.Name, ctx.GuildID) {
					return false
				}
				return ctx.Bot.CanUse(userID, userPerms, cmd.RequiredPerm, cmd.OwnerOnly, guildOwnerID)
			}

			// Single command help
			if len(ctx.Args) > 0 {
				cmdName := strings.ToLower(ctx.Args[0])
				allCmds := make([]Command, len(CoreCommands), len(CoreCommands)+len(ctx.Bot.GetAllModuleCommands()))
				copy(allCmds, CoreCommands)
				allCmds = append(allCmds, ctx.Bot.GetAllModuleCommands()...)
				for _, cmd := range allCmds {
					if cmd.Name == cmdName || util.ContainsStr(cmd.Aliases, cmdName) {
						if ov := ctx.Bot.CommandOverrides(); ov != nil && ov.IsDisabled(cmd.Name, ctx.GuildID) {
							continue
						}
						if !canUse(cmd) {
							return ctx.Respond(embed.Error("🚫 Permission Denied", fmt.Sprintf("You don't have permission to use `%s`.", ctx.Args[0])))
						}
						var fields []discord.EmbedField
						fields = append(fields, discord.EmbedField{
							Name:   "Usage",
							Value:  fmt.Sprintf("`%s%s`", p, cmd.Usage),
							Inline: util.PtrBool(false),
						})
						if cmd.Category != "" {
							fields = append(fields, discord.EmbedField{
								Name:   "Category",
								Value:  cmd.Category,
								Inline: util.PtrBool(true),
							})
						}
						e := embed.New().
							WithTitle(fmt.Sprintf("📖 %s", cmd.Name)).
							WithDescription(cmd.Description).
							WithFields(fields...).
							WithColor(embed.ColorInfo).
							WithTimestamp(time.Now())
						return ctx.Respond(e)
					}
				}
				return ctx.Respond(embed.Error("❌ Not Found", fmt.Sprintf("Command `%s` not found.", ctx.Args[0])))
			}

			// Group all commands by category
			categories := make(map[string][]Command)
			for _, cmd := range CoreCommands {
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
			// module (e.g. cleanup's commands appear under "Cleanup"). This keeps each
			// module's commands together regardless of the Category field the module
			// author set on individual commands.
			for _, mc := range ctx.Bot.GetAllModuleCommandsByModule() {
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
				WithTitle(fmt.Sprintf("📖 %s Help", ctx.Bot.GetName())).
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
					lines = append(lines, fmt.Sprintf("`%s%s` — %s", p, cmd.Name, cmd.Description))
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

	RegisterCoreCommand(Command{
		Name:        "modules",
		Description: "List modules, or enable/disable a compiled-in feature module (applies after restart).",
		Usage:       "modules | modules enable <name> | modules disable <name>",
		Category:    "modules",
		OwnerOnly:   true,
		Execute: func(ctx *Context) error {
			// enable/disable subcommands for compiled-in feature modules.
			if len(ctx.Args) > 0 {
				switch strings.ToLower(ctx.Args[0]) {
				case "enable", "disable":
					if len(ctx.Args) < 2 {
						return ctx.Respond(embed.Warning("⚠️ Usage", "modules enable <name> | modules disable <name>"))
					}
					name := ctx.Args[1]
					if !ctx.Bot.IsBuiltinModule(name) {
						return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("`%s` is not a compiled-in feature module (only cleanup and tickets are). Lua/Python modules are managed with `load`/`unload`.", name)))
					}
					enabled := strings.ToLower(ctx.Args[0]) == "enable"
					if err := ctx.Bot.SetEnabledModule(name, enabled); err != nil {
						return ctx.Respond(embed.Error("❌ Error", err.Error()))
					}
					verb := "enabled"
					if !enabled {
						verb = "disabled"
					}
					return ctx.Respond(embed.Success("✅ Module", fmt.Sprintf("`%s` %s. Applies after the next restart.", name, verb)))
				}
			}

			loaded := ctx.Bot.GetLoadedModuleNames()
			available := ctx.Bot.GetAvailableModuleNames()

			e := embed.New().
				WithTitle("📦 Modules").
				WithColor(embed.ColorInfo).
				WithTimestamp(time.Now())

			var fields []discord.EmbedField

			if len(loaded) > 0 {
				var lines []string
				for _, name := range loaded {
					marker := ""
					if ctx.Bot.IsBuiltinModule(name) {
						marker = " (builtin)"
					}
					lines = append(lines, fmt.Sprintf("`%s` ✅%s", name, marker))
				}
				fields = append(fields, discord.EmbedField{
					Name:   fmt.Sprintf("Loaded (%d)", len(loaded)),
					Value:  strings.Join(lines, "\n"),
					Inline: util.PtrBool(true),
				})
			} else {
				fields = append(fields, discord.EmbedField{
					Name:   "Loaded (0)",
					Value:  "None",
					Inline: util.PtrBool(true),
				})
			}

			var unloaded []string
			loadedSet := make(map[string]bool)
			for _, name := range loaded {
				loadedSet[name] = true
			}
			for _, name := range available {
				if !loadedSet[name] {
					unloaded = append(unloaded, name)
				}
			}

			if len(unloaded) > 0 {
				var lines []string
				for _, name := range unloaded {
					lines = append(lines, fmt.Sprintf("`%s` ❌", name))
				}
				fields = append(fields, discord.EmbedField{
					Name:   fmt.Sprintf("Available (%d)", len(unloaded)),
					Value:  strings.Join(lines, "\n"),
					Inline: util.PtrBool(true),
				})
			} else {
				fields = append(fields, discord.EmbedField{
					Name:   "Available (0)",
					Value:  "None",
					Inline: util.PtrBool(true),
				})
			}

			e = e.WithFields(fields...)
			return ctx.Respond(e)
		},
	})

	RegisterCoreCommand(Command{
		Name:        "load",
		Description: "Load a module so its commands become available. Use `all` to load every module.",
		Usage:       "load <module> | load all",
		OwnerOnly:   true,
		Category:    "modules",
		Execute: func(ctx *Context) error {
			if len(ctx.Args) == 0 {
				return ctx.Respond(embed.Warning("⚠️ Usage", "load <module> | load all"))
			}
			target := ctx.Args[0]
			if target == "all" {
				// Single discovery source: the configured modules path (respects
				// a custom modules.path AND the Go/Lua/Python layout). Scanning
				// GetConfigDir()/modules directly would miss every candidate on
				// installs with a customized path.
				loaded := 0
				for _, name := range ctx.Bot.GetAvailableModuleNames() {
					if err := ctx.Bot.LoadModule(name); err != nil {
						continue // already loaded or failed — count only successes
					}
					loaded++
				}
				return ctx.Respond(embed.Success("✅ Loaded", fmt.Sprintf("Loaded %d modules.", loaded)))
			}
			if err := ctx.Bot.LoadModule(target); err != nil {
				return ctx.Respond(embed.Error("❌ Failed", err.Error()))
			}
			return ctx.Respond(embed.Success("✅ Loaded", fmt.Sprintf("Module `%s` loaded successfully.", target)))
		},
	})

	RegisterCoreCommand(Command{
		Name:        "unload",
		Description: "Unload a module so its commands stop working. Use `all` to unload every module.",
		Usage:       "unload <module> | unload all",
		OwnerOnly:   true,
		Category:    "modules",
		Execute: func(ctx *Context) error {
			if len(ctx.Args) == 0 {
				return ctx.Respond(embed.Warning("⚠️ Usage", "unload <module> | unload all"))
			}
			target := ctx.Args[0]
			if target == "all" {
				if err := ctx.Bot.UnloadAllModules(); err != nil {
					return ctx.Respond(embed.Error("❌ Failed", err.Error()))
				}
				return ctx.Respond(embed.Success("✅ Unloaded", "All modules unloaded."))
			}
			if err := ctx.Bot.UnloadModule(target); err != nil {
				return ctx.Respond(embed.Error("❌ Failed", err.Error()))
			}
			return ctx.Respond(embed.Success("✅ Unloaded", fmt.Sprintf("Module `%s` unloaded successfully.", target)))
		},
	})

	RegisterCoreCommand(Command{
		Name:        "reload",
		Description: "Reload a module to pick up changes without restarting the bot. Use `all` for all.",
		Usage:       "reload <module> | reload all",
		OwnerOnly:   true,
		Category:    "modules",
		Execute: func(ctx *Context) error {
			if len(ctx.Args) == 0 {
				return ctx.Respond(embed.Warning("⚠️ Usage", "reload <module> | reload all"))
			}
			target := ctx.Args[0]
			if target == "all" {
				names := ctx.Bot.GetLoadedModuleNames()
				if len(names) == 0 {
					return ctx.Respond(embed.Info("📦 Reloading", "No modules to reload."))
				}
				failed := 0
				for _, name := range names {
					if err := ctx.Bot.ReloadModule(name); err != nil {
						failed++
					}
				}
				return ctx.Respond(embed.Success("✅ Reloaded", fmt.Sprintf("Reloaded %d modules (%d failed).", len(names)-failed, failed)))
			}
			if err := ctx.Bot.ReloadModule(target); err != nil {
				return ctx.Respond(embed.Error("❌ Failed", err.Error()))
			}
			return ctx.Respond(embed.Success("✅ Reloaded", fmt.Sprintf("Module `%s` reloaded successfully.", target)))
		},
	})

	RegisterCoreCommand(Command{
		Name:        "shutdown",
		Description: "Shut the bot down completely. Owner and elevated users only.",
		Usage:       "shutdown",
		OwnerOnly:   true,
		Category:    "core",
		Execute: func(ctx *Context) error {
			ctx.Respond(embed.Warning("⏻ Shutting Down", "Goodbye!"))
			ctx.Bot.Shutdown()
			return nil
		},
	})

	RegisterCoreCommand(Command{
		Name:        "restart",
		Description: "Restart the bot so it comes back up with a fresh state.",
		Usage:       "restart",
		OwnerOnly:   true,
		Category:    "core",
		Execute: func(ctx *Context) error {
			ctx.Respond(embed.Warning("🔄 Restarting", "Restarting bot..."))
			ctx.Bot.Restart()
			return nil
		},
	})

	RegisterCoreCommand(Command{
		Name:        "permissions",
		Description: "Grant or revoke elevated (owner-like) permissions for a user.",
		Usage:       "permissions add/remove/list <user_id>",
		OwnerOnly:   true,
		Category:    "core",
		Execute: func(ctx *Context) error {
			if len(ctx.Args) == 0 {
				return ctx.Respond(embed.Warning("⚠️ Usage", "permissions add <user_id>\npermissions remove <user_id>\npermissions list"))
			}
			sub := strings.ToLower(ctx.Args[0])
			switch sub {
			case "list":
				elevated := ctx.Bot.GetPermissionManager().GetElevated()
				if len(elevated) == 0 {
					return ctx.Respond(embed.Info("📋 Permissions", "No elevated users configured."))
				}
				list := ""
				for _, id := range elevated {
					list += fmt.Sprintf("<@%s> (`%s`)\n", id, id)
				}
				return ctx.Respond(embed.Info("📋 Permissions", fmt.Sprintf("Elevated users:\n%s", list)))
			case "add":
				if len(ctx.Args) < 2 {
					return ctx.Respond(embed.Warning("⚠️ Usage", "permissions add <user_id>"))
				}
				userID := util.ExtractID(ctx.Args[1])
				ctx.Bot.GetPermissionManager().AddElevated(userID)
				return ctx.Respond(embed.Success("✅ Added", fmt.Sprintf("User <@%s> now has elevated permissions.", userID)))
			case "remove":
				if len(ctx.Args) < 2 {
					return ctx.Respond(embed.Warning("⚠️ Usage", "permissions remove <user_id>"))
				}
				userID := util.ExtractID(ctx.Args[1])
				ctx.Bot.GetPermissionManager().RemoveElevated(userID)
				return ctx.Respond(embed.Success("✅ Removed", fmt.Sprintf("User <@%s> no longer has elevated permissions.", userID)))
			default:
				return ctx.Respond(embed.Warning("⚠️ Usage", "permissions add/remove/list <user_id>"))
			}
		},
	})

	RegisterCoreCommand(Command{
		Name:        "ratelimit",
		Description: "Check a user's rate limit status or reset it so they can use commands again.",
		Usage:       "ratelimit [status|reset] [user_id]",
		OwnerOnly:   true,
		Category:    "core",
		Execute: func(ctx *Context) error {
			if len(ctx.Args) == 0 {
				// Show usage — never auto-delete, like [p]help.
				e := embed.Info("⏱️ Rate Limit Management", "**Usage:** `"+ctx.Bot.GetPrefix()+"ratelimit [status|reset] [user_id]`\n\n**Commands:**\n- `status <user_id>` — Check rate limit status for a user\n- `reset <user_id>` — Reset rate limit for a user")
				return ctx.Respond(e)
			}

			subcmd := strings.ToLower(ctx.Args[0])

			switch subcmd {
			case "status":
				if len(ctx.Args) < 2 {
					return ctx.Respond(embed.Error("❌ Error", "Please specify a user ID."))
				}
				userID := ctx.Args[1]
				// StatusRateLimit is non-mutating: checking a user's status must
				// not count as that user executing a command.
				allowed, wait := ctx.Bot.StatusRateLimit(userID)
				if allowed {
					return ctx.Respond(embed.Success("⏱️ Rate Limit Status", fmt.Sprintf("User `%s` is **allowed** to use commands.", userID)))
				} else {
					return ctx.Respond(embed.Warning("⏱️ Rate Limit Status", fmt.Sprintf("User `%s` is **rate limited**. Wait `%d` seconds.", userID, int(wait.Seconds())+1)))
				}

			case "reset":
				if len(ctx.Args) < 2 {
					return ctx.Respond(embed.Error("❌ Error", "Please specify a user ID."))
				}
				userID := ctx.Args[1]
				ctx.Bot.ResetRateLimit(userID)
				return ctx.Respond(embed.Success("✅ Rate Limit Reset", fmt.Sprintf("Rate limit for user `%s` has been reset.", userID)))

			default:
				return ctx.Respond(embed.Error("❌ Error", "Unknown subcommand. Use `status` or `reset`."))
			}
		},
	})

	RegisterCoreCommand(Command{
		Name:        "update",
		Description: "Check for bot updates, apply them now, or show updater status.",
		Usage:       "update [check|now|status|test|set <key> <value>]",
		OwnerOnly:   true,
		Category:    "core",
		Execute: func(ctx *Context) error {
			upd, _ := ctx.Bot.GetUpdater().(*updater.Manager)
			if upd == nil {
				return ctx.Respond(embed.Error("❌ Updater", "Updater is not available."))
			}
			sub := ""
			if len(ctx.Args) > 0 {
				sub = strings.ToLower(ctx.Args[0])
			}
			switch sub {
			case "", "check":
				res, err := upd.Check(context.Background())
				if err != nil {
					return ctx.Respond(embed.Error("❌ Update Check Failed", err.Error()))
				}
				// Four states, version first where the repo has release tags.
				// "Commits are current" and "the running binary is current" are
				// different facts: a manual `git pull` without a rebuild leaves the
				// process on the previous release while HEAD sits on the new tag, and
				// that must never be reported as "up to date".
				switch {
				case res.VersionBehind && res.UpToDate:
					return ctx.Respond(embed.Info("📥 Rebuild Pending",
						fmt.Sprintf("The code is at **v%s** but this process runs **v%s** — it was pulled without rebuilding. Run `%supdate now` to rebuild and restart.",
							res.ToVersion, res.FromVersion, ctx.Bot.GetPrefix())))
				case res.VersionBehind:
					return ctx.Respond(embed.Info("📥 Update Available",
						fmt.Sprintf("%s (**%d** new commit%s). Run `%supdate now` to pull, rebuild and restart.",
							res.VersionSummary(), res.Behind, plural(res.Behind), ctx.Bot.GetPrefix())))
				case res.UpToDate:
					title := fmt.Sprintf("`%s` is on the latest commit (`%s`).", ctx.Bot.GetName(), short7(res.LocalSHA))
					if summary := res.VersionSummary(); summary != "" {
						title = fmt.Sprintf("`%s` is up to date — %s (`%s`).", ctx.Bot.GetName(), summary, short7(res.LocalSHA))
					}
					return ctx.Respond(embed.Success("✅ Up to Date", title))
				default:
					return ctx.Respond(embed.Info("📥 Update Available",
						fmt.Sprintf("**%d** new commit%s available. Run `%supdate now` to pull, rebuild and restart.", res.Behind, plural(res.Behind), ctx.Bot.GetPrefix())))
				}
			case "now":
				if err := ctx.Respond(embed.Info("🔄 Updating", "Pulling latest code, rebuilding...")); err != nil {
					return err
				}
				if err := upd.Apply(context.Background()); err != nil {
					return ctx.Respond(embed.Error("❌ Update Failed", err.Error()))
				}
				// OnApplied (wired in main.go) triggers the restart after a short
				// delay so this success embed is delivered first.
				return ctx.Respond(embed.Success("✅ Update Applied", "New binary built. Restarting in a moment..."))
			case "status":
				s := upd.Status()
				fields := []discord.EmbedField{
					{Name: "Repo", Value: "`" + s["repo"] + "`", Inline: util.PtrBool(true)},
					{Name: "Branch", Value: "`" + s["branch"] + "`", Inline: util.PtrBool(true)},
					{Name: "Enabled", Value: s["enabled"], Inline: util.PtrBool(true)},
					{Name: "Interval", Value: s["interval"], Inline: util.PtrBool(true)},
					{Name: "Auto Pull", Value: s["auto_pull"], Inline: util.PtrBool(true)},
					{Name: "Notify Channel", Value: "`" + s["notify_channel"] + "`", Inline: util.PtrBool(true)},
					{Name: "Last Seen SHA", Value: "`" + s["last_sha"] + "`", Inline: util.PtrBool(true)},
					{Name: "Last Check", Value: s["last_check"], Inline: util.PtrBool(true)},
					{Name: "Running Version", Value: displayVersion(s["version"]), Inline: util.PtrBool(true)},
				}
				if s["latest_version"] != "" {
					fields = append(fields, discord.EmbedField{Name: "Latest Release", Value: "v" + s["latest_version"], Inline: util.PtrBool(true)})
				}
				if s["last_error"] != "" {
					fields = append(fields, discord.EmbedField{Name: "Last Error", Value: s["last_error"], Inline: util.PtrBool(false)})
				}
				e := embed.New().WithTitle("🔄 Updater Status").WithColor(embed.ColorInfo).WithFields(fields...).WithTimestamp(time.Now())
				return ctx.Respond(e)
			case "test":
				if err := upd.NotifyTest(); err != nil {
					return ctx.Respond(embed.Error("❌ Update Test Failed", err.Error()))
				}
				return ctx.Respond(embed.Success("✅ Test Sent", "Sample PR + commit embeds posted to the notify channel."))
			case "set":
				if len(ctx.Args) < 3 {
					return ctx.Respond(embed.Warning("⚠️ Usage", "update set <key> <value>\nKeys: enabled, repo, branch, token, interval, auto_pull, notify_channel"))
				}
				key := ctx.Args[1]
				value := strings.Join(ctx.Args[2:], " ")
				if err := ctx.Bot.SetConfig("updater_"+key, value); err != nil {
					return ctx.Respond(embed.Error("❌ Error", err.Error()))
				}
				if key == "token" {
					return ctx.Respond(embed.Success("✅ Set", "`updater_token` updated (value hidden)."))
				}
				return ctx.Respond(embed.Success("✅ Set", fmt.Sprintf("`updater_%s` = `%s`", key, value)))
			default:
				return ctx.Respond(embed.Warning("⚠️ Usage", "update [check|now|status|test|set <key> <value>]"))
			}
		},
	})

	registerCoreSlashCommands()
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

// subOpt builds a nested subcommand option. The slash dispatcher prepends the
// subcommand name to the dispatched args, then appends the provided options in
// declaration order — exactly the positional args the prefix handlers expect.
func subOpt(name, desc string, opts ...discord.ApplicationCommandOption) discord.ApplicationCommandOptionSubCommand {
	return discord.ApplicationCommandOptionSubCommand{
		Name:        name,
		Description: desc,
		Options:     opts,
	}
}

func registerCoreSlashCommands() {
	for _, cmd := range CoreCommands {
		var opts []discord.ApplicationCommandOption

		switch cmd.Name {
		case "help":
			opts = []discord.ApplicationCommandOption{
				strOpt("command", "Command name to show details for", false),
			}
		case "load", "unload", "reload":
			opts = []discord.ApplicationCommandOption{
				strOpt("module", "Module name or 'all'", true),
			}
		case "permissions":
			opts = []discord.ApplicationCommandOption{
				subOpt("add", "Grant elevated permissions to a user", userOpt("user", "The user", true)),
				subOpt("remove", "Revoke elevated permissions from a user", userOpt("user", "The user", true)),
				subOpt("list", "List elevated users"),
			}
		case "ratelimit":
			opts = []discord.ApplicationCommandOption{
				subOpt("status", "Check a user's rate limit status", userOpt("user", "The user", true)),
				subOpt("reset", "Reset a user's rate limit", userOpt("user", "The user", true)),
			}
		case "update":
			opts = []discord.ApplicationCommandOption{
				subOpt("check", "Check for new commits"),
				subOpt("now", "Pull, rebuild and restart the bot"),
				subOpt("status", "Show updater status"),
				subOpt("test", "Send sample PR + commit embeds"),
				subOpt("set", "Set an updater config value", strOpt("key", "Config key: enabled, repo, branch, token, interval, auto_pull, notify_channel", true), strOpt("value", "Config value", true)),
			}
		}

		RegisterCoreSlash(SlashCommand{
			Name:           cmd.Name,
			Description:    cmd.Description,
			Category:       cmd.Category,
			Options:        opts,
			RequiredPerm:   cmd.RequiredPerm,
			OwnerOnly:      cmd.OwnerOnly,
			SuperOwnerOnly: cmd.SuperOwnerOnly,
			Execute:        cmd.Execute,
		})
	}
}
