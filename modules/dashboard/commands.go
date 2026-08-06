package main

import (
	"sort"
	"strings"

	"github.com/custombot/bot/commands"
	"github.com/custombot/bot/modules"
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

// buildCommandCatalog builds the raw catalog (every command). Caller filters to
// those usable by the user unless `raw` is requested by an elevated/owner user.
func (m *DashboardModule) buildCommandCatalog(us *userSession) []cmdView {
	level := lvlRegular
	if us != nil {
		level = m.resolveLevel(us)
	}

	var views []cmdView

	addPrefix := func(c commands.Command, owner string) {
		usable, usableIn := m.cmdUsable(c.SuperOwnerOnly, c.RequiredPerm, c.OwnerOnly, us, level)
		aliases := append([]string{}, c.Aliases...)
		views = append(views, cmdView{
			Name: c.Name, Description: c.Description, Usage: c.Usage, Category: c.Category,
			ModuleOwner: owner, Kind: "prefix", RequiredPerm: permLabel(c.RequiredPerm),
			OwnerOnly: c.OwnerOnly, SuperOwnerOnly: c.SuperOwnerOnly, Aliases: aliases,
			Usable: usable, UsableIn: usableIn,
		})
	}
	addSlash := func(c commands.SlashCommand, owner string) {
		usable, usableIn := m.cmdUsable(c.SuperOwnerOnly, c.RequiredPerm, c.OwnerOnly, us, level)
		views = append(views, cmdView{
			Name: c.Name, Description: c.Description, Category: c.Category,
			ModuleOwner: owner, Kind: "slash", RequiredPerm: permLabel(c.RequiredPerm),
			OwnerOnly: c.OwnerOnly, SuperOwnerOnly: c.SuperOwnerOnly,
			Usable: usable, UsableIn: usableIn,
		})
	}

	for _, c := range commands.CoreCommands {
		addPrefix(c, "core")
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
			for _, c := range mod.Commands() {
				addPrefix(c, mod.Name())
			}
			for _, c := range mod.SlashCommands() {
				addSlash(c, mod.Name())
			}
		}
	}

	// stable order: module owner, then category, then name
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].ModuleOwner != views[j].ModuleOwner {
			return views[i].ModuleOwner < views[j].ModuleOwner
		}
		if views[i].Category != views[j].Category {
			return views[i].Category < views[j].Category
		}
		return views[i].Name < views[j].Name
	})
	return views
}

// filterCatalog hides unusable commands unless the caller is elevated+ and
// requests `raw` (e.g. an elevated user inspecting super-owner commands).
func (m *DashboardModule) filterCatalog(us *userSession, raw, guildScoped bool, guildID string) []cmdView {
	level := lvlRegular
	if us != nil {
		level = m.resolveLevel(us)
	}
	all := m.buildCommandCatalog(us)
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
