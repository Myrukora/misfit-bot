package leveling

import (
	"bytes"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"

	"github.com/misfit/bot/commands"
)

// TestRankCommandFiles pins the two response paths: a real attachment when the
// context supports files, and the embed fallback for web-executed commands.
func TestRankCommandFiles(t *testing.T) {
	m, r, b := newHarness(t)
	saveConfig(t, m, "1", enabledConfig())
	if _, err := m.store.SetXP("1", "3", 300); err != nil {
		t.Fatalf("SetXP: %v", err)
	}
	b.members["1/3"] = &discord.Member{User: discord.User{ID: 3, Username: "vixen"}}
	r.users[3] = &discord.User{ID: 3, Username: "vixen"}

	var files []*discord.File
	ctx := &commands.Context{
		GuildID: "1",
		Author:  discord.User{ID: 3},
		Respond: func(...discord.Embed) error {
			t.Fatal("the file path must not fall back to an embed")
			return nil
		},
		RespondWithFiles: func(f []*discord.File, _ ...discord.Embed) error {
			files = f
			return nil
		},
	}
	if err := m.executeRank(ctx); err != nil {
		t.Fatalf("executeRank: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if files[0].Name != "rank.png" {
		t.Fatalf("file name = %q, want rank.png", files[0].Name)
	}
	raw, err := io.ReadAll(files[0].Reader)
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil || cfg.Width != 800 || cfg.Height != 200 {
		t.Fatalf("attachment is not an 800x200 PNG: %+v %v", cfg, err)
	}

	// Web execution has no file responder: same stats, delivered as an embed.
	var embeds []discord.Embed
	ctx.RespondWithFiles = nil
	ctx.Respond = func(e ...discord.Embed) error {
		embeds = e
		return nil
	}
	if err := m.executeRank(ctx); err != nil {
		t.Fatalf("executeRank(embed): %v", err)
	}
	if len(embeds) != 1 {
		t.Fatalf("embeds = %d, want 1", len(embeds))
	}
	if embeds[0].Title != "Rank #1" {
		t.Fatalf("embed title = %q, want Rank #1", embeds[0].Title)
	}
}

// TestLeaderboardEmbed pins the paging footer and member lines.
func TestLeaderboardEmbed(t *testing.T) {
	m, _, b := newHarness(t)
	saveConfig(t, m, "1", enabledConfig())
	for userID, xp := range map[string]int64{"3": 300, "4": 900} {
		if _, err := m.store.SetXP("1", userID, xp); err != nil {
			t.Fatalf("SetXP: %v", err)
		}
	}
	b.guild = &discord.Guild{Name: "Guild HQ"}
	b.members["1/3"] = &discord.Member{User: discord.User{ID: 3, Username: "vixen"}}
	b.members["1/4"] = &discord.Member{User: discord.User{ID: 4, Username: "nova"}}

	var embeds []discord.Embed
	ctx := &commands.Context{
		GuildID: "1",
		Respond: func(e ...discord.Embed) error {
			embeds = e
			return nil
		},
	}
	if err := m.executeLeaderboard(ctx); err != nil {
		t.Fatalf("executeLeaderboard: %v", err)
	}
	if len(embeds) != 1 {
		t.Fatalf("embeds = %d, want 1", len(embeds))
	}
	got := embeds[0]
	if !strings.Contains(got.Title, "Guild HQ") {
		t.Fatalf("title = %q, want the guild name", got.Title)
	}
	if !strings.Contains(got.Description, "#1") || !strings.Contains(got.Description, "nova") {
		t.Fatalf("description = %q, want the top member first", got.Description)
	}
	if !strings.Contains(got.Footer.Text, "Page 1/1") || !strings.Contains(got.Footer.Text, "2 ranked members") {
		t.Fatalf("footer = %q", got.Footer.Text)
	}
}

// TestRankRequiresGuild pins the outside-a-server error.
func TestRankRequiresGuild(t *testing.T) {
	m, _, _ := newHarness(t)
	var embeds []discord.Embed
	ctx := &commands.Context{
		Respond: func(e ...discord.Embed) error {
			embeds = e
			return nil
		},
	}
	if err := m.executeRank(ctx); err != nil {
		t.Fatalf("executeRank: %v", err)
	}
	if len(embeds) != 1 || !strings.Contains(embeds[0].Title, "Rank") {
		t.Fatalf("embeds = %+v, want one error embed", embeds)
	}
}
