package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/misfit/bot/config"
	"github.com/misfit/bot/embed"
)

// TestAutoDeleteTiming locks in the simplified message-disappearance rule:
// the bot auto-deletes ONLY error-colored embeds — and it does so after 7s so
// the user can read them. Every other response (success, info, warning,
// usage/reference listings, status reports, plain text) STAYS on screen
// permanently. There is no per-command "preserve" list and no opt-in hook:
// the dispatcher decides purely from the first embed's color (red ⇒ error).
func TestAutoDeleteTiming(t *testing.T) {
	if errorAutoDeleteDelay != 7*time.Second {
		t.Errorf("errorAutoDeleteDelay = %v, want 7s", errorAutoDeleteDelay)
	}

	// Error embed (red) → detected as an error response → auto-deletes at 7s.
	errEmbed := embed.Error("❌ Error", "Command failed")
	if !isErrorResponse([]discord.Embed{errEmbed}) {
		t.Error("embed.Error() should be detected as an error response (red)")
	}

	// Non-error embeds → NOT errors → stay on screen (never auto-delete).
	for name, e := range map[string]discord.Embed{
		"success": embed.Success("✅", "ok"),
		"info":    embed.Info("ℹ️", "ok"),
		"warning": embed.Warning("⚠️", "ok"),
		"usage":   embed.New().WithTitle("📘 Usage").WithColor(embed.ColorInfo),
		"status":  embed.New().WithTitle("📊 Status").WithColor(embed.ColorDefault),
	} {
		if isErrorResponse([]discord.Embed{e}) {
			t.Errorf("%s embed flagged as error — only red (embed.Error) should be", name)
		}
	}

	// Empty response: must not be treated as an error (no color to read),
	// so it stays on screen too.
	if isErrorResponse(nil) {
		t.Error("nil embeds flagged as error")
	}
	if isErrorResponse([]discord.Embed{}) {
		t.Error("empty embed slice flagged as error")
	}
}

// TestMigrateFromPluginEra verifies the one-time startup sweep removes stale
// .so files but leaves the data folders (config.yml) intact.
func TestMigrateFromPluginEra(t *testing.T) {
	// Point Dir at a temp tree and set Cfg.Modules.Path.
	dir := t.TempDir()
	goDir := filepath.Join(dir, "modules", "Go")
	cleanupDir := filepath.Join(goDir, "cleanup")
	if err := os.MkdirAll(cleanupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(cleanupDir, "cleanup.so")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(cleanupDir, "config.yml")
	if err := os.WriteFile(data, []byte("k: v\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldDir, oldCfg := Dir, Cfg
	Dir, Cfg = dir, &config.Config{Modules: config.ModulesConfig{Path: "modules"}}
	defer func() { Dir, Cfg = oldDir, oldCfg }()

	migrateFromPluginEra()

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale .so should be removed")
	}
	if _, err := os.Stat(data); err != nil {
		t.Errorf("data folder should be preserved: %v", err)
	}
}
