package imagefilter

import (
	"strings"

	"github.com/disgoorg/disgo/events"
)

// registerDetector subscribes the guild message hook. The gateway goroutine
// only does cheap gating; everything heavy (fetch, embed, compare, punish)
// happens on the manager's worker via job submission (never blocks dispatch).
func (m *ImageFilterModule) registerDetector() {
	m.ctx.Events.AddGuildMessageCreate(func(e *events.GuildMessageCreate) {
		defer func() {
			if r := recover(); r != nil {
				m.ctx.Logger.Error("imagefilter: panic in message gate: %v", r)
			}
		}()
		m.gateMessage(e)
	})
}

// isBotAuthor handles both shapes a message can arrive in: webhook/DM-style
// (Author populated) and member-style (Member.User populated).
func isBotAuthor(e *events.GuildMessageCreate) bool {
	if e.Message.Member != nil && e.Message.Member.User.ID != 0 {
		return e.Message.Member.User.Bot
	}
	return e.Message.Author.Bot
}

// authorIDOf resolves the author ID from either message shape.
func authorIDOf(e *events.GuildMessageCreate) string {
	if e.Message.Member != nil && e.Message.Member.User.ID != 0 {
		return e.Message.Member.User.ID.String()
	}
	return e.Message.Author.ID.String()
}

// genericContentType reports whether a content type carries no format
// information, i.e. whether the filename extension may be trusted instead.
func genericContentType(ct string) bool {
	switch ct {
	case "", "application/octet-stream", "binary/octet-stream", "application/binary":
		return true
	}
	return false
}

// looksLikeImage: Discord normally sends image/* content types, but some
// clients attach images with a missing or generic type. Only those fall back
// to the filename extension (formats Preprocess actually decodes); an explicit
// non-image type (text/plain on a "photo.png") is authoritative.
func looksLikeImage(contentType, filename string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasPrefix(ct, "image/") {
		return true
	}
	if !genericContentType(ct) {
		return false
	}
	name := strings.ToLower(filename)
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return false
	}
	switch name[i:] {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// gateMessage decides whether a message needs detection (cheap path only).
func (m *ImageFilterModule) gateMessage(e *events.GuildMessageCreate) {
	if !m.isLoaded() || e.GuildID == 0 {
		return
	}
	if isBotAuthor(e) {
		return // bots (including ourselves) are never punished
	}
	guildID := e.GuildID.String()
	if !m.mgr.cfg.GuildSettings(guildID).Enabled {
		return
	}
	if !m.mgr.warm() {
		return // cold model: skip silently (dashboard shows the state)
	}
	// Submit EVERY image attachment (Python parity: the retired module checked
	// every attachment on the message and punished on the first one over the
	// threshold). A message with several images is fully covered instead of
	// stopping at the first one — a spam poster can put the bad image second.
	// Identical URLs are submitted once.
	seen := make(map[string]struct{}, len(e.Message.Attachments))
	for _, att := range e.Message.Attachments {
		ct := ""
		if att.ContentType != nil {
			ct = *att.ContentType
		}
		if !looksLikeImage(ct, att.Filename) || att.ProxyURL == "" {
			continue
		}
		if _, dup := seen[att.ProxyURL]; dup {
			continue
		}
		seen[att.ProxyURL] = struct{}{}
		m.mgr.submit(job{
			guildID:   guildID,
			channelID: e.Message.ChannelID.String(),
			messageID: e.Message.ID.String(),
			authorID:  authorIDOf(e),
			imageURL:  att.ProxyURL,
		})
	}
}
