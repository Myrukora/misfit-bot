package moderation

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// ModerationModule provides ban/kick/timeout/warn commands, a persistent case
// audit trail, warn notifications and automatic escalation rules. Every setting
// lives on the dashboard; the data dir is modules/moderation/.
type ModerationModule struct {
	ctx      *modules.Context
	cfgStore *configStore
	cases    *caseStore

	mu      sync.Mutex
	lastErr map[string]string
	// warnMu serializes the warn → count → rule sequence so two concurrent
	// warnings cannot both trigger the same threshold.
	warnMu sync.Mutex
}

// New constructs the module (registered as a compiled-in builtin).
func New() modules.Module { return &ModerationModule{} }

func (m *ModerationModule) Name() string    { return "moderation" }
func (m *ModerationModule) Version() string { return "1.0.0" }
func (m *ModerationModule) Description() string {
	return "Ban, kick, timeout, warnings, audit log and escalation rules"
}
func (m *ModerationModule) Author() string { return "misfit" }

func (m *ModerationModule) OnLoad(ctx *modules.Context) error {
	m.ctx = ctx
	m.lastErr = map[string]string{}
	cfgStore, err := openConfigStore(filepath.Join(ctx.DataDir, "guilds"))
	if err != nil {
		return err
	}
	cases, err := openCaseStore(filepath.Join(ctx.DataDir, "cases"))
	if err != nil {
		return err
	}
	m.cfgStore = cfgStore
	m.cases = cases
	ctx.Logger.Info("Moderation module loaded")
	return nil
}

func (m *ModerationModule) OnUnload() error { return nil }

// OnDisable implements modules.Disablable; hooks/commands are managed by the manager.
func (m *ModerationModule) OnDisable() error { return nil }

func (m *ModerationModule) Dependencies() []string { return nil }

// ── modules.ModerationAdmin ──────────────────────────────────────────────

var _ modules.ModerationAdmin = (*ModerationModule)(nil)

func (m *ModerationModule) GetGuildConfig(guildID string) (modules.ModerationConfig, error) {
	if m.cfgStore == nil {
		return modules.ModerationConfig{}, fmt.Errorf("moderation module not loaded")
	}
	return m.cfgStore.Get(guildID)
}

func (m *ModerationModule) SetGuildConfig(guildID string, cfg modules.ModerationConfig) error {
	if m.cfgStore == nil {
		return fmt.Errorf("moderation module not loaded")
	}
	return m.cfgStore.Save(guildID, cfg)
}

func (m *ModerationModule) ListCases(guildID string, filter modules.ModerationCaseFilter) ([]modules.ModerationCase, error) {
	if m.cases == nil {
		return nil, fmt.Errorf("moderation module not loaded")
	}
	return m.cases.List(guildID, filter)
}

func (m *ModerationModule) RemoveWarning(guildID string, caseID int, byID, byTag, reason string) error {
	if m.cases == nil {
		return fmt.Errorf("moderation module not loaded")
	}
	c, err := m.cases.RemoveWarning(guildID, caseID, byID, byTag, reason)
	if err != nil {
		return err
	}
	m.publishRemoved(guildID, c)
	return nil
}

func (m *ModerationModule) LastError(guildID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr[guildID]
}

func (m *ModerationModule) setLastError(guildID, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg == "" {
		delete(m.lastErr, guildID)
		return
	}
	m.lastErr[guildID] = msg
}

// ── delivery helpers ─────────────────────────────────────────────────────

func (m *ModerationModule) guildName(guildID snowflake.ID) string {
	if g, err := m.getGuild(guildID); err == nil && g != nil {
		return g.Name
	}
	return ""
}

func (m *ModerationModule) userTag(guildID, userID snowflake.ID) string {
	if member := m.ctx.Bot.GetCachedMember(guildID.String(), userID.String()); member != nil {
		return member.User.Tag()
	}
	if m.ctx.Rest != nil {
		if user, err := m.ctx.Rest.GetUser(userID); err == nil && user != nil {
			return user.Tag()
		}
	}
	return userID.String()
}

func (m *ModerationModule) sendEmbed(guildID string, channelID string, e discord.Embed) error {
	chID, err := snowflake.Parse(channelID)
	if err != nil {
		return fmt.Errorf("invalid destination channel")
	}
	if m.ctx.Rest == nil {
		return fmt.Errorf("Discord REST client unavailable")
	}
	_, err = m.ctx.Rest.CreateMessage(chID, discord.MessageCreate{
		Embeds:          []discord.Embed{e},
		AllowedMentions: &discord.AllowedMentions{Parse: []discord.AllowedMentionType{}},
	})
	if err != nil {
		m.setLastError(guildID, err.Error())
		m.ctx.Logger.Error("Moderation: failed to post to %s (guild %s): %v", chID, guildID, err)
		return err
	}
	return nil
}

// publishCase posts a warn notification and/or the audit embed for a new case.
// LastError is cleared only when every attempted destination succeeded.
func (m *ModerationModule) publishCase(guildID string, cfg modules.ModerationConfig, c modules.ModerationCase, warnCount int, note string) {
	gid, _ := snowflake.Parse(guildID)
	guildName := m.guildName(gid)
	warnChannel := firstNonEmpty(cfg.WarnChannel, cfg.AuditChannel)
	warnPosted := false
	attempted := false
	failed := false
	if c.Type == modules.ModerationTypeWarn && cfg.WarnNotify && warnChannel != "" {
		attempted = true
		if err := m.sendEmbed(guildID, warnChannel, warnEmbed(c, warnCount, note, guildName)); err == nil {
			warnPosted = true
		} else {
			failed = true
		}
	}
	if cfg.AuditChannel != "" {
		if !(c.Type == modules.ModerationTypeWarn && warnPosted && cfg.AuditChannel == warnChannel) {
			attempted = true
			if err := m.sendEmbed(guildID, cfg.AuditChannel, auditEmbed(c, guildName)); err != nil {
				failed = true
			}
		}
	}
	if attempted && !failed {
		m.setLastError(guildID, "")
	}
}

func (m *ModerationModule) publishRemoved(guildID string, c modules.ModerationCase) {
	cfg, err := m.cfgStore.Get(guildID)
	if err != nil || cfg.AuditChannel == "" {
		return
	}
	if err := m.sendEmbed(guildID, cfg.AuditChannel, warningRemovedEmbed(c, m.guildName(mustParseID(guildID)))); err == nil {
		m.setLastError(guildID, "")
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// configFor resolves the guild config for a command and rejects non-guild and
// disabled contexts.
func (m *ModerationModule) configFor(ctx *commands.Context) (modules.ModerationConfig, snowflake.ID, error) {
	gid, err := snowflake.Parse(ctx.GuildID)
	if err != nil || ctx.GuildID == "" {
		return modules.ModerationConfig{}, 0, fmt.Errorf("moderation commands only work in a server")
	}
	cfg, err := m.cfgStore.Get(gid.String())
	if err != nil {
		return modules.ModerationConfig{}, gid, err
	}
	if !cfg.Enabled {
		return cfg, gid, fmt.Errorf("moderation is disabled on this server — enable it on the dashboard first")
	}
	return cfg, gid, nil
}

func (m *ModerationModule) findRule(rules []modules.ModerationRule, threshold int) *modules.ModerationRule {
	for i := range rules {
		if rules[i].Threshold == threshold {
			return &rules[i]
		}
	}
	return nil
}

// ── command parsing helpers ──────────────────────────────────────────────

func argAt(args []string, i int) string {
	if i < 0 || i >= len(args) {
		return ""
	}
	return strings.TrimSpace(args[i])
}

func parseUserArg(args []string, i int) (snowflake.ID, error) {
	raw := argAt(args, i)
	if raw == "" {
		return 0, fmt.Errorf("member is required")
	}
	id, err := snowflake.Parse(raw)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("invalid member")
	}
	return id, nil
}

func parseCaseArg(args []string, i int) (int, error) {
	raw := argAt(args, i)
	id, err := strconv.Atoi(raw)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid case number")
	}
	return id, nil
}

// userDisplayReason trims a reason and caps it at Discord's audit-reason limit.
func userDisplayReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > 450 {
		reason = string([]rune(reason)[:450])
	}
	return reason
}

func modActionOpts(reason string) []rest.RequestOpt {
	if strings.TrimSpace(reason) == "" {
		return nil
	}
	return []rest.RequestOpt{rest.WithReason(reason)}
}

// mustParseID is only fed guild IDs that were already validated.
func mustParseID(s string) snowflake.ID {
	id, _ := snowflake.Parse(s)
	return id
}
