package moderation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misfit/bot/modules"
)

func TestModerationConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := openConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testModConfig()
	cfg.Rules = []modules.ModerationRule{
		{Threshold: 3, Action: modules.ModerationActionBan},
		{Threshold: 1, Action: modules.ModerationActionTimeout, DurationMinutes: 60},
	}
	if err := st.Save("1", cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "1.yaml")); err != nil {
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
	if len(got.Rules) != 2 || got.Rules[0].Threshold != 1 || got.Rules[1].Threshold != 3 {
		t.Fatalf("rules not sorted on read: %+v", got.Rules)
	}
	if got.Rules[1].DurationMinutes != 0 {
		t.Fatalf("ban rule kept a duration: %+v", got.Rules[1])
	}
	if !got.WarnNotify || got.WarnExpiryDays != 30 {
		t.Fatalf("config mismatch: %+v", got)
	}
}

func TestModerationDefaultConfig(t *testing.T) {
	st, err := openConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st.Get("1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Fatal("new guilds default to disabled")
	}
	if !cfg.WarnNotify {
		t.Fatal("warn notifications should default on once configured")
	}
}

func TestModerationConfigValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*modules.ModerationConfig)
		want   string
	}{
		{"negative expiry", func(c *modules.ModerationConfig) { c.WarnExpiryDays = -1 }, "warn expiry"},
		{"huge expiry", func(c *modules.ModerationConfig) { c.WarnExpiryDays = maxWarnExpiryDays + 1 }, "warn expiry"},
		{"negative delete", func(c *modules.ModerationConfig) { c.BanDeleteSeconds = -1 }, "ban message deletion"},
		{"huge delete", func(c *modules.ModerationConfig) { c.BanDeleteSeconds = maxBanDeleteSeconds + 1 }, "ban message deletion"},
		{"threshold zero", func(c *modules.ModerationConfig) {
			c.Rules = []modules.ModerationRule{{Threshold: 0, Action: modules.ModerationActionKick}}
		}, "threshold must be"},
		{"threshold high", func(c *modules.ModerationConfig) {
			c.Rules = []modules.ModerationRule{{Threshold: maxWarnThreshold + 1, Action: modules.ModerationActionKick}}
		}, "threshold must be"},
		{"bad action", func(c *modules.ModerationConfig) {
			c.Rules = []modules.ModerationRule{{Threshold: 1, Action: "warn"}}
		}, "action must be"},
		{"timeout zero", func(c *modules.ModerationConfig) {
			c.Rules = []modules.ModerationRule{{Threshold: 1, Action: modules.ModerationActionTimeout}}
		}, "timeout duration"},
		{"timeout high", func(c *modules.ModerationConfig) {
			c.Rules = []modules.ModerationRule{{Threshold: 1, Action: modules.ModerationActionTimeout, DurationMinutes: maxTimeoutMinutes + 1}}
		}, "timeout duration"},
		{"too many rules", func(c *modules.ModerationConfig) {
			for i := 0; i <= maxRules; i++ {
				c.Rules = append(c.Rules, modules.ModerationRule{Threshold: i + 1, Action: modules.ModerationActionKick})
			}
		}, "at most"},
		{"duplicate threshold", func(c *modules.ModerationConfig) {
			c.Rules = []modules.ModerationRule{
				{Threshold: 2, Action: modules.ModerationActionKick},
				{Threshold: 2, Action: modules.ModerationActionBan},
			}
		}, "duplicate"},
	}
	for _, tc := range cases {
		cfg := modules.ModerationConfig{}
		tc.mutate(&cfg)
		_, err := normalizeAndValidate(cfg)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.name, err, tc.want)
		}
	}
	if err := validateConfig(testModConfig()); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func normalizeAndValidate(cfg modules.ModerationConfig) (modules.ModerationConfig, error) {
	norm, err := normalizeConfig(cfg)
	if err != nil {
		return norm, err
	}
	return norm, validateConfig(norm)
}

func TestModerationConfigRejectsInvalidGuildIDs(t *testing.T) {
	st, err := openConfigStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range []string{"", "0", "..", "abc", "01"} {
		if _, err := st.Get(gid); err == nil {
			t.Errorf("Get(%q) should fail", gid)
		}
		if err := st.Save(gid, testModConfig()); err == nil {
			t.Errorf("Save(%q) should fail", gid)
		}
	}
}

func TestModerationConfigCorruptFile(t *testing.T) {
	dir := t.TempDir()
	st, err := openConfigStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.yaml"), []byte("rules: [broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get("1"); err == nil {
		t.Fatal("corrupt config should be an error")
	}
}
