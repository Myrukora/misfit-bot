package main

import (
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// registerLogging subscribes message create/update/delete hooks that mirror
// ticket conversations into the per-ticket log. Only messages inside known
// ticket threads are recorded; the bot's own posts are skipped.
func (m *TicketsModule) registerLogging() {
	m.ctx.Events.AddGuildMessageCreate(func(e *events.GuildMessageCreate) {
		defer m.recoverLog("message-create")
		m.logMessageCreate(e)
	})
	m.ctx.Events.AddGuildMessageUpdate(func(e *events.GuildMessageUpdate) {
		defer m.recoverLog("message-update")
		m.logMessageUpdate(e)
	})
	m.ctx.Events.AddGuildMessageDelete(func(e *events.GuildMessageDelete) {
		defer m.recoverLog("message-delete")
		m.logMessageDelete(e)
	})
}

func (m *TicketsModule) recoverLog(what string) {
	if r := recover(); r != nil {
		m.ctx.Logger.Error("Tickets: panic in %s logger: %v", what, r)
	}
}

// ticketByChannel resolves the open ticket owning a channel, if any.
func (m *TicketsModule) ticketByChannel(guildID, channelID string) *modules.Ticket {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, tk := range m.store.tickets[guildID] {
		if tk.ChannelID == channelID && tk.Status == "open" {
			return tk
		}
	}
	return nil
}

// isOwnPost reports whether a message was authored by the bot itself (panel
// embeds etc. must not appear in transcripts).
func (m *TicketsModule) isOwnPost(authorID string) bool {
	return authorID == m.selfID()
}

func (m *TicketsModule) selfID() string {
	if id := m.ctx.Bot.GetSelfUserID(); id != "" {
		return id
	}
	return "0"
}

func (m *TicketsModule) logMessageCreate(e *events.GuildMessageCreate) {
	guildID := e.GuildID.String()
	channelID := e.Message.ChannelID.String()
	tk := m.ticketByChannel(guildID, channelID)
	if tk == nil {
		return
	}
	authorID := ""
	authorName := ""
	isBot := false
	if e.Message.Member != nil && e.Message.Member.User.ID != 0 {
		authorID = e.Message.Member.User.ID.String()
		authorName = e.Message.Member.EffectiveName()
		isBot = e.Message.Member.User.Bot
	} else if e.Message.Author.ID != 0 {
		authorID = e.Message.Author.ID.String()
		authorName = e.Message.Author.EffectiveName()
		isBot = e.Message.Author.Bot
	}
	if authorID == m.selfID() || isBot {
		return
	}

	entry := modules.LogEntry{
		MsgID:       e.Message.ID.String(),
		AuthorID:    authorID,
		AuthorName:  authorName,
		Timestamp:   time.Now().UTC(),
		Content:     e.Message.Content,
		Attachments: classifyAttachments(e.Message.Attachments),
		Stickers:    classifyStickers(e.Message.StickerItems),
	}
	m.mu.Lock()
	tk.Log = append(tk.Log, entry)
	m.mu.Unlock()
	_ = m.store.save(tk)
}

func (m *TicketsModule) logMessageUpdate(e *events.GuildMessageUpdate) {
	guildID := e.GuildID.String()
	channelID := e.Message.ChannelID.String()
	tk := m.ticketByChannel(guildID, channelID)
	if tk == nil {
		return
	}
	msgID := e.Message.ID.String()
	m.mu.Lock()
	for i := range tk.Log {
		if tk.Log[i].MsgID == msgID {
			tk.Log[i].Content = e.Message.Content
			tk.Log[i].Edited = true
			break
		}
	}
	m.mu.Unlock()
	_ = m.store.save(tk)
}

func (m *TicketsModule) logMessageDelete(e *events.GuildMessageDelete) {
	guildID := e.GuildID.String()
	channelID := e.Message.ChannelID.String()
	tk := m.ticketByChannel(guildID, channelID)
	if tk == nil {
		return
	}
	msgID := e.Message.ID.String()
	m.mu.Lock()
	for i := range tk.Log {
		if tk.Log[i].MsgID == msgID {
			tk.Log[i].Deleted = true
			break
		}
	}
	m.mu.Unlock()
	_ = m.store.save(tk)
}

// ── classification (pure functions, unit-tested) ─────────────────────────

// classifyAttachments maps Discord attachments to Media records, classifying
// by content type: image/* → image, video/* → video, else file.
func classifyAttachments(atts []discord.Attachment) []modules.Media {
	out := make([]modules.Media, 0, len(atts))
	for _, a := range atts {
		ct := ""
		if a.ContentType != nil {
			ct = *a.ContentType
		}
		kind := "file"
		lct := strings.ToLower(ct)
		switch {
		case strings.HasPrefix(lct, "image/"):
			kind = "image"
		case strings.HasPrefix(lct, "video/"):
			kind = "video"
		}
		out = append(out, modules.Media{
			URL:         a.URL,
			ProxyURL:    a.ProxyURL,
			Kind:        kind,
			ContentType: ct,
			Filename:    a.Filename,
			Size:        a.Size,
		})
	}
	return out
}

// stickerAssetURL builds the CDN URL for a message sticker; Lottie stickers
// get the .json asset (the dashboard renders the preview variant instead).
func stickerAssetURL(id snowflake.ID, format discord.StickerFormatType) string {
	base := "https://cdn.discordapp.com/stickers/" + id.String()
	switch format {
	case discord.StickerFormatTypeLottie:
		return base + ".json?passthrough=true"
	case discord.StickerFormatTypeGIF:
		return base + ".gif"
	default:
		return base + ".png"
	}
}

func classifyStickers(items []discord.MessageSticker) []modules.Media {
	if len(items) == 0 {
		return nil
	}
	out := make([]modules.Media, 0, len(items))
	for _, s := range items {
		out = append(out, modules.Media{
			URL:      stickerAssetURL(s.ID, s.FormatType),
			Kind:     "sticker",
			Filename: s.Name,
		})
	}
	return out
}
