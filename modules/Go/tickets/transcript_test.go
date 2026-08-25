package main

import (
	"strings"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

func htmlTicket() *modules.Ticket {
	t := &modules.Ticket{
		ID: "staff-0007", Type: "staff", Group: "staff", GuildID: "999",
		OpenerID: "42", Status: "open",
		OpenedAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC),
	}
	t.Log = []modules.LogEntry{
		{MsgID: "1", AuthorID: "42", AuthorName: "vixen", Timestamp: time.Now().UTC(),
			Content: "My game **crashes** <script>alert(1)</script>"},
		{MsgID: "2", AuthorID: "7", AuthorName: "helper", Timestamp: time.Now().UTC(),
			Content: "try this", Attachments: []modules.Media{
				{URL: "https://cdn.discordapp.com/a.png", Kind: "image", Filename: "a.png"},
			}},
		{MsgID: "3", AuthorID: "42", AuthorName: "vixen", Timestamp: time.Now().UTC(),
			Content: "deleted msg", Deleted: true},
	}
	return t
}

func TestTranscriptHTMLEscapesAndStructures(t *testing.T) {
	out := buildTranscriptHTML(htmlTicket(), "Test Guild")
	if !strings.Contains(out, "ticket-staff-0007") {
		t.Fatal("missing ticket id in title/body")
	}
	if strings.Contains(out, "<script>alert") {
		t.Fatal("XSS: raw script tag leaked into transcript")
	}
	if !strings.Contains(out, "&lt;script&gt;") && !strings.Contains(out, "alert(1)") == false {
		t.Fatal("escaping sanity broken")
	}
	for _, want := range []string{"crashes", "try this", "a.png"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in transcript", want)
		}
	}
	if !strings.Contains(out, "msg deleted") {
		t.Fatal("deleted message not marked")
	}
	if !strings.Contains(out, "<img class='attachment") {
		t.Fatal("image attachment not rendered as image")
	}
}

func TestBuildChannelNameCollision(t *testing.T) {
	at := time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)
	taken := map[string]bool{}
	n1 := buildChannelName("Vixen Fox", at, taken)
	n2 := buildChannelName("Vixen Fox", at, taken)
	n3 := buildChannelName("Vixen Fox", at, taken)
	if n1 != "vixen-fox-08-25-26" {
		t.Fatalf("n1 = %q", n1)
	}
	if n2 != "vixen-fox-08-25-26-2" || n3 != "vixen-fox-08-25-26-3" {
		t.Fatalf("collision suffixes wrong: %q %q", n2, n3)
	}
}

func TestSanitizeChannelName(t *testing.T) {
	cases := map[string]string{
		"Vixen":        "vixen",
		"Cool User 77": "cool-user-77",
		"日本語user":      "user",
		"":             "ticket",
		"---":          "ticket",
	}
	for in, want := range cases {
		if got := sanitizeChannelName(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}
