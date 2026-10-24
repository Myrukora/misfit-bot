package moderation

import (
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/omit"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

type warnOutcome struct {
	Case  modules.ModerationCase
	Count int
	Note  string
}

func actionLabel(action string) string {
	switch action {
	case modules.ModerationActionTimeout:
		return "Timeout"
	case modules.ModerationActionKick:
		return "Kick"
	case modules.ModerationActionBan:
		return "Ban"
	default:
		return action
	}
}

func (m *ModerationModule) newCase(gid snowflake.ID, typ string, targetID snowflake.ID, targetTag, modID, modTag, reason string, expires *time.Time) modules.ModerationCase {
	return modules.ModerationCase{
		GuildID:      gid.String(),
		Type:         typ,
		TargetID:     targetID.String(),
		TargetTag:    targetTag,
		ModeratorID:  modID,
		ModeratorTag: modTag,
		Reason:       userDisplayReason(reason),
		ExpiresAt:    expires,
	}
}

// ban applies a ban and records/publishes a case.
func (m *ModerationModule) ban(cfg modules.ModerationConfig, gid, modID snowflake.ID, modTag string, targetID snowflake.ID, reason string) (modules.ModerationCase, error) {
	if err := m.ensureCanPunish(gid, modID, targetID); err != nil {
		return modules.ModerationCase{}, err
	}
	if err := m.ensureBotPermission(gid, discord.PermissionBanMembers); err != nil {
		return modules.ModerationCase{}, err
	}
	reason = userDisplayReason(reason)
	deleteDur := time.Duration(cfg.BanDeleteSeconds) * time.Second
	if err := m.ctx.Rest.AddBan(gid, targetID, deleteDur, modActionOpts(reason)...); err != nil {
		return modules.ModerationCase{}, fmt.Errorf("Discord rejected the ban: %w", err)
	}
	c := m.newCase(gid, modules.ModerationTypeBan, targetID, m.userTag(gid, targetID), modID.String(), modTag, reason, nil)
	c, err := m.cases.Add(c)
	if err != nil {
		return c, err
	}
	m.publishCase(gid.String(), cfg, c, 0, "")
	return c, nil
}

// kick removes a member and records/publishes a case.
func (m *ModerationModule) kick(cfg modules.ModerationConfig, gid, modID snowflake.ID, modTag string, targetID snowflake.ID, reason string) (modules.ModerationCase, error) {
	if err := m.ensureCanPunish(gid, modID, targetID); err != nil {
		return modules.ModerationCase{}, err
	}
	if err := m.ensureBotPermission(gid, discord.PermissionKickMembers); err != nil {
		return modules.ModerationCase{}, err
	}
	reason = userDisplayReason(reason)
	if err := m.ctx.Rest.RemoveMember(gid, targetID, modActionOpts(reason)...); err != nil {
		return modules.ModerationCase{}, fmt.Errorf("Discord rejected the kick: %w", err)
	}
	c := m.newCase(gid, modules.ModerationTypeKick, targetID, m.userTag(gid, targetID), modID.String(), modTag, reason, nil)
	c, err := m.cases.Add(c)
	if err != nil {
		return c, err
	}
	m.publishCase(gid.String(), cfg, c, 0, "")
	return c, nil
}

// timeout applies a communication timeout for minutes and records the case.
func (m *ModerationModule) timeout(cfg modules.ModerationConfig, gid, modID snowflake.ID, modTag string, targetID snowflake.ID, minutes int, reason string) (modules.ModerationCase, error) {
	if minutes < 1 || minutes > maxTimeoutMinutes {
		return modules.ModerationCase{}, fmt.Errorf("timeout must be between 1 and %d minutes", maxTimeoutMinutes)
	}
	if err := m.ensureCanPunish(gid, modID, targetID); err != nil {
		return modules.ModerationCase{}, err
	}
	if err := m.ensureBotPermission(gid, discord.PermissionModerateMembers); err != nil {
		return modules.ModerationCase{}, err
	}
	reason = userDisplayReason(reason)
	until := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
	update := discord.MemberUpdate{CommunicationDisabledUntil: omit.NewPtr(until)}
	if _, err := m.ctx.Rest.UpdateMember(gid, targetID, update, modActionOpts(reason)...); err != nil {
		return modules.ModerationCase{}, fmt.Errorf("Discord rejected the timeout: %w", err)
	}
	c := m.newCase(gid, modules.ModerationTypeTimeout, targetID, m.userTag(gid, targetID), modID.String(), modTag, reason, &until)
	c, err := m.cases.Add(c)
	if err != nil {
		return c, err
	}
	m.publishCase(gid.String(), cfg, c, 0, "")
	return c, nil
}

// unban lifts a ban and records the case (no hierarchy applies to unbanning).
func (m *ModerationModule) unban(cfg modules.ModerationConfig, gid, modID snowflake.ID, modTag string, targetID snowflake.ID, reason string) (modules.ModerationCase, error) {
	if err := m.ensureBotPermission(gid, discord.PermissionBanMembers); err != nil {
		return modules.ModerationCase{}, err
	}
	reason = userDisplayReason(reason)
	if err := m.ctx.Rest.DeleteBan(gid, targetID, modActionOpts(reason)...); err != nil {
		return modules.ModerationCase{}, fmt.Errorf("Discord rejected the unban: %w", err)
	}
	c := m.newCase(gid, modules.ModerationTypeUnban, targetID, m.userTag(gid, targetID), modID.String(), modTag, reason, nil)
	c, err := m.cases.Add(c)
	if err != nil {
		return c, err
	}
	m.publishCase(gid.String(), cfg, c, 0, "")
	return c, nil
}

// warn records a warning, posts the notification and evaluates escalation
// rules. A rule failure never loses the warning.
func (m *ModerationModule) warn(cfg modules.ModerationConfig, gid, modID snowflake.ID, modTag string, targetID snowflake.ID, reason string) (warnOutcome, error) {
	m.warnMu.Lock()
	defer m.warnMu.Unlock()
	if err := m.ensureCanWarn(gid, modID, targetID); err != nil {
		return warnOutcome{}, err
	}
	reason = userDisplayReason(reason)
	if reason == "" {
		return warnOutcome{}, fmt.Errorf("a reason is required to warn a member")
	}
	var expires *time.Time
	if cfg.WarnExpiryDays > 0 {
		t := time.Now().UTC().AddDate(0, 0, cfg.WarnExpiryDays)
		expires = &t
	}
	c := m.newCase(gid, modules.ModerationTypeWarn, targetID, m.userTag(gid, targetID), modID.String(), modTag, reason, expires)
	c, err := m.cases.Add(c)
	if err != nil {
		return warnOutcome{}, err
	}
	count, err := m.cases.CountActiveWarnings(gid.String(), targetID.String(), time.Now().UTC())
	if err != nil {
		m.ctx.Logger.Error("Moderation: count warnings for %s: %v", targetID, err)
	}
	note := ""
	if rule := m.findRule(cfg.Rules, count); rule != nil {
		if rc, ruleErr := m.executeRule(cfg, gid, targetID, c, *rule, count); ruleErr != nil {
			note = fmt.Sprintf("%s rule failed: %v", actionLabel(rule.Action), ruleErr)
			m.setLastError(gid.String(), note)
			m.ctx.Logger.Error("Moderation: auto %s for guild %s failed: %v", rule.Action, gid, ruleErr)
		} else {
			note = fmt.Sprintf("%s applied automatically at %d warnings (case #%d)", actionLabel(rule.Action), count, rc.ID)
		}
	}
	m.publishCase(gid.String(), cfg, c, count, note)
	return warnOutcome{Case: c, Count: count, Note: note}, nil
}

// executeRule performs an escalation action against the warned member.
func (m *ModerationModule) executeRule(cfg modules.ModerationConfig, gid, targetID snowflake.ID, warning modules.ModerationCase, rule modules.ModerationRule, count int) (modules.ModerationCase, error) {
	selfID, err := snowflake.Parse(m.ctx.Bot.GetSelfUserID())
	if err != nil || selfID == 0 {
		return modules.ModerationCase{}, fmt.Errorf("bot identity unavailable")
	}
	if err := m.ensureCanPunish(gid, selfID, targetID); err != nil {
		return modules.ModerationCase{}, err
	}
	var perm discord.Permissions
	switch rule.Action {
	case modules.ModerationActionTimeout:
		perm = discord.PermissionModerateMembers
	case modules.ModerationActionKick:
		perm = discord.PermissionKickMembers
	case modules.ModerationActionBan:
		perm = discord.PermissionBanMembers
	default:
		return modules.ModerationCase{}, fmt.Errorf("unknown rule action %q", rule.Action)
	}
	if err := m.ensureBotPermission(gid, perm); err != nil {
		return modules.ModerationCase{}, err
	}
	reason := fmt.Sprintf("Automatic: %d active warnings (threshold %d, warning #%d)", count, rule.Threshold, warning.ID)
	var expires *time.Time
	var typ string
	switch rule.Action {
	case modules.ModerationActionTimeout:
		until := time.Now().UTC().Add(time.Duration(rule.DurationMinutes) * time.Minute)
		update := discord.MemberUpdate{CommunicationDisabledUntil: omit.NewPtr(until)}
		if _, err := m.ctx.Rest.UpdateMember(gid, targetID, update, rest.WithReason(reason)); err != nil {
			return modules.ModerationCase{}, fmt.Errorf("timeout: %w", err)
		}
		expires = &until
		typ = modules.ModerationTypeTimeout
	case modules.ModerationActionKick:
		if err := m.ctx.Rest.RemoveMember(gid, targetID, rest.WithReason(reason)); err != nil {
			return modules.ModerationCase{}, fmt.Errorf("kick: %w", err)
		}
		typ = modules.ModerationTypeKick
	case modules.ModerationActionBan:
		if err := m.ctx.Rest.AddBan(gid, targetID, time.Duration(cfg.BanDeleteSeconds)*time.Second, rest.WithReason(reason)); err != nil {
			return modules.ModerationCase{}, fmt.Errorf("ban: %w", err)
		}
		typ = modules.ModerationTypeBan
	}
	c := modules.ModerationCase{
		GuildID:       gid.String(),
		Type:          typ,
		TargetID:      targetID.String(),
		TargetTag:     warning.TargetTag,
		ModeratorID:   selfID.String(),
		ModeratorTag:  m.ctx.Bot.GetName(),
		Reason:        reason,
		ExpiresAt:     expires,
		Automated:     true,
		RuleThreshold: rule.Threshold,
		WarningID:     warning.ID,
	}
	c, err = m.cases.Add(c)
	if err != nil {
		return c, err
	}
	m.publishCase(gid.String(), cfg, c, 0, "")
	return c, nil
}

func (m *ModerationModule) removeWarning(gid string, caseID int, byID snowflake.ID, byTag, reason string) (modules.ModerationCase, error) {
	c, err := m.cases.RemoveWarning(gid, caseID, byID.String(), byTag, userDisplayReason(reason))
	if err != nil {
		return c, err
	}
	m.publishRemoved(gid, c)
	return c, nil
}

// warningList returns the user's active warnings for /warnings.
func (m *ModerationModule) warningList(gid, userID string) ([]modules.ModerationCase, error) {
	if !validGuildID(gid) {
		return nil, fmt.Errorf("invalid guild id")
	}
	return m.cases.ActiveWarnings(gid, userID, time.Now().UTC())
}

// formatWarnings renders up to 25 warnings as embed lines.
func formatWarnings(warns []modules.ModerationCase) string {
	if len(warns) == 0 {
		return "No active warnings."
	}
	var b strings.Builder
	for i, w := range warns {
		if i >= 25 {
			fmt.Fprintf(&b, "\n…and %d more", len(warns)-25)
			break
		}
		expiry := "never expires"
		if w.ExpiresAt != nil {
			expiry = "expires <t:" + fmt.Sprint(w.ExpiresAt.Unix()) + ":R>"
		}
		fmt.Fprintf(&b, "**#%d** · %s · by %s · %s\n%s\n\n",
			w.ID, w.CreatedAt.UTC().Format("2006-01-02 15:04"), w.ModeratorTag, expiry, truncate(reasonOrNone(w.Reason), 300))
	}
	return strings.TrimSpace(b.String())
}
