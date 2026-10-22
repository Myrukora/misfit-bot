package tickets

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

// testLogger is a no-op modules.Logger for tests. When warns is non-nil it
// records every Warn message (mutex-guarded: the close tail may run in a
// goroutine); the zero value stays a pure no-op.
type testLogger struct {
	warns *warnCapture
}

// warnCapture records formatted Warn messages for assertions.
type warnCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (c *warnCapture) record(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, fmt.Sprintf(format, args...))
}

// messages returns a copy of the recorded warnings.
func (c *warnCapture) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.msgs...)
}

func (testLogger) Debug(string, ...any) {}
func (testLogger) Info(string, ...any)  {}
func (l testLogger) Warn(format string, args ...any) {
	if l.warns != nil {
		l.warns.record(format, args...)
	}
}
func (testLogger) Error(string, ...any) {}

// TestModuleConfigRetentionRoundTrip pins the bot-wide retention semantics: an
// explicit 0 (keep forever) survives save→reload, an omitted field falls back
// to the 30-day default, and a null value also falls back to the default.
func TestModuleConfigRetentionRoundTrip(t *testing.T) {
	dir := t.TempDir()

	mod, err := loadModuleConfig(dir)
	if err != nil {
		t.Fatalf("loadModuleConfig fresh: %v", err)
	}
	if mod.RetentionDays() != defaultRetentionDays {
		t.Fatalf("fresh: want default %d, got %d", defaultRetentionDays, mod.RetentionDays())
	}

	mod.Retention = retentionDays{value: 0, set: true}
	if err := mod.save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := loadModuleConfig(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.RetentionDays() != 0 {
		t.Fatalf("explicit zero lost: got %d, want 0", reloaded.RetentionDays())
	}

	if err := os.WriteFile(moduleConfigPath(dir), []byte("version: 3\nstorage_retention_days: null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nullMod, err := loadModuleConfig(dir)
	if err != nil {
		t.Fatalf("load null: %v", err)
	}
	if nullMod.RetentionDays() != defaultRetentionDays {
		t.Fatalf("null: want default %d, got %d", defaultRetentionDays, nullMod.RetentionDays())
	}
}

// TestGuildConfigRoundTrip covers a per-guild file: types + panels (with
// questions) + log channel round-trip.
func TestGuildConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	guildID := "111222333444555666"
	cfg := loadGuildConfig(dir, guildID, nil)
	cfg.LogChannel = "999888777666555444"
	cfg.Types["staff"] = &TypeConfig{
		Key: "staff", Label: "Staff", Enabled: true,
		Category: "111222333444555666",
	}
	cfg.Panels["staff_panel"] = PanelConfig{
		Name: "staff_panel", ChannelID: "121212121212121212",
		TypeKey: "staff", Title: "Need help?",
		ModalTitle: "Tell us what's up",
		Questions: []QuestionConfig{
			{Label: "What do you need?", Style: "short", Required: true},
			{Label: "Details", Style: "paragraph"},
		},
	}
	if err := cfg.save(dir, guildID); err != nil {
		t.Fatal(err)
	}
	got := loadGuildConfig(dir, guildID, nil)
	if got.LogChannel != "999888777666555444" {
		t.Fatalf("log channel lost: %q", got.LogChannel)
	}
	tc := got.Types["staff"]
	if tc == nil || !tc.Enabled || tc.Category != "111222333444555666" {
		t.Fatalf("type round-trip broken: %+v", tc)
	}
	p := got.Panels["staff_panel"]
	if p.ModalTitle != "Tell us what's up" || len(p.Questions) != 2 {
		t.Fatalf("panel questions lost: %+v", p)
	}
	if p.Questions[0].Label != "What do you need?" || !p.Questions[0].Required || p.Questions[1].Style != "paragraph" {
		t.Fatalf("question fields lost: %+v", p.Questions)
	}
}
