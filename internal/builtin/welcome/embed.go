package welcome

import (
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/internal/util"
	"github.com/misfit/bot/modules"
)

// buildMessage renders one welcome/leave event into a Discord message payload.
// The returned warning is non-empty when the configured embed had to be
// dropped (after variable expansion it exceeded Discord's total embed limit);
// the message content is still delivered.
func buildMessage(ev modules.WelcomeEvent, d templateData) (discord.MessageCreate, string) {
	content := renderTemplate(ev.Message, d)
	var embeds []discord.Embed
	warning := ""
	if ev.Embed.Enabled {
		if embed := buildEmbed(ev.Embed, d); embed != nil {
			if n := embedLength(*embed); n > maxEmbedTotalLen {
				warning = fmt.Sprintf("embed is %d characters after variable expansion (max %d); it was omitted", n, maxEmbedTotalLen)
			} else {
				embeds = append(embeds, *embed)
			}
		}
	}
	return discord.MessageCreate{
		Content: content,
		Embeds:  embeds,
		// Allow the joining/leaving member's own mention (the {mention}
		// variable) but never @everyone/@here or roles, even if a display
		// name or the template contains one.
		AllowedMentions: &discord.AllowedMentions{
			Parse: []discord.AllowedMentionType{},
			Users: []snowflake.ID{d.User.ID},
		},
	}, warning
}

func messageEmpty(m discord.MessageCreate) bool {
	return strings.TrimSpace(m.Content) == "" && len(m.Embeds) == 0
}

// buildEmbed assembles the embed after expanding variables. It returns nil
// when the rendered embed has no visible content.
func buildEmbed(cfg modules.WelcomeEmbed, d templateData) *discord.Embed {
	if !cfg.Enabled {
		return nil
	}
	e := discord.NewEmbed()
	if cfg.Color != "" {
		if color := parseColor(cfg.Color); color != 0 {
			e = e.WithColor(color)
		}
	}
	authorName := renderTemplate(cfg.Author.Name, d)
	if authorName != "" {
		e = e.WithAuthor(authorName, renderURL(cfg.Author.URL, d), renderURL(cfg.Author.IconURL, d))
	}
	title := renderTemplate(cfg.Title, d)
	if title != "" {
		e = e.WithTitle(title)
		if u := renderURL(cfg.TitleURL, d); u != "" {
			e = e.WithURL(u)
		}
	}
	if desc := renderTemplate(cfg.Description, d); desc != "" {
		e = e.WithDescription(desc)
	}
	fields := make([]discord.EmbedField, 0, len(cfg.Fields))
	for _, f := range cfg.Fields {
		name := renderTemplate(f.Name, d)
		value := renderTemplate(f.Value, d)
		if name == "" || value == "" {
			continue
		}
		fields = append(fields, discord.EmbedField{Name: name, Value: value, Inline: util.PtrBool(f.Inline)})
	}
	if len(fields) > 0 {
		e = e.WithFields(fields...)
	}
	if u := renderURL(cfg.ImageURL, d); u != "" {
		e = e.WithImage(u)
	}
	if u := renderURL(cfg.ThumbnailURL, d); u != "" {
		e = e.WithThumbnail(u)
	}
	if footer := renderTemplate(cfg.Footer, d); footer != "" {
		e = e.WithFooter(footer, renderURL(cfg.FooterIcon, d))
	}
	if cfg.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, cfg.Timestamp); err == nil {
			e = e.WithTimestamp(t)
		}
	}
	if !embedRendered(e) {
		return nil
	}
	return &e
}

// renderURL expands variables and returns the value only when it is still a
// usable http(s) URL. A broken URL is omitted rather than making Discord
// reject the whole message.
func renderURL(raw string, d templateData) string {
	if raw == "" {
		return ""
	}
	u := strings.TrimSpace(renderTemplate(raw, d))
	if !validEmbedURL(u) {
		return ""
	}
	return u
}

func embedRendered(e discord.Embed) bool {
	if e.Title != "" || e.Description != "" || e.Author != nil || e.Footer != nil ||
		e.Image != nil || e.Thumbnail != nil || len(e.Fields) > 0 {
		return true
	}
	return false
}

// embedLength mirrors Discord's 6000-character embed budget.
func embedLength(e discord.Embed) int {
	n := charLen(e.Title) + charLen(e.Description)
	if e.Author != nil {
		n += charLen(e.Author.Name)
	}
	if e.Footer != nil {
		n += charLen(e.Footer.Text)
	}
	for _, f := range e.Fields {
		n += charLen(f.Name) + charLen(f.Value)
	}
	return n
}
