package main

import (
	"sort"
	"strings"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
	"github.com/disgoorg/disgo/discord"
)

// cmdView is one command entry in the dashboard's command catalog.
type cmdView struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Usage          string   `json:"usage"`
	Category       string   `json:"category"`
	ModuleOwner    string   `json:"module_owner"`        // "core" | <module name>
	Kind           string   `json:"kind"`                // "prefix" | "slash"
	RequiredPerm   string   `json:"required_perm_label"` // human label
	OwnerOnly      bool     `json:"owner_only"`
	SuperOwnerOnly bool     `json:"super_owner_only"`
	Aliases        []string `json:"aliases"`
	Usable         bool     `json:"usable"`
	UsableIn       []string `json:"usable_in"`
	// Options is the click-driven argument form schema, derived from the
	// command's slash options (the same schema Discord's UI uses). Empty =
	// free-form text arguments.
	Options []argOpt `json:"options"`
	// FreeArgs is true when the command takes arguments but has no option
	// schema: render the free-text fallback box. Zero-arg commands render
	// neither (Run executes with no arguments).
	FreeArgs bool `json:"free_args"`
}

// argOpt is one typed argument of a command's web Run form.
type argOpt struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Type        string   `json:"type"` // "string" | "int" | "bool" | "channel" | "role" | "user" | "subcommand"
	Required    bool     `json:"required"`
	Choices     []string `json:"choices,omitempty"`
	// Sub is set for Type == "subcommand": the nested arguments of each
	// subcommand. Choices holds the subcommand names.
	Sub []subArg `json:"sub,omitempty"`
}

// subArg is one subcommand's nested argument schema.
type subArg struct {
	Name string   `json:"name"`
	Args []argOpt `json:"args"`
}

// optionRequired extracts the Required flag from a typed discord option.
func optionRequired(o discord.ApplicationCommandOption) bool {
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

// optionSchema converts one discord option into the web form's argOpt. A
// subcommand becomes a required string with the subcommand names as choices
// (matching how the slash dispatcher prepends the subcommand name to args).
func optionSchema(o discord.ApplicationCommandOption) argOpt {
	switch t := o.(type) {
	case discord.ApplicationCommandOptionString:
		a := argOpt{Name: t.Name, Description: t.Description, Type: "string", Required: t.Required}
		for _, c := range t.Choices {
			a.Choices = append(a.Choices, c.Value)
		}
		return a
	case discord.ApplicationCommandOptionInt, discord.ApplicationCommandOptionFloat:
		return argOpt{Name: o.OptionName(), Description: o.OptionDescription(), Type: "int", Required: optionRequired(o)}
	case discord.ApplicationCommandOptionBool:
		return argOpt{Name: o.OptionName(), Description: o.OptionDescription(), Type: "bool", Required: optionRequired(o)}
	case discord.ApplicationCommandOptionChannel:
		return argOpt{Name: o.OptionName(), Description: o.OptionDescription(), Type: "channel", Required: optionRequired(o)}
	case discord.ApplicationCommandOptionRole:
		return argOpt{Name: o.OptionName(), Description: o.OptionDescription(), Type: "role", Required: optionRequired(o)}
	case discord.ApplicationCommandOptionUser, discord.ApplicationCommandOptionMentionable:
		return argOpt{Name: o.OptionName(), Description: o.OptionDescription(), Type: "user", Required: optionRequired(o)}
	case discord.ApplicationCommandOptionSubCommand:
		// A subcommand is one choice of the subcommand selector; its nested
		// options become the args shown once that subcommand is picked. The
		// dispatched args are [subcommandName, ...nestedArgs] — matching the
		// slash dispatcher's SubCommandName-first conversion.
		a := argOpt{Name: "subcommand", Description: t.Description, Type: "subcommand", Required: true}
		a.Sub = append(a.Sub, subArg{Name: t.Name, Args: optionSchemas(t.Options)})
		a.Choices = append(a.Choices, t.Name)
		return a
	case discord.ApplicationCommandOptionSubCommandGroup:
		// Sub-command groups don't map to flat positional args; skip (the
		// caller filters empty names).
		return argOpt{}
	default:
		return argOpt{Name: o.OptionName(), Description: o.OptionDescription(), Type: "string", Required: optionRequired(o)}
	}
}

// optionSchemas converts a command's options into web form args. Sibling
// subcommand options are merged into ONE selector (the dispatcher branches on
// ctx.Args[0], so a command with `ban` + `kick` must present a single choice).
func optionSchemas(opts []discord.ApplicationCommandOption) []argOpt {
	if len(opts) == 0 {
		return nil
	}
	out := make([]argOpt, 0, len(opts))
	subIdx := -1 // index of the merged subcommand selector, if any
	for _, o := range opts {
		a := optionSchema(o)
		if a.Name == "" {
			continue // unrenderable (e.g. subcommand group)
		}
		if a.Type == "subcommand" {
			if subIdx < 0 {
				out = append(out, a)
				subIdx = len(out) - 1
			} else {
				out[subIdx].Choices = append(out[subIdx].Choices, a.Choices...)
				out[subIdx].Sub = append(out[subIdx].Sub, a.Sub...)
			}
			continue
		}
		// A plain option literally named "subcommand" would collide with the
		// merged selector's data-arg label and confuse the JS arg collection —
		// the selector already contributes the subcommand name positionally.
		if a.Name == "subcommand" && subIdx >= 0 {
			continue
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cmdUsable computes whether a command is usable for the logged-in user and in
// which guilds, using the exact filter the bot applies at dispatch:
//
//	cmdUsable = !SuperOwnerOnly(other than owner) && CanUse(...)
//
// Owner/elevated bypass permission checks (CanUse returns true for them), so
// they can use every command in every bot guild (SuperOwnerOnly commands only
// the owner). Staff/regular are checked per mutual guild.
func (m *DashboardModule) cmdUsable(superOnly bool, reqPerm discord.Permissions, ownerOnly bool, us *userSession, level string) (bool, []string) {
	// SuperOwnerOnly is checked at dispatch level before CanUse: only the owner passes.
	if superOnly && level != lvlOwner {
		return false, nil
	}
	if level == lvlOwner || level == lvlElevated {
		return true, m.allBotGuildList()
	}
	// staff / regular: evaluate per mutual guild where they're a member/cache entry
	userID := ""
	if us != nil {
		userID = us.userID.String()
	}
	var usableIn []string
	for _, gID := range m.mutualGuildIDs(us) {
		userPerms := m.ctx.Bot.GetUserPermissions(userID, gID)
		guildOwner := m.ctx.Bot.GetGuildOwnerID(gID)
		if m.ctx.Bot.CanUse(userID, userPerms, reqPerm, ownerOnly, guildOwner) {
			usableIn = append(usableIn, gID)
		}
	}
	return len(usableIn) > 0, usableIn
}

// freeArgsNeeded reports whether a command without an option schema still
// takes untyped positional arguments (usage contains <arg> or [opt]
// placeholders). Zero-arg commands render neither a form nor a free-text box.
func freeArgsNeeded(usage string, hasOptions bool) bool {
	return !hasOptions && strings.ContainsAny(usage, "<[")
}

// buildCommandCatalog builds the raw catalog (every command). Caller filters to
// those usable by the user unless `raw` is requested by an elevated/owner user.
func (m *DashboardModule) buildCommandCatalog(us *userSession) []cmdView {
	level := lvlRegular
	if us != nil {
		level = m.resolveLevel(us)
	}

	var views []cmdView

	addPrefix := func(c commands.Command, owner string, slashOpts []discord.ApplicationCommandOption) {
		usable, usableIn := m.cmdUsable(c.SuperOwnerOnly, c.RequiredPerm, c.OwnerOnly, us, level)
		aliases := append([]string{}, c.Aliases...)
		views = append(views, cmdView{
			Name: c.Name, Description: c.Description, Usage: c.Usage, Category: c.Category,
			ModuleOwner: owner, Kind: "prefix", RequiredPerm: permLabel(c.RequiredPerm),
			OwnerOnly: c.OwnerOnly, SuperOwnerOnly: c.SuperOwnerOnly, Aliases: aliases,
			Usable: usable, UsableIn: usableIn, Options: optionSchemas(slashOpts),
			FreeArgs: freeArgsNeeded(c.Usage, len(slashOpts) > 0),
		})
	}
	addSlash := func(c commands.SlashCommand, owner string) {
		usable, usableIn := m.cmdUsable(c.SuperOwnerOnly, c.RequiredPerm, c.OwnerOnly, us, level)
		views = append(views, cmdView{
			Name: c.Name, Description: c.Description, Category: c.Category,
			ModuleOwner: owner, Kind: "slash", RequiredPerm: permLabel(c.RequiredPerm),
			OwnerOnly: c.OwnerOnly, SuperOwnerOnly: c.SuperOwnerOnly,
			Usable: usable, UsableIn: usableIn, Options: optionSchemas(c.Options),
			// Slash commands can't carry untyped args — no free-text box.
			FreeArgs: false,
		})
	}

	// Slash twins supply the arg schema for prefix rows too (same options the
	// Discord UI would show for the command).
	coreSlashOpts := map[string][]discord.ApplicationCommandOption{}
	for i := range commands.CoreSlashCommands {
		coreSlashOpts[commands.CoreSlashCommands[i].Name] = commands.CoreSlashCommands[i].Options
	}
	for _, c := range commands.CoreCommands {
		addPrefix(c, "core", coreSlashOpts[c.Name])
	}
	for _, c := range commands.CoreSlashCommands {
		addSlash(c, "core")
	}
	if mgr, ok := m.ctx.Bot.GetModuleManager().(*modules.Manager); ok {
		for _, name := range m.ctx.Bot.GetLoadedModuleNames() {
			mod, ok := mgr.Get(name)
			if !ok {
				continue
			}
			// Fetch each command list ONCE per module — the interface
			// methods can rebuild slices per call.
			modCmds := mod.Commands()
			modSlash := mod.SlashCommands()
			modSlashOpts := make(map[string][]discord.ApplicationCommandOption, len(modSlash))
			for i := range modSlash {
				modSlashOpts[modSlash[i].Name] = modSlash[i].Options
			}
			for _, c := range modCmds {
				addPrefix(c, mod.Name(), modSlashOpts[c.Name])
			}
			for _, c := range modSlash {
				addSlash(c, mod.Name())
			}
		}
	}

	// NOT sorted here on purpose: the registration order mirrors
	// ExecuteCommand's resolution precedence (core prefix, core slash, then
	// modules in load order), and dedupeForMode relies on it to pick the same
	// command the Run button would actually execute. Presentation sorting
	// happens in filterCatalog AFTER dedupe.
	return views
}

// sortViews orders the catalog for presentation: module owner, then category,
// then name.
func sortViews(views []cmdView) {
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].ModuleOwner != views[j].ModuleOwner {
			return views[i].ModuleOwner < views[j].ModuleOwner
		}
		if views[i].Category != views[j].Category {
			return views[i].Category < views[j].Category
		}
		return views[i].Name < views[j].Name
	})
}

// filterCatalog hides unusable commands unless the caller is elevated+ and
// requests `raw` (e.g. an elevated user inspecting super-owner commands), and
// collapses the catalog to ONE entry per command name — the configured
// execution way (prefix/slash) wins, with a fallback to the other kind for
// commands that only exist in one form.
func (m *DashboardModule) filterCatalog(us *userSession, raw, guildScoped bool, guildID string) []cmdView {
	level := lvlRegular
	if us != nil {
		level = m.resolveLevel(us)
	}
	// Dedupe on the registration order (dispatch precedence) so the displayed
	// entry is the one ExecuteCommand resolves, then sort for presentation.
	all := dedupeForMode(m.buildCommandCatalog(us), m.execMode())
	sortViews(all)
	includeAll := raw && (level == lvlOwner || level == lvlElevated)

	out := make([]cmdView, 0, len(all))
	for _, c := range all {
		usable := c.Usable
		if guildScoped {
			usable = contains(c.UsableIn, guildID) || level == lvlOwner || level == lvlElevated
		}
		if !includeAll && !usable {
			continue
		}
		c.Usable = usable
		out = append(out, c)
	}
	return out
}

// dedupeForMode collapses a catalog to one entry per command name, preferring
// the given execution kind ("prefix" or "slash"). Commands that only exist in
// the other kind keep their entry, so nothing disappears from the catalog.
// Order follows the first occurrence of each name in views.
func dedupeForMode(views []cmdView, mode string) []cmdView {
	type pick struct {
		pref, fallback cmdView
		hasPref        bool
		hasFallback    bool
	}
	var order []string
	byName := map[string]*pick{}
	for _, v := range views {
		p, ok := byName[v.Name]
		if !ok {
			p = &pick{}
			byName[v.Name] = p
			order = append(order, v.Name)
		}
		if v.Kind == mode {
			if !p.hasPref {
				p.pref = v
				p.hasPref = true
			}
		} else if !p.hasFallback {
			p.fallback = v
			p.hasFallback = true
		}
	}
	out := make([]cmdView, 0, len(order))
	for _, name := range order {
		p := byName[name]
		if p.hasPref {
			out = append(out, p.pref)
		} else {
			out = append(out, p.fallback)
		}
	}
	return out
}

// contains reports whether s contains v.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// permLabel renders requirement permissions in human form using disgo's own
// Permissions.String() (e.g. "Administrator", "Manage Server, Kick Members").
func permLabel(p discord.Permissions) string {
	s := p.String()
	if s == "" {
		return "None"
	}
	return s
}

// groupingForTemplate groups commands by ModuleOwner then Category for the page.
func groupCommands(views []cmdView) []moduleGroup {
	index := map[string]int{}
	var groups []moduleGroup
	for _, v := range views {
		mi, ok := index[v.ModuleOwner]
		if !ok {
			mi = len(groups)
			groups = append(groups, moduleGroup{Module: v.ModuleOwner})
			index[v.ModuleOwner] = mi
		}
		ci := -1
		for i, c := range groups[mi].Categories {
			if c.Name == v.Category {
				ci = i
				break
			}
		}
		if ci < 0 {
			groups[mi].Categories = append(groups[mi].Categories, catGroup{Name: v.Category})
			ci = len(groups[mi].Categories) - 1
		}
		groups[mi].Categories[ci].Commands = append(groups[mi].Categories[ci].Commands, v)
	}
	return groups
}

type moduleGroup struct {
	Module     string
	Categories []catGroup
}

type catGroup struct {
	Name     string
	Commands []cmdView
}

// Display returns the human heading for a module group.
func (mg *moduleGroup) Display() string {
	if mg.Module == "core" {
		return "Core"
	}
	return mg.Module
}

// pageTitle returns a human-readable module heading.
func (mg moduleGroup) pageTitle() string {
	if mg.Module == "core" {
		return "Core"
	}
	return strings.Title(mg.Module) //nolint:staticcheck
}
