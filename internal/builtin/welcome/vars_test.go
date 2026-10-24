package welcome

import (
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
)

func strPtr(s string) *string { return &s }

func sampleTemplateData() templateData {
	joined := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	user := discord.User{ID: 42, Username: "galactus", GlobalName: strPtr("Gal"), Discriminator: "0"}
	return templateData{
		User:        user,
		Member:      &discord.Member{User: user, Nick: strPtr("Nick"), JoinedAt: &joined},
		GuildID:     7,
		GuildName:   "Server",
		GuildIcon:   "https://cdn.example/icon.png",
		MemberCount: 1024,
		JoinedAt:    joined,
	}
}

func TestRenderTemplateVariables(t *testing.T) {
	d := sampleTemplateData()
	cases := map[string]string{
		"{mention}":               "<@42>",
		"{user}":                  "Nick",
		"{nickname}":              "Nick",
		"{username}":              "galactus",
		"{user(proper)}":          "galactus",
		"{user(id)}":              "42",
		"{server}":                "Server",
		"{server(id)}":            "7",
		"{server(members)}":       "1024",
		"{server(icon)}":          "https://cdn.example/icon.png",
		"{ord:{server(members)}}": "1024th",
		"{random: only}":          "only",
		"{unknown}":               "{unknown}",
		"Hi {user} on {server}":   "Hi Nick on Server",
	}
	for in, want := range cases {
		if got := renderTemplate(in, d); got != want {
			t.Errorf("renderTemplate(%q) = %q, want %q", in, got, want)
		}
	}
	if got := renderTemplate("{user(created)}", d); !strings.HasPrefix(got, "<t:") {
		t.Errorf("{user(created)} = %q, want a Discord timestamp", got)
	}
	if got := renderTemplate("{user(joined)}", d); !strings.HasPrefix(got, "<t:") {
		t.Errorf("{user(joined)} = %q, want a Discord timestamp", got)
	}
}

func TestRenderTemplateProperLegacyTag(t *testing.T) {
	d := sampleTemplateData()
	d.User.Discriminator = "0001"
	if got := renderTemplate("{user(proper)}", d); got != "galactus#0001" {
		t.Fatalf("{user(proper)} = %q, want galactus#0001", got)
	}
}

func TestRenderTemplateNoMember(t *testing.T) {
	user := discord.User{ID: 9, Username: "plain", GlobalName: strPtr("Display")}
	d := templateData{User: user, GuildID: 1}
	if got := renderTemplate("{user}|{nickname}", d); got != "Display|" {
		t.Fatalf("no-member render = %q", got)
	}
	if got := renderTemplate("{user(joined)}", d); got != "" {
		t.Fatalf("zero joined time = %q, want empty", got)
	}
}

func TestRenderTemplateRandomList(t *testing.T) {
	d := sampleTemplateData()
	for i := 0; i < 20; i++ {
		got := renderTemplate("{random: a, b, c}", d)
		if got != "a" && got != "b" && got != "c" {
			t.Fatalf("random picked %q", got)
		}
	}
	if got := renderTemplate("{random: }", d); got != "{random: }" {
		t.Fatalf("empty random list = %q, want token untouched", got)
	}
}

func TestOrdinal(t *testing.T) {
	cases := map[int]string{
		1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th",
		13: "13th", 21: "21st", 22: "22nd", 101: "101st", 111: "111th", 1024: "1024th",
	}
	for n, want := range cases {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}
