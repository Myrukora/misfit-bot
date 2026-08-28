package tickets

import (
	"strings"
	"testing"
)

func TestParseGroupsYAMLValid(t *testing.T) {
	in := `
- key: staff
  label: "Staff"
  enabled: true
  parent_channel: "123456789012345678"
  ping_roles: ["111", "222"]
  embed_template: |
    {user} opened a **{group}** ticket.
  color: "0x5865F2"
  allow_claim: true
  allow_close: true
`
	groups, err := parseGroupsYAML(in)
	if err != nil {
		t.Fatalf("parseGroupsYAML: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	g := groups[0]
	if g.Key != "staff" || g.Label != "Staff" || !g.Enabled || g.ParentChannel != "123456789012345678" {
		t.Fatalf("bad group parse: %+v", g)
	}
	if len(g.PingRoles) != 2 || !g.AllowClaimOn() || !g.AllowCloseOn() {
		t.Fatalf("bad group fields: %+v", g)
	}
	if g.EmbedTemplate == "" {
		t.Fatal("embed_template lost")
	}
}

func TestParseGroupsYAMLEmpty(t *testing.T) {
	groups, err := parseGroupsYAML("")
	if err != nil {
		t.Fatalf("empty config must not error: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("want 0 groups, got %d", len(groups))
	}
}

func TestParseGroupsYAMLDefaults(t *testing.T) {
	in := `
- key: apps
  label: "Applications"
  parent_channel: "123"
`
	gs, err := parseGroupsYAML(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	g := gs[0]
	// Defaults: disabled until configured, claims allowed, blurple color.
	if g.Enabled {
		t.Error("group should default to disabled")
	}
	if !g.AllowClaimOn() || !g.AllowCloseOn() {
		t.Error("claim/close should default to true")
	}
	if g.Color == 0 {
		t.Error("color should default to blurple, not black")
	}
}

func TestParseGroupsYAMLDuplicateKey(t *testing.T) {
	in := `
- key: staff
  label: "A"
  parent_channel: "1"
- key: staff
  label: "B"
  parent_channel: "2"
`
	_, err := parseGroupsYAML(in)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate-key error, got %v", err)
	}
}

func TestParseGroupsYAMLEnabledNeedsParent(t *testing.T) {
	in := `
- key: staff
  label: "Staff"
  enabled: true
`
	_, err := parseGroupsYAML(in)
	if err == nil || !strings.Contains(err.Error(), "parent_channel") {
		t.Fatalf("want parent_channel error, got %v", err)
	}
}

func TestParseGroupsYAMLEmptyKeyRejected(t *testing.T) {
	in := `
- label: "NoKey"
`
	if _, err := parseGroupsYAML(in); err == nil {
		t.Fatal("empty key must be rejected")
	}
}

func TestParseGroupsYAMLBadColorFallsBack(t *testing.T) {
	in := `
- key: staff
  label: "S"
  color: "not-a-color"
`
	gs, err := parseGroupsYAML(in)
	if err != nil {
		t.Fatalf("bad color must fall back, not error: %v", err)
	}
	if gs[0].Color == 0 {
		t.Fatal("fallback color must be non-zero")
	}
}

func TestParseGroupsYAMLInvalidYAML(t *testing.T) {
	_, err := parseGroupsYAML("{{{ not yaml ]]")
	if err == nil {
		t.Fatal("invalid YAML must error")
	}
}

// TestLoadConfigRetentionZeroExplicit pins "keep forever": an explicitly
// saved storage_retention_days: 0 must survive save→reload, while an OMITTED
// field falls back to the 30-day default.
func TestLoadConfigRetentionZeroExplicit(t *testing.T) {
	dir := t.TempDir()
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("loadConfig fresh: %v", err)
	}
	if cfg.RetentionDays() != defaultRetentionDays {
		t.Fatalf("fresh config: want default %d, got %d", defaultRetentionDays, cfg.RetentionDays())
	}
	// Owner sets 0 (keep forever) via the dashboard setter path.
	cfg.Retention = retentionDays{value: 0, set: true}
	if err := cfg.save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := loadConfig(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.RetentionDays() != 0 {
		t.Fatalf("explicit zero lost on reload: got %d, want 0 (keep forever)", reloaded.RetentionDays())
	}
}
