package welcome

import (
	"strings"
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

func TestBuildMessageRendersAndLimitsMentions(t *testing.T) {
	d := sampleTemplateData()
	ev := modules.WelcomeEvent{
		Message: "Hi {mention}, welcome to {server} ({server(members)})",
		Embed: modules.WelcomeEmbed{
			Enabled:     true,
			Color:       "#5865f2",
			Title:       "Welcome {user}",
			Description: "Member #{ord:{server(members)}}",
			Fields:      []modules.WelcomeEmbedField{{Name: "Account", Value: "{user(proper)}", Inline: true}},
			Timestamp:   "2025-01-02T03:04:05Z",
		},
	}
	msg, warning := buildMessage(ev, d)
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	if msg.Content != "Hi <@42>, welcome to Server (1024)" {
		t.Fatalf("content = %q", msg.Content)
	}
	if msg.AllowedMentions == nil || len(msg.AllowedMentions.Parse) != 0 {
		t.Fatalf("mentions should not parse anything: %+v", msg.AllowedMentions)
	}
	if len(msg.AllowedMentions.Users) != 1 || msg.AllowedMentions.Users[0] != snowflake.ID(42) {
		t.Fatalf("explicit user mention not allowed: %+v", msg.AllowedMentions)
	}
	if len(msg.Embeds) != 1 {
		t.Fatalf("embeds = %d, want 1", len(msg.Embeds))
	}
	e := msg.Embeds[0]
	if e.Title != "Welcome Nick" || e.Description != "Member #1024th" || e.Color != 0x5865f2 {
		t.Fatalf("embed = %+v", e)
	}
	if len(e.Fields) != 1 || e.Fields[0].Name != "Account" || e.Fields[0].Value != "galactus" {
		t.Fatalf("fields = %+v", e.Fields)
	}
	if e.Fields[0].Inline == nil || !*e.Fields[0].Inline {
		t.Fatalf("inline flag lost: %+v", e.Fields[0])
	}
	if e.Timestamp == nil {
		t.Fatal("timestamp missing")
	}
}

func TestBuildMessageEmptyEmbedIsNotSent(t *testing.T) {
	d := sampleTemplateData()
	msg, _ := buildMessage(modules.WelcomeEvent{
		Message: "Hi {user}",
		Embed:   modules.WelcomeEmbed{Enabled: true},
	}, d)
	if len(msg.Embeds) != 0 {
		t.Fatalf("empty embed should not be sent: %+v", msg.Embeds)
	}
}

func TestBuildMessageDropsOversizedEmbed(t *testing.T) {
	d := sampleTemplateData()
	ev := modules.WelcomeEvent{
		Message: "",
		Embed: modules.WelcomeEmbed{
			Enabled:     true,
			Title:       strings.Repeat("t", maxTitleLen),
			Description: strings.Repeat("d", maxDescriptionLen),
			Footer:      strings.Repeat("f", maxFooterLen),
			Author:      modules.WelcomeEmbedAuthor{Name: strings.Repeat("a", maxAuthorNameLen)},
		},
	}
	msg, warning := buildMessage(ev, d)
	if len(msg.Embeds) != 0 || !strings.Contains(warning, "embed is") {
		t.Fatalf("embeds=%d warning=%q", len(msg.Embeds), warning)
	}
	if !messageEmpty(msg) {
		t.Fatal("message with dropped embed should be empty")
	}
}

func TestBuildEmbedDropsInvalidRenderedURL(t *testing.T) {
	d := sampleTemplateData()
	e := buildEmbed(modules.WelcomeEmbed{Enabled: true, Title: "Hi", ImageURL: "{server}"}, d)
	if e == nil {
		t.Fatal("embed should render")
	}
	if e.Image != nil {
		t.Fatalf("invalid expanded URL should be dropped: %+v", e.Image)
	}
}

func TestBuildEmbedTemplateURLStillWorks(t *testing.T) {
	d := sampleTemplateData()
	e := buildEmbed(modules.WelcomeEmbed{Enabled: true, Title: "Hi", ImageURL: "https://example.com/{user(id)}.png"}, d)
	if e == nil || e.Image == nil || e.Image.URL != "https://example.com/42.png" {
		t.Fatalf("templated URL not rendered: %+v", e)
	}
}

func TestBuildEmbedRenderedContentOnly(t *testing.T) {
	d := sampleTemplateData()
	// Title/description made only of an empty variable render to "".
	e := buildEmbed(modules.WelcomeEmbed{Enabled: true, Title: "{nickname}", Description: "{nickname}"}, templateData{User: d.User, GuildID: 1})
	if e != nil {
		t.Fatalf("empty rendered embed should be nil: %+v", e)
	}
}
