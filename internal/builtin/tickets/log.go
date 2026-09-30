package tickets

import (
	"net/url"
	"strings"

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

// ticketByChannel resolves the open ticket owning a channel, if any. The
// lookup is delegated to the store so the tickets map is only ever iterated
// under the STORE lock (the module lock does not protect it).
func (m *TicketsModule) ticketByChannel(guildID, channelID string) *modules.Ticket {
	if !m.isLoaded() {
		return nil
	}
	return m.store.ticketByChannel(guildID, channelID)
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
	// Cheap copy-free gate: skip the classification work for non-ticket
	// channels without deep-copying a transcript (ticketByChannel copies).
	if !m.isLoaded() || !m.store.hasOpenTicketOnChannel(guildID, channelID) {
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
		Timestamp:   e.Message.CreatedAt.UTC(),
		Content:     e.Message.Content,
		Attachments: classifyAttachments(e.Message.Attachments),
		Stickers:    classifyStickers(e.Message.StickerItems),
		Embeds:      classifyEmbeds(e.Message.Embeds),
	}
	tk, changed, _ := m.store.mutateByChannel(guildID, channelID, func(tk *modules.Ticket) bool {
		for i := range tk.Log {
			if tk.Log[i].MsgID == entry.MsgID {
				return false
			}
		}
		tk.Log = append(tk.Log, entry)
		return true
	})
	if !changed || tk == nil {
		return
	}

	// Mirror non-link media in the background (sticker/attachment/embed).
	if m.mirror != nil && entryHasMirrorableMedia(entry) {
		m.mirror.enqueue(mirrorJob{guildID: guildID, ticketID: tk.ID, entry: entry})
	}
}

func (m *TicketsModule) logMessageUpdate(e *events.GuildMessageUpdate) {
	guildID := e.GuildID.String()
	channelID := e.Message.ChannelID.String()
	msgID := e.Message.ID.String()
	_, _, _ = m.store.mutateByChannel(guildID, channelID, func(tk *modules.Ticket) bool {
		for i := range tk.Log {
			if tk.Log[i].MsgID == msgID {
				tk.Log[i].Content = e.Message.Content
				tk.Log[i].Edited = true
				return true
			}
		}
		return false
	})
}

func (m *TicketsModule) logMessageDelete(e *events.GuildMessageDelete) {
	guildID := e.GuildID.String()
	channelID := e.Message.ChannelID.String()
	msgID := e.Message.ID.String()
	_, _, _ = m.store.mutateByChannel(guildID, channelID, func(tk *modules.Ticket) bool {
		for i := range tk.Log {
			if tk.Log[i].MsgID == msgID {
				tk.Log[i].Deleted = true
				return true
			}
		}
		return false
	})
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

// classifyEmbeds maps Discord embeds to Media records: video → "video",
// image → "image", thumbnail (when no image) → "image", else link. Only
// http/https URLs are kept. Filename falls back to the embed title, then the
// provider name.
func classifyEmbeds(embeds []discord.Embed) []modules.Media {
	if len(embeds) == 0 {
		return nil
	}
	out := make([]modules.Media, 0, len(embeds))
	for _, e := range embeds {
		kind := "link"
		url := ""
		switch {
		case e.Video != nil && e.Video.URL != "":
			kind = "video"
			url = e.Video.URL
		case e.Image != nil && e.Image.URL != "":
			kind = "image"
			url = e.Image.URL
		case e.Thumbnail != nil && e.Thumbnail.URL != "":
			kind = "image"
			url = e.Thumbnail.URL
		case e.URL != "":
			kind = "link"
			url = e.URL
		default:
			continue
		}
		if !isHTTPURL(url) {
			continue
		}
		filename := e.Title
		if filename == "" && e.Provider != nil {
			filename = e.Provider.Name
		}
		out = append(out, modules.Media{
			URL:      url,
			Kind:     kind,
			Filename: filename,
		})
	}
	return out
}

// isHTTPURL reports whether u is an http/https URL.
func isHTTPURL(u string) bool {
	parsed, err := url.Parse(u)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
