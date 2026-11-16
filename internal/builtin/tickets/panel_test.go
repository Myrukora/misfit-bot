package tickets

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
)

// TestButtonStyleOf pins the config-name to Discord-style mapping, including
// the legacy default: an empty or unknown value renders the grey (secondary)
// button panels used before the field existed. An unset style must never
// silently become the blurple primary again.
func TestButtonStyleOf(t *testing.T) {
	cases := map[string]discord.ButtonStyle{
		"":          discord.ButtonStyleSecondary,
		"secondary": discord.ButtonStyleSecondary,
		"primary":   discord.ButtonStylePrimary,
		"PRIMARY":   discord.ButtonStylePrimary,
		"success":   discord.ButtonStyleSuccess,
		"danger":    discord.ButtonStyleDanger,
		"purple":    discord.ButtonStyleSecondary, // rejected by config, safe render
	}
	for in, want := range cases {
		if got := buttonStyleOf(in); got != want {
			t.Errorf("buttonStyleOf(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestBuildPanelRowsAppearance pins that the type's appearance reaches the
// posted Open button: label fallback, emoji, style, and the disabled flag a
// suspended panel or disabled type sets.
func TestBuildPanelRowsAppearance(t *testing.T) {
	typ := TypeConfig{
		Key: "staff", Label: "Staff Ticket", Enabled: true,
		ButtonEmoji: "🎫", ButtonStyle: "success",
	}
	rows := buildPanelRows(PanelConfig{Name: "staff_panel", TypeKey: "staff"}, typ)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row, ok := rows[0].(discord.ActionRowComponent)
	if !ok || len(row.Components) != 1 {
		t.Fatalf("row = %#v, want one action row with one button", rows[0])
	}
	btn, ok := row.Components[0].(discord.ButtonComponent)
	if !ok {
		t.Fatalf("component = %T, want discord.ButtonComponent", row.Components[0])
	}
	if btn.Style != discord.ButtonStyleSuccess {
		t.Errorf("style = %v, want success", btn.Style)
	}
	if btn.Label != "Staff Ticket" {
		t.Errorf("label = %q, want the type label as fallback", btn.Label)
	}
	if btn.Emoji == nil || btn.Emoji.Name != "🎫" {
		t.Errorf("emoji = %#v, want the configured emoji", btn.Emoji)
	}
	if btn.CustomID != "tickets:open:staff_panel" {
		t.Errorf("custom id = %q, want tickets:open:staff_panel", btn.CustomID)
	}
	if btn.Disabled {
		t.Error("enabled type on a live panel must not render disabled")
	}

	// Suspended panel: same button, greyed out.
	rows = buildPanelRows(PanelConfig{Name: "staff_panel", TypeKey: "staff", Suspended: true}, typ)
	row = rows[0].(discord.ActionRowComponent)
	if btn = row.Components[0].(discord.ButtonComponent); !btn.Disabled {
		t.Error("suspended panel must render the button disabled")
	}
}
