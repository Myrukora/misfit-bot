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
		if att.ContentType == nil || !strings.HasPrefix(*att.ContentType, "image/") || att.ProxyURL == "" {
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
