package tickets

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/modules"
)

// webconfig.go — [p]tickets / /tickets command tree (v3, per-guild) +
// WebConfigurable surface.
//
// Per-server configuration: every subcommand reads/writes the CURRENT guild's
// config file. The dashboard writes the global module settings through
// WebConfigurable; per-guild panel/type edits go through these commands.
//
//	[p]tickets setup                          guided checklist
//	[p]tickets panel create <name> [#chan] <type>
//	[p]tickets panel set <name> title|description <text…>
//	[p]tickets panel move <name> [#chan] · resend · suspend · resume · remove · list
//	[p]tickets type add <key> · set <key> <field> <value…> · enable/disable/remove/list/show
//	[p]tickets access add|remove|list <role…>
//	[p]tickets logchannel [#chan] · reload

func (m *TicketsModule) prefixCommands() []commands.Command {
	return []commands.Command{
		{
			Name: "tickets", Description: "Ticket system configuration for this server",
			Usage: "tickets setup|panel|type|access|logchannel|reload", Category: "Tickets",
			RequiredPerm: discord.PermissionManageGuild,
			Execute:      m.runTicketsCommand,
		},
	}
}

func (m *TicketsModule) runTicketsCommand(ctx *commands.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.Respond(embed.Info("🎫 Tickets", usageText()))
	}
	switch ctx.Args[0] {
	case "setup":
		return m.cmdSetup(ctx)
	case "panel":
		return m.cmdPanel(ctx)
	case "type":
		return m.cmdType(ctx)
	case "access":
		return m.cmdAccess(ctx)
	case "logchannel":
		return m.cmdLogChannel(ctx)
	case "reload":
		return m.cmdReload(ctx)
	}
	return ctx.Respond(embed.Warning("⚠️ Usage", usageText()))
}

func usageText() string {
	return strings.Join([]string{
		"`tickets setup` — guided setup checklist",
		"`tickets type add <key>` / `type set <key> <field> <value…>`",
		"　fields: `label category ping helper access welcome body color button emoji`",
		"`tickets type list|show|enable|disable|remove <key>`",
		"`tickets panel create <name> [#channel] <type>`",
		"`tickets panel set <name> title|description <text…>`",
		"`tickets panel list|move|resend|suspend|resume|remove …`",
		"`tickets access add|remove|list <role…>` — who can open tickets",
		"`tickets logchannel #channel` — where transcripts go",
		"`/tickets …` — same tree as the prefix commands above",
	}, "\n")
}

// ── slash tree ────────────────────────────────────────────────────────────
//
// The slash tree mirrors the prefix tree exactly: `type`, `panel` and
// `access` are subcommand GROUPS (Discord's one nesting level), each holding
// the same subcommands the prefix tree dispatches on. The dispatcher builds
// the positional arg vector via commands.SlashArgs (declared option order),
// so Execute can be the prefix handler verbatim — no parallel mapping table.

func strOpt(name, desc string, required bool) discord.ApplicationCommandOption {
	return discord.ApplicationCommandOptionString{Name: name, Description: desc, Required: required}
}

func chanOpt(name, desc string, required bool) discord.ApplicationCommandOption {
	return discord.ApplicationCommandOptionChannel{Name: name, Description: desc, Required: required}
}

func subCmd(name, desc string, opts ...discord.ApplicationCommandOption) discord.ApplicationCommandOptionSubCommand {
	return discord.ApplicationCommandOptionSubCommand{Name: name, Description: desc, Options: opts}
}

func subGroup(name, desc string, subs ...discord.ApplicationCommandOptionSubCommand) discord.ApplicationCommandOption {
	return discord.ApplicationCommandOptionSubCommandGroup{Name: name, Description: desc, Options: subs}
}

func (m *TicketsModule) slashOptions() []discord.ApplicationCommandOption {
	return []discord.ApplicationCommandOption{
		subCmd("setup", "Guided setup checklist"),
		subCmd("reload", "Reload this server's ticket config from disk"),
		subCmd("logchannel", "Set the transcript log channel", chanOpt("channel", "Log channel", false)),
		subGroup("type", "Manage ticket types",
			subCmd("add", "Add a ticket type", strOpt("key", "Type key", true)),
			subCmd("set", "Set a type field", strOpt("key", "Type key", true), strOpt("field", "Field", true), strOpt("value", "Value", true)),
			subCmd("enable", "Enable a type", strOpt("key", "Type key", true)),
			subCmd("disable", "Disable a type", strOpt("key", "Type key", true)),
			subCmd("remove", "Remove a type", strOpt("key", "Type key", true)),
			subCmd("show", "Show a type", strOpt("key", "Type key", true)),
			subCmd("list", "List types"),
		),
		subGroup("panel", "Manage ticket panels",
			subCmd("create", "Create a panel", strOpt("name", "Panel name", true), chanOpt("channel", "Channel (default: current)", false), strOpt("type", "Type key", true)),
			subCmd("set", "Set a panel field", strOpt("name", "Panel name", true), strOpt("field", "title|description", true), strOpt("value", "Value", true)),
			subCmd("move", "Move a panel", strOpt("name", "Panel name", true), chanOpt("channel", "Channel (default: current)", false)),
			subCmd("resend", "Repost a panel", strOpt("name", "Panel name", false)),
			subCmd("suspend", "Suspend a panel", strOpt("name", "Panel name", true)),
			subCmd("resume", "Resume a panel", strOpt("name", "Panel name", true)),
			subCmd("remove", "Remove a panel", strOpt("name", "Panel name", true)),
			subCmd("list", "List panels"),
		),
		subGroup("access", "Manage who can open tickets",
			subCmd("add", "Add opener roles to a type", strOpt("type", "Type key", true), strOpt("role", "Role ID", true)),
			subCmd("remove", "Remove opener roles from a type", strOpt("type", "Type key", true), strOpt("role", "Role ID", true)),
			subCmd("list", "List opener roles"),
		),
	}
}

func (m *TicketsModule) slashCommands() []commands.SlashCommand {
	return []commands.SlashCommand{{
		Name:         "tickets",
		Description:  "Ticket system configuration for this server",
		Category:     "Tickets",
		RequiredPerm: discord.PermissionManageGuild,
		Options:      m.slashOptions(),
		Execute:      m.runTicketsCommand,
	}}
}

// ── setup ────────────────────────────────────────────────────────────────

// cmdSetup inspects live config state and prints a plain-language checklist
// with the exact next commands — AAA3A-style guided setup.
func (m *TicketsModule) cmdSetup(ctx *commands.Context) error {
	if ctx.GuildID == "" {
		return ctx.Respond(embed.Error("❌ Error", "Server-only command."))
	}
	cfg := m.guildConfig(ctx.GuildID)
	m.mu.RLock()
	var (
		hasType   bool
		hasPanel  bool
		hasLog    bool
		typeNames []string
		panelRows []string
	)
	for k, t := range cfg.Types {
		if t == nil {
			continue
		}
		state := "🟢"
		if !t.Enabled || t.Category == "" {
			state = "🟡"
		}
		typeNames = append(typeNames, state+" `"+k+"`")
		if t.Enabled && t.Category != "" {
			hasType = true
		}
	}
	for name, p := range cfg.Panels {
		susp := ""
		if p.Suspended {
			susp = " ⏸ suspended"
		}
		panelRows = append(panelRows, "`"+name+"` → <#"+p.ChannelID+"> ("+p.TypeKey+")"+susp)
		if !p.Suspended {
			hasPanel = true
		}
	}
	hasLog = cfg.LogChannel != ""
	m.mu.RUnlock()

	var b strings.Builder
	b.WriteString("**Ticket system status**\n")
	switch {
	case len(typeNames) == 0:
		b.WriteString("1️⃣ No ticket types yet. Create one:\n　`tickets type add staff`\n　`tickets type set staff category <#category>`\n　`tickets type set staff label Staff`\n　`tickets type enable staff`\n")
	default:
		b.WriteString("✅ Types: " + strings.Join(typeNames, ", ") + "\n")
	}
	if len(typeNames) > 0 && !hasType {
		b.WriteString("⚠️ No type is fully enabled — each needs `category` set and to be enabled.\n")
	}
	if hasLog {
		b.WriteString("✅ Transcript log channel set.\n")
	} else {
		b.WriteString("2️⃣ Set a transcript log channel:\n　`tickets logchannel #ticket-log`\n")
	}
	switch {
	case len(panelRows) == 0:
		b.WriteString("3️⃣ Post your first panel:\n　`tickets panel create contact_staff #support staff`\n")
	case !hasPanel:
		b.WriteString("⚠️ All panels are suspended — resume one with `tickets panel resume <name>`.\n")
	default:
		b.WriteString("✅ Panels: " + strings.Join(panelRows, ", ") + "\n")
	}
	if hasType && hasLog && hasPanel {
		b.WriteString("\n🎉 Everything is configured. Users can open tickets from your panel buttons.")
	} else {
		b.WriteString("\nFinish the numbered steps above and you're done.")
	}
	return ctx.Respond(embed.Info("🎫 Tickets setup", b.String()))
}

// ── panel subcommand tree ────────────────────────────────────────────────

func (m *TicketsModule) cmdPanel(ctx *commands.Context) error {
	if ctx.GuildID == "" {
		return ctx.Respond(embed.Error("❌ Error", "Server-only command."))
	}
	args := ctx.Args[1:]
	if len(args) == 0 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel create|set|move|resend|suspend|resume|remove|list …`"))
	}
	switch args[0] {
	case "list":
		panels := m.panelsSnapshot(ctx.GuildID)
		if len(panels) == 0 {
			return ctx.Respond(embed.Info("Panels", "None yet — `tickets panel create <name> [#chan] <type>`."))
		}
		var rows []string
		for _, p := range panels {
			state := "🟢"
			if p.Suspended {
				state = "⏸"
			}
			rows = append(rows, fmt.Sprintf("%s `%s` → <#%s> (%s)", state, p.Name, p.ChannelID, p.TypeKey))
		}
		return ctx.Respond(embed.Info("Panels", strings.Join(rows, "\n")))

	case "create":
		// Short form: `panel create <name> <type>` (uses current channel).
		// Long form: `panel create <name> [#channel] <type>`.
		if len(args) < 3 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel create <name> [#channel] <type>`\nThe type must exist first (`tickets type add <key>`)."))
		}
		name := args[1]
		if !validPanelName(name) {
			return ctx.Respond(embed.Error("❌ Error", "Panel names: letters, digits, `-`, `_` only (no spaces)."))
		}
		var channelArg, typeKey string
		if len(args) == 3 {
			channelArg = "" // current channel
			typeKey = args[2]
		} else {
			channelArg = args[2]
			typeKey = args[3]
		}
		chID := parseChannelRef(channelArg, ctx.ChannelID)
		if chID == "" {
			return ctx.Respond(embed.Error("❌ Error", "Could not parse the channel. Mention it (#channel) or paste its ID."))
		}
		cfg := m.guildConfig(ctx.GuildID)
		m.mu.Lock()
		if _, exists := cfg.Panels[name]; exists {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Panel `"+name+"` already exists — use `panel set`/`panel move`."))
		}
		p := PanelConfig{Name: name, ChannelID: chID, TypeKey: typeKey}
		cfg.Panels[name] = p
		m.mu.Unlock()
		if err := m.postOrUpdatePanel(ctx.GuildID, &p); err != nil {
			m.mu.Lock()
			delete(cfg.Panels, name)
			_ = m.saveGuildLocked(ctx.GuildID)
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Panel created", fmt.Sprintf("`%s` now lives in <#%s> (type: %s).", name, p.ChannelID, p.TypeKey)))

	case "set":
		if len(args) < 4 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel set <name> title|description <text…>`"))
		}
		name, field := args[1], args[2]
		text := strings.Join(args[3:], " ")
		cfg := m.guildConfig(ctx.GuildID)
		m.mu.Lock()
		p, ok := cfg.Panels[name]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown panel `"+name+"`."))
		}
		switch field {
		case "title":
			p.Title = text
		case "description":
			p.Description = text
		default:
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Fields: `title`, `description`."))
		}
		cfg.Panels[name] = p
		m.mu.Unlock()
		if err := m.postOrUpdatePanel(ctx.GuildID, &p); err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Updated", "Panel `"+name+"` "+field+" set."))

	case "move":
		// Short form: `panel move <name>` (moves to current channel).
		if len(args) < 2 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel move <name> [#channel]`"))
		}
		name := args[1]
		channelArg := ""
		if len(args) >= 3 {
			channelArg = args[2]
		}
		chID := parseChannelRef(channelArg, ctx.ChannelID)
		if chID == "" {
			return ctx.Respond(embed.Error("❌ Error", "Could not parse the channel."))
		}
		cfg := m.guildConfig(ctx.GuildID)
		m.mu.Lock()
		p, ok := cfg.Panels[name]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown panel `"+name+"`."))
		}
		p.ChannelID = chID
		p.MessageID = ""
		cfg.Panels[name] = p
		m.mu.Unlock()
		if err := m.postOrUpdatePanel(ctx.GuildID, &p); err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Moved", "`"+name+"` now lives in <#"+chID+">."))

	case "resend":
		name := ""
		if len(args) >= 2 {
			name = args[1]
		}
		cfg := m.guildConfig(ctx.GuildID)
		m.mu.Lock()
		p, ok := cfg.Panels[name]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown panel `"+name+"`."))
		}
		p.MessageID = "" // force repost
		cfg.Panels[name] = p
		m.mu.Unlock()
		if err := m.postOrUpdatePanel(ctx.GuildID, &p); err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Resent", "`"+name+"` posted fresh in <#"+p.ChannelID+">."))

	case "suspend", "resume":
		if len(args) < 2 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel suspend|resume <name>`"))
		}
		p, err := m.setPanelSuspended(ctx.GuildID, args[1], args[0] == "suspend")
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		state := "resumed 🟢"
		if p.Suspended {
			state = "suspended ⏸ (buttons disabled; other panels unaffected)"
		}
		return ctx.Respond(embed.Success("✅ "+titleWord(args[0]), "`"+p.Name+"` "+state))

	case "remove":
		if len(args) < 2 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel remove <name>`"))
		}
		cfg := m.guildConfig(ctx.GuildID)
		m.mu.Lock()
		p, ok := cfg.Panels[args[1]]
		if ok {
			delete(cfg.Panels, args[1])
		}
		_ = m.saveGuildLocked(ctx.GuildID)
		m.mu.Unlock()
		if !ok {
			return ctx.Respond(embed.Error("❌ Error", "Unknown panel `"+args[1]+"`."))
		}
		if p.MessageID != "" {
			m.tryDeleteMessage(p.ChannelID, p.MessageID)
		}
		return ctx.Respond(embed.Success("✅ Removed", "Panel `"+args[1]+"` deleted (its embed was removed too)."))
	}
	return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel create|set|move|resend|suspend|resume|remove|list …`"))
}

// ── type subcommand tree ─────────────────────────────────────────────────

var typeFields = []string{"label", "category", "ping", "helper", "access", "welcome", "body", "color", "button", "emoji"}

func (m *TicketsModule) cmdType(ctx *commands.Context) error {
	if ctx.GuildID == "" {
		return ctx.Respond(embed.Error("❌ Error", "Server-only command."))
	}
	args := ctx.Args[1:]
	if len(args) == 0 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type add|set|enable|disable|remove|list|show …`"))
	}
	cfg := m.guildConfig(ctx.GuildID)
	switch args[0] {
	case "add":
		if len(args) < 2 || !validTypeKey(args[1]) {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type add <key>` — key: letters/digits/`-`/`_`."))
		}
		key := strings.ToLower(args[1])
		m.mu.Lock()
		if _, exists := cfg.Types[key]; exists {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Type `"+key+"` already exists."))
		}
		cfg.Types[key] = &TypeConfig{
			Key: key, Label: titleCase(key),
			Color:       colorValue(defaultTicketColor),
			ButtonLabel: titleCase(key),
			AllowClaim:  boolPtr(true), AllowClose: boolPtr(true),
		}
		err := m.saveGuildLocked(ctx.GuildID)
		m.mu.Unlock()
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Type created", "`"+key+"` created (disabled). Next:\n"+
			"`tickets type set "+key+" category <#category-id>`\n"+
			"`tickets type set "+key+" label <Name>`\n"+
			"`tickets type enable "+key+"`"))

	case "set":
		if len(args) < 4 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type set <key> <field> <value…>`\nFields: "+strings.Join(typeFields, ", ")))
		}
		key, field := strings.ToLower(args[1]), strings.ToLower(args[2])
		val := strings.Join(args[3:], " ")
		m.mu.Lock()
		t, ok := cfg.Types[key]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown type `"+key+"`. Valid: "+typeKeysLocked(cfg)))
		}
		err := applyTypeField(t, field, val)
		if err == nil {
			err = m.saveGuildLocked(ctx.GuildID)
		}
		m.mu.Unlock()
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Updated", "`"+key+"."+field+"` set."))

	case "enable", "disable":
		if len(args) < 2 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type "+args[0]+" <key>`"))
		}
		key := strings.ToLower(args[1])
		m.mu.Lock()
		t, ok := cfg.Types[key]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown type `"+key+"`."))
		}
		if args[0] == "enable" && strings.TrimSpace(t.Category) == "" {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Set a category first: `tickets type set "+key+" category <#category>` (paste the **ID**, right-click → Copy ID)."))
		}
		t.Enabled = args[0] == "enable"
		enabled := t.Enabled // snapshot: no reads after unlock
		err := m.saveGuildLocked(ctx.GuildID)
		m.mu.Unlock()
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		state := "disabled 🔴"
		if enabled {
			state = "enabled 🟢"
		}
		return ctx.Respond(embed.Success("✅ "+titleWord(args[0])+"d", "Type `"+key+"` is now "+state+"."))

	case "remove":
		if len(args) < 2 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type remove <key>`"))
		}
		key := strings.ToLower(args[1])
		m.mu.Lock()
		if _, ok := cfg.Types[key]; !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown type `"+key+"`."))
		}
		delete(cfg.Types, key)
		for name, p := range cfg.Panels { // panels bound to it die too
			if p.TypeKey == key {
				delete(cfg.Panels, name)
			}
		}
		err := m.saveGuildLocked(ctx.GuildID)
		m.mu.Unlock()
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Removed", "Type `"+key+"` (and its panels) removed."))

	case "list":
		types := m.typesSnapshot(ctx.GuildID)
		if len(types) == 0 {
			return ctx.Respond(embed.Info("Types", "None yet — `tickets type add <key>`."))
		}
		var rows []string
		for _, t := range types {
			state := "🔴"
			if t.Enabled && t.Category != "" {
				state = "🟢"
			}
			rows = append(rows, fmt.Sprintf("%s `%s` — %s (category %s, %d ping, %d helpers)",
				state, t.Key, t.Label, orDash(t.Category), len(t.PingRoles), len(t.HelperRoles)))
		}
		return ctx.Respond(embed.Info("Types", strings.Join(rows, "\n")))

	case "show":
		if len(args) < 2 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type show <key>`"))
		}
		t, ok := m.typeOf(ctx.GuildID, strings.ToLower(args[1]))
		if !ok {
			return ctx.Respond(embed.Error("❌ Error", "Unknown type."))
		}
		body := strings.Join([]string{
			"Label: **" + t.Label + "**",
			"Enabled: " + map[bool]string{true: "yes", false: "no"}[t.Enabled],
			"Category: `" + orDash(t.Category) + "`",
			"Ping roles: " + rolesOrNone(t.PingRoles),
			"Helper roles: " + rolesOrNone(t.HelperRoles),
			"Access roles: " + rolesOrNone(t.AccessRoles) + " (empty = everyone)",
			"Button: **" + firstNonEmpty(t.ButtonLabel, t.Label) + "** " + orDash(t.ButtonEmoji),
			"Color: `#" + fmt.Sprintf("%06X", int(t.Color)) + "`",
			"Welcome: " + codeOrNone(t.WelcomeMsg),
			"Embed body: " + codeOrNone(t.EmbedBody),
		}, "\n")
		return ctx.Respond(embed.Info("Type `"+t.Key+"`", body))
	}
	return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type add|set|enable|disable|remove|list|show …`"))
}

// applyTypeField mutates one typed field on TypeConfig (shared by dashboard).
func applyTypeField(t *TypeConfig, field, val string) error {
	switch field {
	case "label":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("label cannot be empty")
		}
		t.Label = val
		if t.ButtonLabel == "" || t.ButtonLabel == t.Key {
			t.ButtonLabel = val
		}
	case "category":
		id := extractSnowflake(val)
		if id == "" {
			return fmt.Errorf("mention the category (#…) or paste its ID")
		}
		t.Category = id
	case "ping":
		t.PingRoles = splitRoleList(val)
	case "helper":
		t.HelperRoles = splitRoleList(val)
	case "access":
		t.AccessRoles = splitRoleList(val)
	case "welcome":
		t.WelcomeMsg = val
	case "body":
		t.EmbedBody = val
	case "color":
		c, err := colorFromHex(val)
		if err != nil {
			return err
		}
		t.Color = colorValue(c)
	case "button":
		if strings.TrimSpace(val) == "" || len(val) > 80 {
			return fmt.Errorf("button label must be 1–80 chars")
		}
		t.ButtonLabel = val
	case "emoji":
		if !validEmoji(val) {
			return fmt.Errorf("not a valid emoji (unicode or <:name:id>)")
		}
		t.ButtonEmoji = strings.TrimSpace(val)
	default:
		return fmt.Errorf("unknown field %q — valid: %s", field, strings.Join(typeFields, ", "))
	}
	return nil
}

// ── access (per-type opener gate) ─────────────────────────────────────────

func (m *TicketsModule) cmdAccess(ctx *commands.Context) error {
	if ctx.GuildID == "" {
		return ctx.Respond(embed.Error("❌ Error", "Server-only command."))
	}
	args := ctx.Args[1:]
	if len(args) == 0 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets access add|remove <type> <role…>` · `tickets access list`\nAccess roles decide who may OPEN tickets (empty = everyone). Helpers are set per-type via `type set helper`."))
	}
	cfg := m.guildConfig(ctx.GuildID)
	switch args[0] {
	case "list":
		m.mu.RLock()
		var rows []string
		for k, t := range cfg.Types {
			if t != nil && len(t.AccessRoles) > 0 {
				rows = append(rows, "`"+k+"`: "+rolesOrNone(t.AccessRoles))
			}
		}
		m.mu.RUnlock()
		if len(rows) == 0 {
			return ctx.Respond(embed.Info("Access", "All types open to everyone. Restrict per-type:\n`tickets type set <key> access @Role`"))
		}
		return ctx.Respond(embed.Info("Access", strings.Join(rows, "\n")))
	case "add", "remove":
		if len(args) < 3 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets access add|remove <type> <role…>`"))
		}
		key := strings.ToLower(args[1])
		roleIDs := extractSnowflakes(strings.Join(args[2:], " "))
		if len(roleIDs) == 0 {
			return ctx.Respond(embed.Error("❌ Error", "Mention role(s) (@Role) or paste IDs."))
		}
		m.mu.Lock()
		t, ok := cfg.Types[key]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown type `"+key+"`."))
		}
		set := map[string]bool{}
		for _, r := range t.AccessRoles {
			set[r] = true
		}
		for _, r := range roleIDs {
			if args[0] == "add" {
				set[r] = true
			} else {
				delete(set, r)
			}
		}
		t.AccessRoles = mapKeysSorted(set)
		accessSnapshot := t.AccessRoles // snapshot: no reads after unlock
		err := m.saveGuildLocked(ctx.GuildID)
		m.mu.Unlock()
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Access updated", "`"+key+"` openers: "+rolesOrNone(accessSnapshot)))
	}
	return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets access add|remove <type> <role…>` · `tickets access list`"))
}

func (m *TicketsModule) cmdLogChannel(ctx *commands.Context) error {
	if ctx.GuildID == "" {
		return ctx.Respond(embed.Error("❌ Error", "Server-only command."))
	}
	cfg := m.guildConfig(ctx.GuildID)
	if len(ctx.Args) < 2 {
		m.mu.RLock()
		cur := cfg.LogChannel
		m.mu.RUnlock()
		msg := "Current: "
		if cur == "" {
			msg += "*not set*"
		} else {
			msg += "<#" + cur + ">"
		}
		return ctx.Respond(embed.Info("Log channel", msg+"\n\nSet it: `tickets logchannel #channel`"))
	}
	id := extractSnowflake(ctx.Args[1])
	if id == "" {
		return ctx.Respond(embed.Error("❌ Error", "Mention the channel (#…) or paste its ID."))
	}
	m.mu.Lock()
	cfg.LogChannel = id
	err := m.saveGuildLocked(ctx.GuildID)
	m.mu.Unlock()
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", err.Error()))
	}
	return ctx.Respond(embed.Success("✅ Log channel set", "Transcripts will be posted in <#"+id+">."))
}

func (m *TicketsModule) cmdReload(ctx *commands.Context) error {
	if ctx.GuildID == "" {
		return ctx.Respond(embed.Error("❌ Error", "Server-only command."))
	}
	fresh := loadGuildConfig(m.ctx.DataDir, ctx.GuildID, m.ctx.Logger)
	m.mu.Lock()
	m.guilds[ctx.GuildID] = fresh
	m.mu.Unlock()
	return ctx.Respond(embed.Success("✅ Reloaded", fmt.Sprintf("%d types, %d panels.", len(fresh.Types), len(fresh.Panels))))
}

// typeKeysLocked lists keys of a loaded config (error-text helper; caller
// holds m.mu).
func typeKeysLocked(cfg *Config) string {
	keys := make([]string, 0, len(cfg.Types))
	for k := range cfg.Types {
		keys = append(keys, "`"+k+"`")
	}
	if len(keys) == 0 {
		return "(none)"
	}
	return strings.Join(keys, ", ")
}

// tryDeleteMessage best-effort deletes the panel embed after removal.
func (m *TicketsModule) tryDeleteMessage(channelID, messageID string) {
	cid, e1 := snowflake.Parse(channelID)
	mid, e2 := snowflake.Parse(messageID)
	if e1 != nil || e2 != nil {
		return
	}
	if err := m.ctx.Rest.DeleteMessage(cid, mid); err != nil {
		m.ctx.Logger.Warn("Tickets: panel embed delete failed: %v", err)
	}
}

// ── WebConfigurable (global module settings) ──────────────────────────────

func (m *TicketsModule) WebConfigSchema() []modules.ConfigField {
	return []modules.ConfigField{
		{Key: "storage_retention_days", Label: "Retention (days)", Help: "Days to keep closed tickets before pruning (0 = keep forever).", Type: modules.FieldTypeNumber, Scope: "global", Placeholder: "30", Min: new(0.0), Step: new(1.0)},
		{Key: "modals_enabled", Label: "Open-time question modals", Help: "Allow panels to ask questions via a modal when a ticket is opened.", Type: modules.FieldTypeToggle, Scope: "global"},
		{Key: "allow_dashboard_close", Label: "Allow dashboard close", Help: "Allow tickets to be closed from the dashboard.", Type: modules.FieldTypeToggle, Scope: "global"},
	}
}

func (m *TicketsModule) WebGetConfig(guildID string) (map[string]string, error) {
	if guildID != "" {
		return map[string]string{}, nil
	}
	if !m.isLoaded() {
		return nil, fmt.Errorf("tickets module is not loaded")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]string{
		"storage_retention_days": strconv.Itoa(m.module.RetentionDays()),
		"modals_enabled":         strconv.FormatBool(m.module.ModalsOn()),
		"allow_dashboard_close":  strconv.FormatBool(m.module.AllowDashClose),
	}, nil
}

func (m *TicketsModule) WebSetConfig(guildID, key, value string) error {
	if guildID != "" {
		return fmt.Errorf("tickets settings are global; guild %q is not supported", guildID)
	}
	// Same nil-deref hazard as the read path: m.module is nil until OnLoad and
	// after OnUnload, so a dashboard write in that window must error, not panic.
	// Checked BEFORE taking m.mu (isLoaded takes the read lock).
	if !m.isLoaded() {
		return fmt.Errorf("tickets module is not loaded")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch key {
	case "storage_retention_days":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("storage_retention_days must be a non-negative integer")
		}
		m.module.Retention = retentionDays{value: n, set: true}
	case "modals_enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("modals_enabled must be a boolean")
		}
		m.module.ModalsEnabled = new(b)
	case "allow_dashboard_close":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("allow_dashboard_close must be a boolean")
		}
		m.module.AllowDashClose = b
	default:
		return fmt.Errorf("unknown tickets setting %q", key)
	}
	return m.module.save(m.ctx.DataDir)
}
