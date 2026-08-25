package main

import (
	"fmt"
	"strings"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/embed"
)

// webconfig.go — [p]tickets command tree (v2) + WebConfigurable surface.
//
// CLI is the PRIMARY configuration path; the dashboard writes through the
// same helpers so behavior never diverges.
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
			Name: "tickets", Description: "Ticket system configuration (owner + elevated)",
			Usage: "tickets setup|panel|type|access|logchannel|reload", Category: "Tickets",
			OwnerOnly: true,
			Execute:   m.runTicketsCommand,
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
	}, "\n")
}

// ── setup ────────────────────────────────────────────────────────────────

// cmdSetup inspects live config state and prints a plain-language checklist
// with the exact next commands — AAA3A-style guided setup.
func (m *TicketsModule) cmdSetup(ctx *commands.Context) error {
	if ctx.GuildID == "" {
		return ctx.Respond(embed.Error("❌ Error", "Server-only command."))
	}
	m.mu.RLock()
	var (
		hasType   bool
		hasPanel  bool
		hasLog    bool
		typeNames []string
		panelRows []string
	)
	for k, t := range m.cfg.Types {
		state := "🟢"
		if !t.Enabled || t.Category == "" {
			state = "🟡"
		}
		typeNames = append(typeNames, state+" `"+k+"`")
		if t.Enabled && t.Category != "" {
			hasType = true
		}
	}
	for name, p := range m.cfg.Panels {
		susp := ""
		if p.Suspended {
			susp = " ⏸ suspended"
		}
		panelRows = append(panelRows, "`"+name+"` → <#"+p.ChannelID+"> ("+p.TypeKey+")"+susp)
		if !p.Suspended {
			hasPanel = true
		}
	}
	hasLog = m.cfg.LogChannel != ""
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
		panels := m.panelsSnapshot()
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
		if len(args) < 3 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel create <name> [#channel] <type>`\nThe type must exist first (`tickets type add <key>`)."))
		}
		name := args[1]
		if !validPanelName(name) {
			return ctx.Respond(embed.Error("❌ Error", "Panel names: letters, digits, `-`, `_` only (no spaces)."))
		}
		channelArg := args[2]
		typeKey := ""
		if len(args) >= 4 {
			typeKey = args[3]
		} else {
			// allow omitting channel: use current channel
			channelArg = "<#" + ctx.ChannelID + ">"
		}
		chID := parseChannelRef(channelArg, ctx.ChannelID)
		if chID == "" {
			return ctx.Respond(embed.Error("❌ Error", "Could not parse the channel. Mention it (#channel) or paste its ID."))
		}
		m.mu.Lock()
		if _, exists := m.cfg.Panels[name]; exists {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Panel `"+name+"` already exists — use `panel set`/`panel move`."))
		}
		p := PanelConfig{Name: name, ChannelID: chID, TypeKey: typeKey}
		m.cfg.Panels[name] = p
		m.mu.Unlock()
		if err := m.postOrUpdatePanel(ctx.GuildID, &p); err != nil {
			m.mu.Lock()
			delete(m.cfg.Panels, name)
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
		m.mu.Lock()
		p, ok := m.cfg.Panels[name]
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
		m.cfg.Panels[name] = p
		m.mu.Unlock()
		if err := m.postOrUpdatePanel(ctx.GuildID, &p); err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Updated", "Panel `"+name+"` "+field+" set."))

	case "move":
		if len(args) < 3 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel move <name> [#channel]`"))
		}
		name := args[1]
		chID := parseChannelRef(args[2], ctx.ChannelID)
		if chID == "" {
			return ctx.Respond(embed.Error("❌ Error", "Could not parse the channel."))
		}
		m.mu.Lock()
		p, ok := m.cfg.Panels[name]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown panel `"+name+"`."))
		}
		oldMsg := p.MessageID // drop old message link; we post fresh in new channel
		p.ChannelID = chID
		p.MessageID = ""
		m.cfg.Panels[name] = p
		m.mu.Unlock()
		_ = oldMsg
		if err := m.postOrUpdatePanel(ctx.GuildID, &p); err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Moved", "`"+name+"` now lives in <#"+chID+">."))

	case "resend":
		name := ""
		if len(args) >= 2 {
			name = args[1]
		}
		m.mu.Lock()
		p, ok := m.cfg.Panels[name]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown panel `"+name+"`."))
		}
		p.MessageID = "" // force repost
		m.cfg.Panels[name] = p
		m.mu.Unlock()
		if err := m.postOrUpdatePanel(ctx.GuildID, &p); err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Resent", "`"+name+"` posted fresh in <#"+p.ChannelID+">."))

	case "suspend", "resume":
		if len(args) < 2 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel suspend|resume <name>`"))
		}
		p, err := m.setPanelSuspended(args[1], args[0] == "suspend")
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		state := "resumed 🟢"
		if p.Suspended {
			state = "suspended ⏸ (buttons disabled; other panels unaffected)"
		}
		return ctx.Respond(embed.Success("✅ "+strings.Title(args[0]), "`"+p.Name+"` "+state))

	case "remove":
		if len(args) < 2 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel remove <name>`"))
		}
		m.mu.Lock()
		p, ok := m.cfg.Panels[args[1]]
		if ok {
			delete(m.cfg.Panels, args[1])
		}
		saveErr := m.cfg.save(m.ctx.DataDir)
		m.mu.Unlock()
		if !ok {
			return ctx.Respond(embed.Error("❌ Error", "Unknown panel `"+args[1]+"`."))
		}
		_ = saveErr
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
	args := ctx.Args[1:]
	if len(args) == 0 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type add|set|enable|disable|remove|list|show …`"))
	}
	switch args[0] {
	case "add":
		if len(args) < 2 || !validTypeKey(args[1]) {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type add <key>` — key: letters/digits/`-`/`_`."))
		}
		key := strings.ToLower(args[1])
		m.mu.Lock()
		if _, exists := m.cfg.Types[key]; exists {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Type `"+key+"` already exists."))
		}
		m.cfg.Types[key] = &TypeConfig{
			Key: key, Label: strings.Title(key),
			Color:       colorValue(defaultTicketColor),
			ButtonLabel: strings.Title(key),
			AllowClaim:  boolPtr(true), AllowClose: boolPtr(true),
		}
		err := m.cfg.save(m.ctx.DataDir)
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
		t, ok := m.cfg.Types[key]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown type `"+key+"`. Valid: "+m.typeKeysLocked()))
		}
		err := applyTypeField(t, field, val)
		if err == nil {
			err = m.cfg.save(m.ctx.DataDir)
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
		t, ok := m.cfg.Types[key]
		if !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown type `"+key+"`."))
		}
		if args[0] == "enable" && strings.TrimSpace(t.Category) == "" {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Set a category first: `tickets type set "+key+" category <#category>` (paste the **ID**, right-click → Copy ID)."))
		}
		t.Enabled = args[0] == "enable"
		err := m.cfg.save(m.ctx.DataDir)
		m.mu.Unlock()
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ "+strings.Title(args[0])+"d", "Type `"+key+"` is now "+map[bool]string{true: "enabled 🟢", false: "disabled 🔴"}[t.Enabled]+"."))

	case "remove":
		if len(args) < 2 {
			return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets type remove <key>`"))
		}
		key := strings.ToLower(args[1])
		m.mu.Lock()
		if _, ok := m.cfg.Types[key]; !ok {
			m.mu.Unlock()
			return ctx.Respond(embed.Error("❌ Error", "Unknown type `"+key+"`."))
		}
		delete(m.cfg.Types, key)
		for name, p := range m.cfg.Panels { // panels bound to it die too
			if p.TypeKey == key {
				delete(m.cfg.Panels, name)
			}
		}
		err := m.cfg.save(m.ctx.DataDir)
		m.mu.Unlock()
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Removed", "Type `"+key+"` (and its panels) removed."))

	case "list":
		types := m.typesSnapshot()
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
		t, ok := m.typeOf(strings.ToLower(args[1]))
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

// ── access (guild-wide quick gate) ────────────────────────────────────────

func (m *TicketsModule) cmdAccess(ctx *commands.Context) error {
	args := ctx.Args[1:]
	if len(args) == 0 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets access add|remove <role…>` · `tickets access list`\nAccess roles decide who may OPEN tickets (empty = everyone). Helpers are set per-type via `type set helper`."))
	}
	switch args[0] {
	case "list":
		m.mu.RLock()
		var rows []string
		for k, t := range m.cfg.Types {
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
		t, ok := m.cfg.Types[key]
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
		err := m.cfg.save(m.ctx.DataDir)
		m.mu.Unlock()
		if err != nil {
			return ctx.Respond(embed.Error("❌ Error", err.Error()))
		}
		return ctx.Respond(embed.Success("✅ Access updated", "`"+key+"` openers: "+rolesOrNone(t.AccessRoles)))
	}
	return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets access add|remove <type> <role…>` · `tickets access list`"))
}

func (m *TicketsModule) cmdLogChannel(ctx *commands.Context) error {
	if len(ctx.Args) < 2 {
		m.mu.RLock()
		cur := m.cfg.LogChannel
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
	m.cfg.LogChannel = id
	err := m.cfg.save(m.ctx.DataDir)
	m.mu.Unlock()
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", err.Error()))
	}
	return ctx.Respond(embed.Success("✅ Log channel set", "Transcripts will be posted in <#"+id+">."))
}

func (m *TicketsModule) cmdReload(ctx *commands.Context) error {
	cfg, err := loadConfig(m.dataDir())
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", err.Error()))
	}
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
	return ctx.Respond(embed.Success("✅ Reloaded", fmt.Sprintf("%d types, %d panels.", len(cfg.Types), len(cfg.Panels))))
}

// typeKeysLocked lists keys under an existing lock (error-text helper).
func (m *TicketsModule) typeKeysLocked() string {
	keys := make([]string, 0, len(m.cfg.Types))
	for k := range m.cfg.Types {
		keys = append(keys, "`"+k+"`")
	}
	if len(keys) == 0 {
		return "(none)"
	}
	return strings.Join(keys, ", ")
}

// dataDir returns the module data dir (locked read).
func (m *TicketsModule) dataDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ctx.DataDir
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
