package reactionroles

import (
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// Emoji storage format is Discord's reaction string: the Unicode emoji itself
// or "name:id" for a custom emoji. normalizeEmoji also accepts the mention
// forms <:name:id>, <a:name:id> and ":name:id" so the dashboard can take a
// pasted emoji.

func normalizeEmoji(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("emoji is required")
	}
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		s = strings.TrimSuffix(strings.TrimPrefix(s, "<"), ">")
		s = strings.TrimPrefix(s, "a:")
		s = strings.TrimPrefix(s, ":")
	}
	if strings.Contains(s, ":") {
		parts := strings.Split(s, ":")
		if len(parts) == 3 && parts[0] == "" {
			parts = parts[1:]
		}
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", fmt.Errorf("custom emoji must be name:id or a custom emoji mention")
		}
		name, rawID := parts[0], parts[1]
		if !validEmojiName(name) {
			return "", fmt.Errorf("invalid custom emoji name")
		}
		id, err := snowflake.Parse(rawID)
		if err != nil || id == 0 {
			return "", fmt.Errorf("invalid custom emoji id")
		}
		return name + ":" + id.String(), nil
	}
	if _, err := snowflake.Parse(s); err == nil {
		return "", fmt.Errorf("use name:id for custom emojis")
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return "", fmt.Errorf("emoji must be a Unicode emoji or name:id")
	}
	if len([]rune(s)) > 64 {
		return "", fmt.Errorf("emoji is too long")
	}
	return s, nil
}

func validEmojiName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

// emojiUniqueKey identifies an emoji regardless of a custom emoji's current
// name: custom emojis match by ID, Unicode by the character sequence.
func emojiUniqueKey(stored string) string {
	if i := strings.LastIndexByte(stored, ':'); i > 0 {
		if id, err := snowflake.Parse(stored[i+1:]); err == nil && id != 0 {
			return "id:" + id.String()
		}
	}
	return "u:" + stripVariationSelectors(stored)
}

// emojiMatches reports whether a stored mapping refers to the reaction's emoji.
// Custom emojis match by ID (names can be renamed), Unicode by value. The
// variation selector is ignored because clients disagree on whether "❤️" and
// "❤" carry U+FE0F in reaction payloads.
func emojiMatches(stored string, e discord.PartialEmoji) bool {
	if e.Name == nil {
		return false
	}
	if e.ID != nil {
		return strings.HasSuffix(stored, ":"+e.ID.String())
	}
	if stored == *e.Name {
		return true
	}
	return stripVariationSelectors(stored) == stripVariationSelectors(*e.Name)
}

func stripVariationSelectors(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\uFE0E' || r == '\uFE0F' {
			return -1
		}
		return r
	}, s)
}
