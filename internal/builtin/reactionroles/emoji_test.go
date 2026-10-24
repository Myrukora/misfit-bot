package reactionroles

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

func TestNormalizeEmoji(t *testing.T) {
	cases := map[string]string{
		"👍":                            "👍",
		"❤️":                           "❤️",
		"wave:123456789012345678":      "wave:123456789012345678",
		"<:wave:123456789012345678>":   "wave:123456789012345678",
		"<a:dance:123456789012345678>": "dance:123456789012345678",
		":wave:123456789012345678":     "wave:123456789012345678",
		"  👍  ":                        "👍",
	}
	for in, want := range cases {
		got, err := normalizeEmoji(in)
		if err != nil {
			t.Errorf("normalizeEmoji(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normalizeEmoji(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "   ", "123456789012345678", "hello world", "<:bad name:123>", "wave:abc"} {
		if _, err := normalizeEmoji(bad); err == nil {
			t.Errorf("normalizeEmoji(%q) should fail", bad)
		}
	}
}

func TestEmojiMatches(t *testing.T) {
	unicode := "👍"
	name := "wave"
	customID := snowflake.ID(123)

	if !emojiMatches("👍", discord.PartialEmoji{Name: &unicode}) {
		t.Fatal("unicode emoji should match")
	}
	if emojiMatches("👎", discord.PartialEmoji{Name: &unicode}) {
		t.Fatal("different unicode emoji should not match")
	}
	// Custom emojis match by ID, so a renamed emoji keeps working.
	if !emojiMatches("wave:123", discord.PartialEmoji{Name: &name, ID: &customID}) {
		t.Fatal("custom emoji should match by id")
	}
	renamed := "renamed"
	if !emojiMatches("wave:123", discord.PartialEmoji{Name: &renamed, ID: &customID}) {
		t.Fatal("custom emoji should match by id after a rename")
	}
	otherID := snowflake.ID(456)
	if emojiMatches("wave:123", discord.PartialEmoji{Name: &name, ID: &otherID}) {
		t.Fatal("different custom emoji should not match")
	}
	// Variation selectors differ between clients; both forms are the same
	// reaction.
	heartVS := "❤️"
	heart := "❤"
	if !emojiMatches(heartVS, discord.PartialEmoji{Name: &heart}) {
		t.Fatal("variation-selector-free unicode emoji should match")
	}
	if emojiUniqueKey(heartVS) != emojiUniqueKey(heart) {
		t.Fatal("variation selectors should share a key")
	}
}

func TestEmojiUniqueKey(t *testing.T) {
	// Custom emoji IDs are the identity; names may differ.
	if emojiUniqueKey("wave:123") != emojiUniqueKey("renamed:123") {
		t.Fatal("custom emojis with the same id should share a key")
	}
	if emojiUniqueKey("wave:123") != "id:123" {
		t.Fatalf("custom key = %q", emojiUniqueKey("wave:123"))
	}
	if emojiUniqueKey("👍") != "u:👍" {
		t.Fatalf("unicode key = %q", emojiUniqueKey("👍"))
	}
}
