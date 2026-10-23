package dashboard

import (
	"strings"
	"testing"

	"github.com/misfit/bot/modules"
)

// TestLogChannelOptions: the imagefilter log-channel picker must offer only
// channel kinds that can receive a message — a log embed posted to a voice,
// category or forum channel is an API error — and the list must be sorted by
// name regardless of cache order.
func TestLogChannelOptions(t *testing.T) {
	in := []entityOpt{
		{ID: "a", Name: "voice-lounge", Type: "Voice"},
		{ID: "b", Name: "general", Type: "Text"},
		{ID: "c", Name: "announcements", Type: "Announcement"},
		{ID: "d", Name: "Media", Type: "Category"},
		{ID: "e", Name: "bot-commands", Type: "Text"},
		{ID: "f", Name: "suggestions", Type: "Forum"},
		{ID: "g", Name: "other", Type: "Other"},
	}
	got := logChannelOptions(in)
	want := []entityOpt{
		{ID: "c", Name: "announcements", Type: "Announcement"},
		{ID: "e", Name: "bot-commands", Type: "Text"},
		{ID: "b", Name: "general", Type: "Text"},
	}
	if len(got) != len(want) {
		t.Fatalf("logChannelOptions kept %d channels, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("logChannelOptions[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestStaleLogChannelOption: a saved log channel that no longer exists (or is
// a type the picker no longer offers) must surface as a disabled option, so
// saving the other detection fields cannot silently wipe the value.
func TestStaleLogChannelOption(t *testing.T) {
	listed := []entityOpt{{ID: "1", Name: "general"}}
	for _, tc := range []struct {
		name   string
		id     string
		listed []entityOpt
		want   string // "" = nil (no option)
	}{
		{"empty saved value", "", listed, ""},
		{"still listed", "1", listed, ""},
		{"deleted channel", "99", listed, "99"},
		{"cache unavailable", "99", nil, "99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := staleLogChannelOption(tc.id, tc.listed)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("staleLogChannelOption(%q) = %+v, want nil", tc.id, got)
				}
				return
			}
			if got == nil || got.ID != tc.want {
				t.Fatalf("staleLogChannelOption(%q) = %+v, want option for %q", tc.id, got, tc.want)
			}
		})
	}
}

// TestImageFilterStaleChannelRenders: the stale log-channel option must
// actually reach the HTML (an absent option silently degrades the saved
// value to "none" on the next save — the template branch is what keeps it
// alive).
func TestImageFilterStaleChannelRenders(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	d := mkData(lvlOwner)
	d.Page = "imagefilter"
	d.GuildID = "1"
	d.GuildName = "G"
	d.ShowImageFilter = true
	d.Content = map[string]any{
		"admin":        true,
		"guild":        "1",
		"config":       map[string]string{"log_channel": "99"},
		"images":       []string{},
		"status":       modules.ImageFilterStatus{Variant: "b32"},
		"enabled":      false,
		"channels":     []entityOpt{},
		"staleChannel": &entityOpt{ID: "99", Name: "99"},
	}
	var sb strings.Builder
	if err := b.render(&sb, "rd_imagefilter", d); err != nil {
		t.Fatalf("render rd_imagefilter: %v", err)
	}
	if !strings.Contains(sb.String(), `<option value="99" disabled selected>#99 (deleted or unavailable)</option>`) {
		t.Error("stale log-channel option missing from rendered dropdown")
	}
}

// TestImageFilterProvisioningRenders: the model chip and the setup error must
// reach the HTML, so a lazy install (and its failure) is visible on the page.
func TestImageFilterProvisioningRenders(t *testing.T) {
	b, err := loadTemplates()
	if err != nil {
		t.Fatalf("loadTemplates: %v", err)
	}
	d := mkData(lvlOwner)
	d.Page = "imagefilter"
	d.GuildID = "1"
	d.GuildName = "G"
	d.ShowImageFilter = true
	d.Content = map[string]any{
		"admin":    true,
		"guild":    "1",
		"config":   map[string]string{},
		"images":   []string{},
		"status":   modules.ImageFilterStatus{Variant: "b32", Provisioning: true, ProvisionError: "network down"},
		"enabled":  true,
		"channels": []entityOpt{},
	}
	var sb strings.Builder
	if err := b.render(&sb, "rd_imagefilter", d); err != nil {
		t.Fatalf("render rd_imagefilter: %v", err)
	}
	out := sb.String()
	for _, want := range []string{"preparing model…", "Model setup failed: network down", `data-provisioning="true"`} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}
