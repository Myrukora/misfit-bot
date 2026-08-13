package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/custombot/bot/commands"
	"github.com/custombot/bot/embed"
	"github.com/custombot/bot/internal/util"
	"github.com/custombot/bot/modules"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type CleanupModule struct {
	ctx *modules.Context
}

func (m *CleanupModule) Name() string        { return "cleanup" }
func (m *CleanupModule) Version() string     { return "1.0.0" }
func (m *CleanupModule) Description() string { return "Message cleanup and moderation commands" }
func (m *CleanupModule) Author() string      { return "custombot" }

func (m *CleanupModule) OnLoad(ctx *modules.Context) error {
	m.ctx = ctx
	ctx.Logger.Info("Cleanup module loaded!")
	return nil
}

func (m *CleanupModule) OnUnload() error { return nil }

const (
	twoWeeks      = 13 * 24 * time.Hour // 13 days — Discord's bulk delete API allows up to 14 days
	bulkBatchSize = 100                 // Discord's bulk-delete endpoint accepts at most 100 messages per request
)

func parseSnowflake(s string) (snowflake.ID, error) {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid ID: %s", s)
	}
	return snowflake.ID(id), nil
}

func safeParseChannelID(s string) (snowflake.ID, error) {
	id, err := snowflake.Parse(s)
	if err != nil {
		return 0, fmt.Errorf("invalid channel ID '%s': %w", s, err)
	}
	return id, nil
}

// maxCleanupCount caps how many messages one cleanup request may target.
// Discord's 14-day fetch window means a huge count just turns into a long
// blocking REST loop; 1000 is far beyond any real moderation need.
const maxCleanupCount = 1000

// parseCount parses a positive count and rejects values above maxCleanupCount
// so a ManageMessages user cannot stall command processing with a huge
// pagination run.
func parseCount(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("number must be a positive integer")
	}
	if n > maxCleanupCount {
		return 0, fmt.Errorf("number must be at most %d", maxCleanupCount)
	}
	return n, nil
}

func (m *CleanupModule) fetchMessages(channelID snowflake.ID, number int, before snowflake.ID, check func(discord.Message) bool) ([]discord.Message, error) {
	cutoff := time.Now().UTC().Add(-twoWeeks)
	var collected []discord.Message
	fetchBefore := before

	for len(collected) < number {
		batchSize := number - len(collected)
		if batchSize > 100 {
			batchSize = 100
		}

		msgs, err := m.ctx.Rest.GetMessages(channelID, 0, fetchBefore, 0, batchSize)
		if err != nil {
			return collected, err
		}
		if len(msgs) == 0 {
			break
		}

		for _, msg := range msgs {
			if msg.CreatedAt.Before(cutoff) {
				return collected, nil
			}
			if check(msg) {
				collected = append(collected, msg)
				if len(collected) >= number {
					break
				}
			}
		}
		fetchBefore = msgs[len(msgs)-1].ID
	}
	return collected, nil
}

// isBulkDeleteUnsupported reports whether a bulk-delete request was rejected
// by the API itself (HTTP 400 — e.g. messages older than 14 days, or a batch
// outside the 2–100 message range). Permission (403) and rate-limit (429)
// failures return false so the caller does NOT fall back to individual
// deletes, which would just fail again — or worse, hammer the API.
func isBulkDeleteUnsupported(err error) bool {
	var restErr *rest.Error
	if !errors.As(err, &restErr) {
		return false
	}
	return restErr.Response != nil && restErr.Response.StatusCode == http.StatusBadRequest
}

func (m *CleanupModule) bulkDelete(channelID snowflake.ID, ids []snowflake.ID) (failed int, _ error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if len(ids) == 1 {
		if err := m.deleteSingle(channelID, ids[0]); err != nil {
			return 1, err
		}
		return 0, nil
	}
	// disgo passes the whole slice through in ONE request, but Discord's
	// bulk-delete endpoint caps at 100 — chunk to stay within the limit.
	var fallback []snowflake.ID
	for start := 0; start < len(ids); start += bulkBatchSize {
		end := start + bulkBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		if err := m.ctx.Rest.BulkDeleteMessages(channelID, batch); err != nil {
			// Only messages the bulk API itself rejects (typically older than
			// 14 days) can still be deleted individually. Leave the loop on
			// permission/rate-limit errors instead of spamming single deletes.
			if !isBulkDeleteUnsupported(err) {
				return 0, err
			}
			m.ctx.Logger.Warn("Bulk delete rejected (%d messages), falling back to single deletes: %v", len(batch), err)
			fallback = append(fallback, batch...)
		}
	}
	for _, id := range fallback {
		if dErr := m.deleteSingle(channelID, id); dErr != nil {
			m.ctx.Logger.Warn("Failed to delete message", "id", id.String(), "error", dErr)
			failed++
		}
	}
	return failed, nil
}

func (m *CleanupModule) deleteSingle(channelID, msgID snowflake.ID) error {
	return m.ctx.Rest.DeleteMessage(channelID, msgID)
}

// done reports the deletion result. The invoking command message is usually
// inside the deleted range (it is the newest message in the channel), so it
// is excluded from the reported count for honesty. failed counts individual
// deletes that errored — reported instead of claiming a full success.
func (m *CleanupModule) done(ctx *commands.Context, ids []snowflake.ID, failed int) {
	count := len(ids)
	if invoking, err := parseSnowflake(ctx.MessageID); err == nil {
		for _, id := range ids {
			if id == invoking {
				count--
				break
			}
		}
	}
	if count <= 0 && failed == 0 {
		ctx.Respond(embed.Info("🧹 Cleanup", "No messages to delete (might be too old or already deleted)."))
		return
	}
	if failed > 0 {
		ctx.Respond(embed.Warning("🧹 Cleanup", fmt.Sprintf("Deleted **%d** messages (**%d** failed).", count, failed)))
		return
	}
	ctx.Respond(embed.Success("🧹 Cleanup", fmt.Sprintf("Deleted **%d** messages.", count)))
}

func (m *CleanupModule) Commands() []commands.Command {
	return []commands.Command{
		{
			Name:         "cleanup",
			Description:  "Message cleanup commands",
			Usage:        "cleanup <subcommand>",
			Category:     "cleanup",
			RequiredPerm: discord.PermissionManageMessages,
			Execute:      m.dispatch,
		},
	}
}

// SlashCommands exposes cleanup as /cleanup with nested subcommand options:
// each subcommand carries only its own arguments (the slash dispatcher
// prepends the subcommand name, then appends the provided options in order —
// exactly the positional args the shared dispatch expects). The dashboard's
// command form renders one subcommand selector with per-subcommand groups.
func (m *CleanupModule) SlashCommands() []commands.SlashCommand {
	intOpt := func(name, desc string, required bool) discord.ApplicationCommandOptionInt {
		return discord.ApplicationCommandOptionInt{Name: name, Description: desc, Required: required}
	}
	strOpt := func(name, desc string, required bool) discord.ApplicationCommandOptionString {
		return discord.ApplicationCommandOptionString{Name: name, Description: desc, Required: required}
	}
	return []commands.SlashCommand{
		{
			Name:         "cleanup",
			Description:  "Message cleanup commands",
			Category:     "cleanup",
			RequiredPerm: discord.PermissionManageMessages,
			Options: []discord.ApplicationCommandOption{
				discord.ApplicationCommandOptionSubCommand{
					Name: "messages", Description: "Delete the last N messages",
					Options: []discord.ApplicationCommandOption{intOpt("count", "How many messages", true)},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name: "user", Description: "Delete N messages from a user",
					Options: []discord.ApplicationCommandOption{
						discord.ApplicationCommandOptionUser{Name: "user", Description: "The user", Required: true},
						intOpt("count", "How many messages", true),
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name: "text", Description: "Delete N messages containing text",
					Options: []discord.ApplicationCommandOption{
						strOpt("text", "Text to match", true),
						intOpt("count", "How many messages", true),
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name: "bot", Description: "Delete bot and command messages",
					Options: []discord.ApplicationCommandOption{intOpt("count", "How many messages", true)},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name: "self", Description: "Delete the bot's own messages",
					Options: []discord.ApplicationCommandOption{intOpt("count", "How many messages", true)},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name: "after", Description: "Delete messages after an ID",
					Options: []discord.ApplicationCommandOption{strOpt("message_id", "Message ID", true)},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name: "before", Description: "Delete N messages before an ID",
					Options: []discord.ApplicationCommandOption{
						strOpt("message_id", "Message ID", true),
						intOpt("count", "How many messages", true),
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name: "between", Description: "Delete messages between two IDs (including the first)",
					Options: []discord.ApplicationCommandOption{
						strOpt("message_id", "First message ID", true),
						strOpt("message_id2", "Second message ID", true),
					},
				},
				discord.ApplicationCommandOptionSubCommand{
					Name: "duplicates", Description: "Delete duplicate messages",
					Options: []discord.ApplicationCommandOption{intOpt("count", "How many messages", false)},
				},
			},
			Execute: m.dispatch,
		},
	}
}

// dispatch runs a cleanup invocation from ctx.Args (prefix and slash share
// this path: slash options are converted to the same positional args).
func (m *CleanupModule) dispatch(ctx *commands.Context) error {
	if len(ctx.Args) == 0 {
		return ctx.Respond(embed.New().
			WithTitle("🧹 Cleanup").
			WithDescription("Available subcommands:").
			WithColor(embed.ColorInfo).
			WithFields(
				discord.EmbedField{Name: "messages", Value: "Delete last N messages\n`cleanup messages <number>`", Inline: util.PtrBool(true)},
				discord.EmbedField{Name: "user", Value: "Delete N messages from a user\n`cleanup user <@user> <number>`", Inline: util.PtrBool(true)},
				discord.EmbedField{Name: "text", Value: "Delete N messages containing text\n`cleanup text \"hello\" <number>`", Inline: util.PtrBool(true)},
				discord.EmbedField{Name: "bot", Value: "Delete bot and command messages\n`cleanup bot <number>`", Inline: util.PtrBool(true)},
				discord.EmbedField{Name: "self", Value: "Delete bot's own messages\n`cleanup self <number>`", Inline: util.PtrBool(true)},
				discord.EmbedField{Name: "after", Value: "Delete messages after an ID\n`cleanup after <message_id>`", Inline: util.PtrBool(true)},
				discord.EmbedField{Name: "before", Value: "Delete N messages before an ID\n`cleanup before <message_id> <number>`", Inline: util.PtrBool(true)},
				discord.EmbedField{Name: "between", Value: "Delete messages between two IDs (including the first)\n`cleanup between <id1> <id2>`", Inline: util.PtrBool(true)},
				discord.EmbedField{Name: "duplicates", Value: "Delete duplicate messages\n`cleanup duplicates [number]`", Inline: util.PtrBool(true)},
			).
			WithTimestamp(time.Now()))
	}

	sub := strings.ToLower(ctx.Args[0])
	args := ctx.Args[1:]

	switch sub {
	case "messages":
		return m.cmdMessages(ctx, args)
	case "user":
		return m.cmdUser(ctx, args)
	case "text":
		return m.cmdText(ctx, args)
	case "bot":
		return m.cmdBot(ctx, args)
	case "self":
		return m.cmdSelf(ctx, args)
	case "after":
		return m.cmdAfter(ctx, args)
	case "before":
		return m.cmdBefore(ctx, args)
	case "between":
		return m.cmdBetween(ctx, args)
	case "duplicates", "spam":
		return m.cmdDuplicates(ctx, args)
	default:
		return ctx.Respond(embed.Warning("⚠️ Unknown subcommand", fmt.Sprintf("`%s` is not a valid cleanup subcommand. Use `cleanup` to see available commands.", sub)))
	}
}

func (m *CleanupModule) Dependencies() []string { return nil }

func (m *CleanupModule) cmdMessages(ctx *commands.Context, args []string) error {
	if len(args) == 0 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`cleanup messages <number>`"))
	}
	number, err := parseCount(args[0])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", err.Error()))
	}

	channelID, err := safeParseChannelID(ctx.ChannelID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid channel: %v", err)))
	}
	toDelete, err := m.fetchMessages(channelID, number, 0, func(discord.Message) bool { return true })
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to fetch: %v", err)))
	}

	ids := make([]snowflake.ID, 0, len(toDelete))
	for _, msg := range toDelete {
		ids = append(ids, msg.ID)
	}

	failed, err := m.bulkDelete(channelID, ids)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to delete: %v", err)))
	}
	m.done(ctx, ids, failed)
	return nil
}

func (m *CleanupModule) cmdUser(ctx *commands.Context, args []string) error {
	if len(args) < 2 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`cleanup user <@user> <number>`"))
	}
	targetID := util.ExtractID(args[0])
	number, err := parseCount(args[1])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", err.Error()))
	}

	channelID, err := safeParseChannelID(ctx.ChannelID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid channel: %v", err)))
	}
	userID, err := parseSnowflake(targetID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid user ID: %v", err)))
	}

	toDelete, err := m.fetchMessages(channelID, number, 0, func(msg discord.Message) bool {
		return msg.Author.ID == userID
	})
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to fetch: %v", err)))
	}

	ids := make([]snowflake.ID, 0, len(toDelete))
	for _, msg := range toDelete {
		ids = append(ids, msg.ID)
	}

	failed, err := m.bulkDelete(channelID, ids)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to delete: %v", err)))
	}
	m.done(ctx, ids, failed)
	return nil
}

func (m *CleanupModule) cmdText(ctx *commands.Context, args []string) error {
	if len(args) < 2 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`cleanup text \"hello\" <number>`"))
	}
	text := strings.Trim(args[0], `"`)
	// An empty filter matches EVERY message (strings.Contains(msg, "") is
	// always true) — reject it so `cleanup text ""` cannot become an
	// unrestricted deletion.
	if text == "" {
		return ctx.Respond(embed.Warning("⚠️ Usage", "Text to match cannot be empty."))
	}
	number, err := parseCount(args[1])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", err.Error()))
	}

	channelID, err := safeParseChannelID(ctx.ChannelID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid channel: %v", err)))
	}
	toDelete, err := m.fetchMessages(channelID, number, 0, func(msg discord.Message) bool {
		return strings.Contains(msg.Content, text)
	})
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to fetch: %v", err)))
	}

	ids := make([]snowflake.ID, 0, len(toDelete))
	for _, msg := range toDelete {
		ids = append(ids, msg.ID)
	}

	failed, err := m.bulkDelete(channelID, ids)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to delete: %v", err)))
	}
	m.done(ctx, ids, failed)
	return nil
}

func (m *CleanupModule) cmdBot(ctx *commands.Context, args []string) error {
	if len(args) == 0 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`cleanup bot <number>`"))
	}
	number, err := parseCount(args[0])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", err.Error()))
	}

	channelID, err := safeParseChannelID(ctx.ChannelID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid channel: %v", err)))
	}
	prefix := m.ctx.Bot.GetPrefix()

	toDelete, err := m.fetchMessages(channelID, number, 0, func(msg discord.Message) bool {
		if msg.Author.Bot {
			return true
		}
		content := strings.TrimSpace(msg.Content)
		return strings.HasPrefix(content, prefix)
	})
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to fetch: %v", err)))
	}

	ids := make([]snowflake.ID, 0, len(toDelete))
	for _, msg := range toDelete {
		ids = append(ids, msg.ID)
	}

	failed, err := m.bulkDelete(channelID, ids)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to delete: %v", err)))
	}
	m.done(ctx, ids, failed)
	return nil
}

func (m *CleanupModule) cmdSelf(ctx *commands.Context, args []string) error {
	if len(args) == 0 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`cleanup self <number>`"))
	}
	number, err := parseCount(args[0])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", err.Error()))
	}

	channelID, err := safeParseChannelID(ctx.ChannelID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid channel: %v", err)))
	}
	selfID := m.ctx.Bot.GetSelfUserID()

	toDelete, err := m.fetchMessages(channelID, number, 0, func(msg discord.Message) bool {
		return msg.Author.ID.String() == selfID
	})
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to fetch: %v", err)))
	}

	ids := make([]snowflake.ID, 0, len(toDelete))
	failed := 0
	for _, msg := range toDelete {
		ids = append(ids, msg.ID)
		if err := m.deleteSingle(channelID, msg.ID); err != nil {
			m.ctx.Logger.Warn("Failed to delete message", "id", msg.ID.String(), "error", err)
			failed++
		}
	}
	m.done(ctx, ids, failed)
	return nil
}

func (m *CleanupModule) cmdAfter(ctx *commands.Context, args []string) error {
	if len(args) == 0 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`cleanup after <message_id>`"))
	}
	afterID, err := parseSnowflake(args[0])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", "Invalid message ID."))
	}

	channelID, err := safeParseChannelID(ctx.ChannelID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid channel: %v", err)))
	}
	toDelete, err := m.fetchMessages(channelID, 1000, 0, func(msg discord.Message) bool {
		return msg.ID > afterID
	})
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to fetch: %v", err)))
	}

	ids := make([]snowflake.ID, 0, len(toDelete))
	for _, msg := range toDelete {
		ids = append(ids, msg.ID)
	}

	failed, err := m.bulkDelete(channelID, ids)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to delete: %v", err)))
	}
	m.done(ctx, ids, failed)
	return nil
}

func (m *CleanupModule) cmdBefore(ctx *commands.Context, args []string) error {
	if len(args) < 2 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`cleanup before <message_id> <number>`"))
	}
	beforeID, err := parseSnowflake(args[0])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", "Invalid message ID."))
	}
	number, err := parseCount(args[1])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", err.Error()))
	}

	channelID, err := safeParseChannelID(ctx.ChannelID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid channel: %v", err)))
	}
	toDelete, err := m.fetchMessages(channelID, number, beforeID, func(discord.Message) bool { return true })
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to fetch: %v", err)))
	}

	ids := make([]snowflake.ID, 0, len(toDelete))
	for _, msg := range toDelete {
		ids = append(ids, msg.ID)
	}

	failed, err := m.bulkDelete(channelID, ids)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to delete: %v", err)))
	}
	m.done(ctx, ids, failed)
	return nil
}

func (m *CleanupModule) cmdBetween(ctx *commands.Context, args []string) error {
	if len(args) < 2 {
		return ctx.Respond(embed.Warning("⚠️ Usage", "`cleanup between <id1> <id2>`"))
	}
	id1, err := parseSnowflake(args[0])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", "Invalid first message ID."))
	}
	id2, err := parseSnowflake(args[1])
	if err != nil {
		return ctx.Respond(embed.Warning("⚠️ Usage", "Invalid second message ID."))
	}
	if id1 > id2 {
		id1, id2 = id2, id1
	}

	channelID, err := safeParseChannelID(ctx.ChannelID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid channel: %v", err)))
	}
	toDelete, err := m.fetchMessages(channelID, 1000, id2, func(msg discord.Message) bool {
		// Inclusive of the older anchor: everything from id1 up to (not
		// including) id2 gets deleted.
		return msg.ID >= id1 && msg.ID < id2
	})
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to fetch: %v", err)))
	}

	ids := make([]snowflake.ID, 0, len(toDelete))
	for _, msg := range toDelete {
		ids = append(ids, msg.ID)
	}

	failed, err := m.bulkDelete(channelID, ids)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to delete: %v", err)))
	}
	m.done(ctx, ids, failed)
	return nil
}

func (m *CleanupModule) cmdDuplicates(ctx *commands.Context, args []string) error {
	number := 50
	if len(args) > 0 {
		n, err := parseCount(args[0])
		if err != nil {
			return ctx.Respond(embed.Warning("⚠️ Usage", err.Error()))
		}
		number = n
	}

	channelID, err := safeParseChannelID(ctx.ChannelID)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Invalid channel: %v", err)))
	}

	msgs, err := m.fetchMessages(channelID, number, 0, func(discord.Message) bool { return true })
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to fetch: %v", err)))
	}

	seen := make(map[string]bool)
	var toDelete []discord.Message

	for _, msg := range msgs {
		if msg.Author.Bot {
			continue
		}
		key := fmt.Sprintf("%d:%s", msg.Author.ID, msg.Content)
		if seen[key] {
			toDelete = append(toDelete, msg)
			continue
		}
		seen[key] = true
	}

	if len(toDelete) == 0 {
		return ctx.Respond(embed.Info("🧹 Cleanup", "No duplicate messages found."))
	}

	ids := make([]snowflake.ID, 0, len(toDelete))
	for _, msg := range toDelete {
		ids = append(ids, msg.ID)
	}

	failed, err := m.bulkDelete(channelID, ids)
	if err != nil {
		return ctx.Respond(embed.Error("❌ Error", fmt.Sprintf("Failed to delete: %v", err)))
	}
	m.done(ctx, ids, failed)
	return nil
}

func New() modules.Module { return &CleanupModule{} }
