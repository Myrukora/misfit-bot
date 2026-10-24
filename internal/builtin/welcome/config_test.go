package welcome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misfit/bot/modules"
)

func validEvent() modules.WelcomeEvent {
	return modules.WelcomeEvent{
		Enabled:   true,
		ChannelID: "2",
		Message:   "Welcome {mention} to {server}!",
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	gid := "1"
	cfg := modules.WelcomeConfig{
		Join:  validEvent(),
		Leave: modules.WelcomeEvent{Enabled: false, ChannelID: "3", Message: "Bye {user}"},
	}
	cfg.Join.Embed = modules.WelcomeEmbed{
		Enabled:     true,
		Title:       "Hello",
		Description: "Welcome to **{server}**",
		Fields:      []modules.WelcomeEmbedField{{Name: "Members", Value: "{server(members)}", Inline: true}},
	}
	if err := st.Save(gid, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, gid+".yaml")); err != nil {
		t.Fatalf("config file missing: %v", err)
	}
	// A fresh store must read the same values back from disk.
	st2, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st2.Get(gid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Join.Message != cfg.Join.Message || got.Join.Embed.Title != "Hello" || !got.Join.Embed.Fields[0].Inline {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestStoreMissingIsZeroConfig(t *testing.T) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Get("1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Join.Enabled || got.Leave.Enabled {
		t.Fatalf("missing config should be disabled: %+v", got)
	}
}

func TestStoreRejectsInvalidGuildIDs(t *testing.T) {
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range []string{"", "0", "..", "../x", "abc", "01"} {
		if _, err := st.Get(gid); err == nil {
			t.Errorf("Get(%q) should fail", gid)
		}
		if err := st.Save(gid, modules.WelcomeConfig{}); err == nil {
			t.Errorf("Save(%q) should fail", gid)
		}
	}
}

func TestStoreCorruptFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.yaml"), []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get("1"); err == nil {
		t.Fatal("corrupt config should be an error")
	}
}

func TestNormalizeConfig(t *testing.T) {
	cfg := modules.WelcomeConfig{Join: validEvent()}
	cfg.Join.ChannelID = " 2 "
	cfg.Join.Embed.Color = "#ABC"
	cfg.Join.Embed.Timestamp = "2025-01-02T03:04:05+02:00"
	norm, err := normalizeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if norm.Join.ChannelID != "2" {
		t.Errorf("channel = %q", norm.Join.ChannelID)
	}
	if norm.Join.Embed.Color != "#aabbcc" {
		t.Errorf("color = %q", norm.Join.Embed.Color)
	}
	if norm.Join.Embed.Timestamp != "2025-01-02T01:04:05Z" {
		t.Errorf("timestamp = %q", norm.Join.Embed.Timestamp)
	}
}

func TestValidateConfigLimits(t *testing.T) {
	long := strings.Repeat("a", maxContentLen+1)
	cases := []struct {
		name string
		ev   modules.WelcomeEvent
		want string
	}{
		{"long message", modules.WelcomeEvent{Message: long}, "message is"},
		{"too many fields", modules.WelcomeEvent{Embed: modules.WelcomeEmbed{Fields: make([]modules.WelcomeEmbedField, maxFields+1)}}, "too many fields"},
		{"long title", modules.WelcomeEvent{Embed: modules.WelcomeEmbed{Title: strings.Repeat("t", maxTitleLen+1)}}, "title is too long"},
		{"bad url", modules.WelcomeEvent{Embed: modules.WelcomeEmbed{ImageURL: "ftp://example.com/x.png"}}, "image url"},
		{"bad color", modules.WelcomeEvent{Embed: modules.WelcomeEmbed{Color: "red"}}, "color must be"},
		{"enabled without channel", modules.WelcomeEvent{Enabled: true, Message: "hi"}, "choose a channel"},
		{"enabled without content", modules.WelcomeEvent{Enabled: true, ChannelID: "2"}, "write a message"},
		{"negative autodelete", modules.WelcomeEvent{AutoDelete: -1}, "auto-delete"},
		{"huge autodelete", modules.WelcomeEvent{AutoDelete: maxAutoDeleteSecs + 1}, "auto-delete"},
	}
	for _, tc := range cases {
		cfg := modules.WelcomeConfig{Join: tc.ev}
		if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
	if err := validateConfig(modules.WelcomeConfig{Join: validEvent()}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateEmbedTotalLength(t *testing.T) {
	e := modules.WelcomeEmbed{
		Enabled:     true,
		Title:       strings.Repeat("t", maxTitleLen),
		Description: strings.Repeat("d", maxDescriptionLen),
		Footer:      strings.Repeat("f", maxFooterLen),
		Author:      modules.WelcomeEmbedAuthor{Name: strings.Repeat("a", maxAuthorNameLen)},
	}
	cfg := modules.WelcomeConfig{Join: modules.WelcomeEvent{Enabled: true, ChannelID: "2", Embed: e}}
	if err := validateConfig(cfg); err == nil || !strings.Contains(err.Error(), "embed is too long") {
		t.Fatalf("err = %v, want embed too long", err)
	}
}

func TestValidateTimestamp(t *testing.T) {
	cfg := modules.WelcomeConfig{Join: validEvent()}
	cfg.Join.Embed.Timestamp = "tomorrow"
	if _, err := normalizeConfig(cfg); err == nil {
		t.Fatal("invalid timestamp should be rejected")
	}
	cfg.Join.Embed.Timestamp = time.Now().UTC().Format(time.RFC3339)
	if _, err := normalizeConfig(cfg); err != nil {
		t.Fatalf("valid timestamp rejected: %v", err)
	}
}
