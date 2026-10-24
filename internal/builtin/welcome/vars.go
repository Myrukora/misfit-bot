package welcome

import (
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// templateData is the resolved member/guild snapshot a welcome or leave
// template renders against. Member is nil when the member object is unknown
// (a leave event for an uncached member).
type templateData struct {
	User        discord.User
	Member      *discord.Member
	GuildID     snowflake.ID
	GuildName   string
	GuildIcon   string
	MemberCount int
	JoinedAt    time.Time
}

func (d templateData) displayName() string {
	if d.Member != nil {
		return d.Member.EffectiveName()
	}
	return d.User.EffectiveName()
}

func (d templateData) nickname() string {
	if d.Member != nil && d.Member.Nick != nil {
		return *d.Member.Nick
	}
	return ""
}

func (d templateData) avatarURL() string {
	if d.Member != nil {
		return d.Member.EffectiveAvatarURL(discord.WithSize(512))
	}
	return d.User.EffectiveAvatarURL(discord.WithSize(512))
}

var (
	// {ord:123} — the number is usually another variable already expanded,
	// e.g. {ord:{server(members)}}.
	ordPattern = regexp.MustCompile(`\{ord:([0-9]+)\}`)
	// {random: one, two, three} — a comma-separated pick.
	randomPattern = regexp.MustCompile(`\{random:([^{}]*)\}`)
)

// renderTemplate expands every supported variable in s. Unknown placeholders
// are left untouched (useful when debugging a template).
func renderTemplate(s string, d templateData) string {
	if s == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"{mention}", "<@"+d.User.ID.String()+">",
		"{user(proper)}", d.User.Tag(),
		"{user(id)}", d.User.ID.String(),
		"{user(avatar)}", d.avatarURL(),
		"{user(created)}", discordTimestamp(d.User.ID.Time()),
		"{user(joined)}", discordTimestamp(d.JoinedAt),
		"{username}", d.User.Username,
		"{nickname}", d.nickname(),
		"{user}", d.displayName(),
		"{server(members)}", strconv.Itoa(d.MemberCount),
		"{server(id)}", d.GuildID.String(),
		"{server(icon)}", d.GuildIcon,
		"{server}", d.GuildName,
	)
	out := replacer.Replace(s)
	out = randomPattern.ReplaceAllStringFunc(out, func(token string) string {
		list := randomPattern.FindStringSubmatch(token)
		if len(list) < 2 {
			return token
		}
		items := make([]string, 0, 4)
		for _, item := range strings.Split(list[1], ",") {
			if item = strings.TrimSpace(item); item != "" {
				items = append(items, item)
			}
		}
		if len(items) == 0 {
			return token
		}
		return items[rand.IntN(len(items))]
	})
	// Nested {ord:…} can expose another {ord:…}; expand a few rounds, bounded
	// so a pathological template can never spin.
	for i := 0; i < 4; i++ {
		next := ordPattern.ReplaceAllStringFunc(out, func(token string) string {
			m := ordPattern.FindStringSubmatch(token)
			if len(m) < 2 {
				return token
			}
			n, err := strconv.Atoi(m[1])
			if err != nil {
				return token
			}
			return ordinal(n)
		})
		if next == out {
			break
		}
		out = next
	}
	return out
}

func ordinal(n int) string {
	suffix := "th"
	switch n % 100 {
	case 11, 12, 13:
	default:
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// discordTimestamp renders a Discord relative timestamp that each viewer sees
// in their own locale/timezone. Zero time renders empty.
func discordTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return "<t:" + strconv.FormatInt(t.Unix(), 10) + ":F>"
}
