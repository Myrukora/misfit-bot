package main

import (
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/misfit/bot/modules"
)

func tmplTicket() *modules.Ticket {
	return &modules.Ticket{
		ID: "staff-0007", Group: "staff", GuildID: "999",
		OpenerID: "42", Status: "open", OpenedAt: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC),
	}
}

func tmplUser() discord.User {
	id := snowflake.ID(42)
	name := "vixen"
	return discord.User{ID: id, Username: name, Discriminator: "0"}
}

func TestRenderTemplateAllTokens(t *testing.T) {
	g := GroupConfig{Key: "staff", Label: "Staff"}
	tk := tmplTicket()
	tmpl := "{user} | {user.mention} | {user.id} | {user.name} | {guild.name} | {guild.id} | {ticket.id} | {ticket.group} | {opener.mention}"
	out := renderTemplate(tmpl, tk, g, tmplUser(), "Test Guild")
	want := "vixen | <@42> | 42 | vixen | Test Guild | 999 | staff-0007 | staff | <@42>"
	if out != want {
		t.Fatalf("got %q\nwant %q", out, want)
	}
}

func TestRenderTemplateUnknownTokenKept(t *testing.T) {
	out := renderTemplate("hello {unknown.token} {user}", nil, GroupConfig{}, tmplUser(), "")
	if !strings.Contains(out, "{unknown.token}") {
		t.Fatalf("unknown tokens must be left as-is, got %q", out)
	}
	if strings.Contains(out, "{user}") {
		t.Fatalf("{user} must resolve, got %q", out)
	}
}

func TestRenderTemplateNilTicketSafe(t *testing.T) {
	// Ticket-dependent tokens with a nil ticket must not panic; they may
	// resolve empty but unknown/nil-safe output is required.
	out := renderTemplate("{ticket.id} ok", nil, GroupConfig{}, tmplUser(), "G")
	if out == "" {
		t.Fatal("must produce output")
	}
}

func TestRenderTemplateNoFetchNeededForStaticText(t *testing.T) {
	// Pure substitution path — no REST calls — must be deterministic.
	a := renderTemplate("**bold** {ticket.group}", tmplTicket(), GroupConfig{Key: "staff"}, tmplUser(), "")
	b := renderTemplate("**bold** {ticket.group}", tmplTicket(), GroupConfig{Key: "staff"}, tmplUser(), "")
	if a != b || a != "**bold** staff" {
		t.Fatalf("determinism broken: %q vs %q", a, b)
	}
}
