package reactionroles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misfit/bot/modules"
)

func storedPanel() modules.ReactionPanel {
	p := validPanel()
	p.ID = 1
	return p
}

func TestRRStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := openConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := modules.ReactionRolesConfig{NextID: 2, Panels: []modules.ReactionPanel{storedPanel()}}
	if err := st.Save("1", cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.yml")); err != nil {
		t.Fatalf("config file missing: %v", err)
	}
	st2, err := openConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st2.Get("1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Panels) != 1 || got.Panels[0].Name != "Pick roles" || got.Panels[0].Mappings[0].Emoji != "👍" {
		t.Fatalf("round-trip = %+v", got)
	}
	if got.NextID != 2 {
		t.Fatalf("next id = %d", got.NextID)
	}
}

func TestRRStoreDefaultsAndInvalidGuilds(t *testing.T) {
	st, err := openConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st.Get("1")
	if err != nil || cfg.NextID != 1 || len(cfg.Panels) != 0 {
		t.Fatalf("default config = %+v err %v", cfg, err)
	}
	for _, gid := range []string{"", "0", "..", "abc", "01"} {
		if _, err := st.Get(gid); err == nil {
			t.Errorf("Get(%q) should fail", gid)
		}
		if err := st.Save(gid, modules.ReactionRolesConfig{NextID: 1}); err == nil {
			t.Errorf("Save(%q) should fail", gid)
		}
	}
}

func TestRRStoreCorruptFile(t *testing.T) {
	dir := t.TempDir()
	st, err := openConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.yml"), []byte("panels: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get("1"); err == nil {
		t.Fatal("corrupt config should be an error")
	}
}

func TestRRNormalizePanel(t *testing.T) {
	p := storedPanel()
	p.Name = "  "
	p.Mappings = []modules.ReactionRoleMapping{{Emoji: "<:wave:123456789012345678>", RoleID: "20"}}
	norm, err := normalizePanel(p)
	if err != nil {
		t.Fatal(err)
	}
	if norm.Name != "Panel 1" {
		t.Errorf("default name = %q", norm.Name)
	}
	if norm.Mappings[0].Emoji != "wave:123456789012345678" {
		t.Errorf("emoji = %q", norm.Mappings[0].Emoji)
	}
}

func TestRRValidateRejectsBadPanels(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*modules.ReactionPanel)
		want   string
	}{
		{"missing role", func(p *modules.ReactionPanel) { p.Mappings[0].RoleID = "" }, "choose a role"},
		{"duplicate emoji", func(p *modules.ReactionPanel) {
			p.Mappings = append(p.Mappings, modules.ReactionRoleMapping{Emoji: "👍", RoleID: "20"})
		}, "duplicate emoji"},
		{"duplicate custom id", func(p *modules.ReactionPanel) {
			p.Mappings = []modules.ReactionRoleMapping{
				{Emoji: "wave:123456789012345678", RoleID: "20"},
				{Emoji: "renamed:123456789012345678", RoleID: "20"},
			}
		}, "duplicate emoji"},
		{"too many mappings", func(p *modules.ReactionPanel) {
			p.Mappings = make([]modules.ReactionRoleMapping, maxMappings+1)
			for i := range p.Mappings {
				p.Mappings[i] = modules.ReactionRoleMapping{Emoji: "👍", RoleID: "20"}
			}
		}, "at most"},
		{"long title", func(p *modules.ReactionPanel) { p.Embed.Title = strings.Repeat("t", maxTitleLen+1) }, "title is too long"},
		{"long description", func(p *modules.ReactionPanel) {
			p.Embed.Description = strings.Repeat("d", maxDescLen+1)
		}, "description is too long"},
		{"bad url", func(p *modules.ReactionPanel) { p.Embed.ImageURL = "ftp://example.com/x.png" }, "image url"},
		{"bad color", func(p *modules.ReactionPanel) { p.Embed.Color = "red" }, "color must be"},
	}
	for _, tc := range cases {
		p := storedPanel()
		tc.mutate(&p)
		_, err := normalizeConfig(modules.ReactionRolesConfig{NextID: 2, Panels: []modules.ReactionPanel{p}})
		if err == nil {
			err = validateConfig(modules.ReactionRolesConfig{NextID: 2, Panels: []modules.ReactionPanel{p}})
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
}

func TestRRNormalizeBumpsNextID(t *testing.T) {
	cfg := modules.ReactionRolesConfig{NextID: 1, Panels: []modules.ReactionPanel{storedPanel()}}
	norm, err := normalizeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if norm.NextID != 2 {
		t.Fatalf("next id = %d, want 2", norm.NextID)
	}
}

func TestRRTooManyPanels(t *testing.T) {
	cfg := modules.ReactionRolesConfig{NextID: 1}
	for i := 0; i < maxPanels+1; i++ {
		p := storedPanel()
		p.ID = i + 1
		cfg.Panels = append(cfg.Panels, p)
	}
	if _, err := normalizeConfig(cfg); err == nil {
		t.Fatal("too many panels should fail")
	}
}
