package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/modules"
)

func fPtr(f float64) *float64 { return &f }

// Commands: owner-only conveniences only — normal usage is 100% buttons.
func (m *TicketsModule) prefixCommands() []commands.Command {
	return []commands.Command{
		{
			Name: "tickets", Description: "Ticket system utilities (owner)",
			Usage: "tickets panel|reload", Category: "Tickets",
			SuperOwnerOnly: true,
			Execute: func(ctx *commands.Context) error {
				if len(ctx.Args) == 0 {
					return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel` — post/refresh the control panel\n`tickets reload` — reload groups from config"))
				}
				switch ctx.Args[0] {
				case "panel":
					if ctx.GuildID == "" {
						return ctx.Respond(embed.Error("❌ Error", "Server-only command."))
					}
					id, err := m.postOrUpdatePanel(ctx.GuildID)
					if err != nil {
						return ctx.Respond(embed.Error("❌ Error", err.Error()))
					}
					return ctx.Respond(embed.Success("✅ Panel posted", fmt.Sprintf("Control panel message ID: `%s`", id)))
				case "reload":
					cfg, err := loadConfig(m.dataDir())
					if err != nil {
						return ctx.Respond(embed.Error("❌ Error", err.Error()))
					}
					m.mu.Lock()
					m.cfg = cfg
					m.mu.Unlock()
					return ctx.Respond(embed.Success("✅ Reloaded", fmt.Sprintf("%d groups configured.", len(cfg.parsed))))
				}
				return ctx.Respond(embed.Warning("⚠️ Usage", "`tickets panel|reload`"))
			},
		},
	}
}

// ── modules.WebConfigurable ───────────────────────────────────────────────

func (m *TicketsModule) WebConfigSchema() []modules.ConfigField {
	retention := float64(3650)
	return []modules.ConfigField{
		{Key: "groups_yaml", Label: "Ticket groups (YAML)", Help: "List of groups: key, label, enabled, parent_channel, ping_roles, embed_template, color, allow_claim, allow_close.",
			Type: modules.FieldTypeTextarea, Scope: "global"},
		{Key: "control_channel", Label: "Control panel channel", Help: "Where the staff Open-button panel lives.",
			Type: modules.FieldTypeChannel, Scope: "guild", GuildScoped: true},
		{Key: "log_channel", Label: "Log channel (optional)", Help: "Closed-ticket summaries are posted here if set.",
			Type: modules.FieldTypeChannel, Scope: "guild", GuildScoped: true},
		{Key: "storage_retention_days", Label: "Transcript retention (days)", Help: "0 = keep forever. Closed tickets older than this are pruned on load.",
			Type: modules.FieldTypeNumber, Min: fPtr(0), Max: &retention, Scope: "global"},
		{Key: "allow_dashboard_close", Label: "Staff may close via dashboard", Help: "Off = dashboard closing is restricted to owner/elevated.",
			Type: modules.FieldTypeToggle, Scope: "global"},
	}
}

func (m *TicketsModule) WebGetConfig(guildID string) (map[string]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	vals := map[string]string{
		"groups_yaml":            m.cfg.GroupsYAML,
		"storage_retention_days": strconv.Itoa(m.cfg.RetentionDays()),
		"allow_dashboard_close":  strconv.FormatBool(m.cfg.AllowDashClose),
	}
	if gcfg, ok := m.cfg.Guilds[guildID]; ok && gcfg != nil && guildID != "" {
		vals["control_channel"] = gcfg.ControlChannel
		vals["log_channel"] = gcfg.LogChannel
	}
	return vals, nil
}

func (m *TicketsModule) WebSetConfig(guildID, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch key {
	case "groups_yaml":
		groups, err := parseGroupsYAML(value)
		if err != nil {
			return err
		}
		m.cfg.GroupsYAML = value
		m.cfg.parsed = groups
	case "storage_retention_days":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 0 || n > 3650 {
			return fmt.Errorf("retention must be 0–3650 days")
		}
		m.cfg.Retention = retentionDays{value: n, set: true}
	case "allow_dashboard_close":
		b := strings.EqualFold(strings.TrimSpace(value), "true") || strings.TrimSpace(value) == "1"
		m.cfg.AllowDashClose = b
	case "control_channel", "log_channel":
		if guildID == "" {
			return fmt.Errorf("%s is a per-server setting — pick a server first", key)
		}
		g := m.cfg.guildCfg(guildID)
		if key == "control_channel" {
			g.ControlChannel = strings.TrimSpace(value)
		} else {
			g.LogChannel = strings.TrimSpace(value)
		}
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return m.cfg.save(m.ctx.DataDir)
}

// dataDir returns the module data dir (locked read).
func (m *TicketsModule) dataDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ctx.DataDir
}
