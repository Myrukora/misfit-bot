package tickets

import (
	"testing"
)

// TestConfigV3FreshRoundTrip covers a native per-guild file: types + panels
// round-trip with button label/emoji and panel registry intact.
func TestConfigV3FreshRoundTrip(t *testing.T) {
	dir := t.TempDir()
	guildID := "111222333444555666"
	cfg := loadGuildConfig(dir, guildID, nil)
	cfg.LogChannel = "999888777666555444"
	cfg.Types["contact"] = &TypeConfig{
		Key: "contact", Label: "Contact Staff", Enabled: true,
		Category:  "111222333444555666",
		PingRoles: []string{"111"}, HelperRoles: []string{"222"},
		WelcomeMsg:  "Welcome {user.mention}!",
		EmbedBody:   "{user} opened a ticket.",
		ButtonLabel: "Open Ticket",
		ButtonEmoji: "🎫",
		Color:       0x5865F2,
	}
	cfg.Panels["contact_staff"] = PanelConfig{
		Name: "contact_staff", ChannelID: "121212121212121212",
		MessageID: "343434343434343434", TypeKey: "contact",
		Title: "Need help?", Description: "Click below.",
	}
	if err := cfg.save(dir, guildID); err != nil {
		t.Fatal(err)
	}
	got := loadGuildConfig(dir, guildID, nil)
	tc := got.Types["contact"]
	if tc == nil || tc.ButtonEmoji != "🎫" || tc.ButtonLabel != "Open Ticket" || !tc.AllowClaimOn() {
		t.Fatalf("type round-trip broken: %+v", tc)
	}
	p := got.Panels["contact_staff"]
	if p.Name == "" || p.MessageID != "343434343434343434" || p.TypeKey != "contact" || p.Suspended {
		t.Fatalf("panel round-trip broken: %+v", p)
	}
}

func TestPanelNameValidation(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"contact_staff", true},
		{"a", true},
		{"Contact-Staff-2", true},
		{"", false},
		{"has space", false},
		{"../evil", false},
		{"dot.dot", false},
	}
	for _, c := range cases {
		if got := validPanelName(c.name); got != c.want {
			t.Errorf("validPanelName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
