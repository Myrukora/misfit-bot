package imagefilter

import (
	"sync/atomic"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

// newTestModule wires a module around the fake manager so the gateway gate can
// be driven directly, without a Discord client.
func newTestModule(t *testing.T) (*ImageFilterModule, *manager, *config) {
	t.Helper()
	mgr, cfg, _, _ := newTestManager(t)
	return &ImageFilterModule{ok: true, mgr: mgr, cfg: cfg}, mgr, cfg
}

// enabledGuild flips the guild on and warms the fake model so the gate passes.
func enabledGuild(t *testing.T, mod *ImageFilterModule, mgr *manager, cfg *config, guildID string) {
	t.Helper()
	if err := mgr.SetGuildEnabled(guildID, true); err != nil {
		t.Fatalf("enable guild: %v", err)
	}
	set := defaultGuildConfig()
	set.Enabled = true
	set.Threshold = 0.99 // no refs → nothing can match
	if err := cfg.SetGuildConfig(guildID, set); err != nil {
		t.Fatalf("set guild config: %v", err)
	}
}

func imageAttachment(proxyURL string) discord.Attachment {
	ct := "image/png"
	return discord.Attachment{ContentType: &ct, ProxyURL: proxyURL, Filename: "x.png"}
}

func guildMessage(guildID snowflake.ID, author discord.User, atts ...discord.Attachment) *events.GuildMessageCreate {
	return &events.GuildMessageCreate{GenericGuildMessage: &events.GenericGuildMessage{
		MessageID: 1,
		GuildID:   guildID,
		ChannelID: 2,
		Message: discord.Message{
			ID:          1,
			ChannelID:   2,
			Author:      author,
			Attachments: atts,
		},
	}}
}

// TestGateSubmitsEveryAttachment pins the Python-parity behavior: the gate must
// submit EVERY distinct image attachment, not stop at the first one (a spam
// poster can put the offending image second).
func TestGateSubmitsEveryAttachment(t *testing.T) {
	mod, mgr, cfg := newTestModule(t)
	mgr.startWorker()
	defer mgr.stopWorker()
	enabledGuild(t, mod, mgr, cfg, "1")

	var fetches atomic.Int32
	mgr.fetchFn = func(string) ([]byte, error) {
		fetches.Add(1)
		return smallPNG(t, 8, 8, rgbaOf(1, 2, 3)), nil
	}

	mod.gateMessage(guildMessage(1, discord.User{ID: 7}, imageAttachment("https://media.discordapp.net/a.png")))
	mgr.drainForTest(t, 1)
	if n := fetches.Load(); n != 1 {
		t.Fatalf("one attachment → fetches = %d, want 1", n)
	}

	mod.gateMessage(guildMessage(1, discord.User{ID: 7},
		imageAttachment("https://media.discordapp.net/a.png"),
		imageAttachment("https://media.discordapp.net/b.png"),
	))
	mgr.drainForTest(t, 3) // cumulative: the earlier job already counted
	if n := fetches.Load(); n != 3 {
		t.Fatalf("two attachments → fetches = %d, want 3 total", n)
	}
}

// TestGateSkipsDuplicateAndNonImage pins the filters: identical URLs are
// submitted once, non-image attachments and bots are never submitted.
func TestGateSkipsDuplicateAndNonImage(t *testing.T) {
	mod, mgr, cfg := newTestModule(t)
	mgr.startWorker()
	defer mgr.stopWorker()
	enabledGuild(t, mod, mgr, cfg, "1")

	var fetches atomic.Int32
	mgr.fetchFn = func(string) ([]byte, error) {
		fetches.Add(1)
		return smallPNG(t, 8, 8, rgbaOf(1, 2, 3)), nil
	}

	// Duplicate URL → exactly one job.
	mod.gateMessage(guildMessage(1, discord.User{ID: 7},
		imageAttachment("https://media.discordapp.net/a.png"),
		imageAttachment("https://media.discordapp.net/a.png"),
	))
	mgr.drainForTest(t, 1)
	if n := fetches.Load(); n != 1 {
		t.Fatalf("duplicate URL → fetches = %d, want 1", n)
	}

	// Non-image attachment, nil content type, and empty proxy URL → no job.
	text := "text/plain"
	noProxy := discord.Attachment{ContentType: &text, ProxyURL: "https://media.discordapp.net/x.txt"}
	nilType := discord.Attachment{ProxyURL: "https://media.discordapp.net/y.png"}
	emptyProxy := discord.Attachment{ContentType: &text, ProxyURL: ""}
	mod.gateMessage(guildMessage(1, discord.User{ID: 7}, noProxy, nilType, emptyProxy))
	if n := fetches.Load(); n != 1 {
		t.Fatalf("non-image attachments → fetches = %d, want 1", n)
	}

	// Bot author → dropped.
	mod.gateMessage(guildMessage(1, discord.User{ID: 7, Bot: true}, imageAttachment("https://media.discordapp.net/c.png")))
	if n := fetches.Load(); n != 1 {
		t.Fatalf("bot author → fetches = %d, want 1", n)
	}

	// Disabled guild → dropped.
	mod.gateMessage(guildMessage(2, discord.User{ID: 7}, imageAttachment("https://media.discordapp.net/d.png")))
	if n := fetches.Load(); n != 1 {
		t.Fatalf("disabled guild → fetches = %d, want 1", n)
	}

	// Cold model → dropped.
	if err := mgr.SetGuildEnabled("1", false); err != nil {
		t.Fatal(err)
	}
	mod.gateMessage(guildMessage(1, discord.User{ID: 7}, imageAttachment("https://media.discordapp.net/e.png")))
	if n := fetches.Load(); n != 1 {
		t.Fatalf("cold model → fetches = %d, want 1", n)
	}
}

// TestGateMemberAuthor pins the member-shaped message path (Author empty,
// Member populated) for both the bot check and the author ID.
func TestGateMemberAuthor(t *testing.T) {
	mod, mgr, cfg := newTestModule(t)
	mgr.startWorker()
	defer mgr.stopWorker()
	enabledGuild(t, mod, mgr, cfg, "1")

	mgr.fetchFn = func(string) ([]byte, error) { return smallPNG(t, 8, 8, rgbaOf(1, 2, 3)), nil }

	e := guildMessage(1, discord.User{}, imageAttachment("https://media.discordapp.net/a.png"))
	e.Message.Member = &discord.Member{User: discord.User{ID: 42}}
	mod.gateMessage(e)
	mgr.drainForTest(t, 1)

	// The member's ID must be used for the job (the embed path reports it).
	if isBotAuthor(e) {
		t.Error("a human member must not be treated as a bot")
	}
	if got := authorIDOf(e); got != "42" {
		t.Errorf("authorIDOf = %q, want 42", got)
	}

	// Bot member shape → dropped.
	e.Message.Member = &discord.Member{User: discord.User{ID: 43, Bot: true}}
	e.Message.Attachments = []discord.Attachment{imageAttachment("https://media.discordapp.net/b.png")}
	mod.gateMessage(e)
	if !isBotAuthor(e) {
		t.Error("member bot must be detected as a bot")
	}
}
