package moderation

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"

	"github.com/misfit/bot/internal/util"
	"github.com/misfit/bot/modules"
)

const maxEmbedFieldValue = 1024

func caseTypeLabel(t string) string {
	switch t {
	case modules.ModerationTypeWarn:
		return "Warning"
	case modules.ModerationTypeBan:
		return "Ban"
	case modules.ModerationTypeKick:
		return "Kick"
	case modules.ModerationTypeTimeout:
		return "Timeout"
	case modules.ModerationTypeUnban:
		return "Unban"
	default:
		return "Moderation"
	}
}

func caseColor(t string) int {
	switch t {
	case modules.ModerationTypeWarn:
		return 0xFEE75C
	case modules.ModerationTypeBan:
		return 0xED4245
	case modules.ModerationTypeKick:
		return 0xE67E22
	case modules.ModerationTypeTimeout:
		return 0x3498DB
	case modules.ModerationTypeUnban:
		return 0x57F287
	default:
		return 0x95A5A6
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func reasonOrNone(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "No reason provided."
	}
	return reason
}

func userLabel(tag, id string) string {
	if tag == "" {
		tag = "unknown user"
	}
	return fmt.Sprintf("%s (`%s`)", tag, id)
}

func expiresLabel(t *time.Time) string {
	if t == nil {
		return "Never"
	}
	return "<t:" + strconv.FormatInt(t.Unix(), 10) + ":F>"
}

// auditEmbed is the permanent record posted to the audit channel for every
// moderation case.
func auditEmbed(c modules.ModerationCase, guildName string) discord.Embed {
	moderator := userLabel(c.ModeratorTag, c.ModeratorID)
	if c.Automated {
		moderator = "Automatic rule"
	}
	fields := []discord.EmbedField{
		{Name: "Target", Value: userLabel(c.TargetTag, c.TargetID), Inline: util.PtrBool(true)},
		{Name: "Moderator", Value: moderator, Inline: util.PtrBool(true)},
		{Name: "Case", Value: fmt.Sprintf("#%d", c.ID), Inline: util.PtrBool(true)},
	}
	if c.ExpiresAt != nil {
		fields = append(fields, discord.EmbedField{Name: "Expires", Value: expiresLabel(c.ExpiresAt), Inline: util.PtrBool(true)})
	}
	if c.Automated {
		fields = append(fields, discord.EmbedField{
			Name:   "Trigger",
			Value:  fmt.Sprintf("Warning #%d · threshold %d", c.WarningID, c.RuleThreshold),
			Inline: util.PtrBool(true),
		})
	}
	e := discord.NewEmbed().
		WithTitle(caseTypeLabel(c.Type) + " · case #" + strconv.Itoa(c.ID)).
		WithDescription(truncate(reasonOrNone(c.Reason), 4000)).
		WithColor(caseColor(c.Type)).
		WithFields(fields...).
		WithTimestamp(c.CreatedAt)
	if guildName != "" {
		e = e.WithFooter(guildName, "")
	}
	return e
}

// warnEmbed is the "user was warned" notification: target, moderator, reason,
// the active warning total and the case ID.
func warnEmbed(c modules.ModerationCase, count int, note, guildName string) discord.Embed {
	fields := []discord.EmbedField{
		{Name: "Target", Value: userLabel(c.TargetTag, c.TargetID), Inline: util.PtrBool(true)},
		{Name: "Moderator", Value: userLabel(c.ModeratorTag, c.ModeratorID), Inline: util.PtrBool(true)},
		{Name: "Total warnings", Value: strconv.Itoa(count), Inline: util.PtrBool(true)},
		{Name: "Case", Value: fmt.Sprintf("#%d", c.ID), Inline: util.PtrBool(true)},
		{Name: "Expires", Value: expiresLabel(c.ExpiresAt), Inline: util.PtrBool(true)},
		{Name: "Reason", Value: truncate(reasonOrNone(c.Reason), maxEmbedFieldValue), Inline: util.PtrBool(false)},
	}
	if note != "" {
		fields = append(fields, discord.EmbedField{Name: "Automatic action", Value: truncate(note, maxEmbedFieldValue), Inline: util.PtrBool(false)})
	}
	e := discord.NewEmbed().
		WithTitle("Warning issued").
		WithDescription(fmt.Sprintf("**%s** was warned.", c.TargetTag)).
		WithColor(caseColor(modules.ModerationTypeWarn)).
		WithFields(fields...).
		WithTimestamp(c.CreatedAt)
	if guildName != "" {
		e = e.WithFooter(guildName, "")
	}
	return e
}

// warningRemovedEmbed records an unwarn in the audit channel.
func warningRemovedEmbed(c modules.ModerationCase, guildName string) discord.Embed {
	remover := userLabel(c.RemovedByTag, c.RemovedByID)
	fields := []discord.EmbedField{
		{Name: "Target", Value: userLabel(c.TargetTag, c.TargetID), Inline: util.PtrBool(true)},
		{Name: "Removed by", Value: remover, Inline: util.PtrBool(true)},
		{Name: "Case", Value: fmt.Sprintf("#%d", c.ID), Inline: util.PtrBool(true)},
		{Name: "Original reason", Value: truncate(reasonOrNone(c.Reason), maxEmbedFieldValue)},
	}
	if strings.TrimSpace(c.RemovedReason) != "" {
		fields = append(fields, discord.EmbedField{Name: "Removal reason", Value: truncate(c.RemovedReason, maxEmbedFieldValue)})
	}
	e := discord.NewEmbed().
		WithTitle("Warning removed · case #" + strconv.Itoa(c.ID)).
		WithDescription(fmt.Sprintf("Warning #%d against **%s** was removed.", c.ID, c.TargetTag)).
		WithColor(0x95A5A6).
		WithFields(fields...).
		WithTimestamp(time.Now().UTC())
	if guildName != "" {
		e = e.WithFooter(guildName, "")
	}
	return e
}
