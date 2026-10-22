package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// botTokenInFile reads bot.token straight from the YAML file (empty when the
// key is absent). Other sections legitimately carry their own token keys.
func botTokenInFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var file struct {
		Bot struct {
			Token string `yaml:"token"`
		} `yaml:"bot"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file.Bot.Token
}

// TestEnvTokenRoundTrip pins the secrets file contract: the token is written
// 0600, Load applies it, and Save/Set never put it back into config.yml.
func TestEnvTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := WriteEnvToken(dir, "tok-123"); err != nil {
		t.Fatalf("WriteEnvToken: %v", err)
	}
	info, err := os.Stat(EnvPath(dir))
	if err != nil {
		t.Fatalf("stat .env: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf(".env mode = %o, want 600", perm)
	}
	if got := ReadEnvToken(dir); got != "tok-123" {
		t.Errorf("ReadEnvToken = %q, want tok-123", got)
	}

	// A minimal config.yml + .env: Load must surface the token.
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("bot:\n  prefix: '!'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Bot.Token != "tok-123" {
		t.Errorf("Load token = %q, want tok-123", cfg.Bot.Token)
	}

	// Save must never serialize the token.
	if err := Save(cfg, dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tok-123") || botTokenInFile(t, filepath.Join(dir, "config.yml")) != "" {
		t.Errorf("config.yml leaked the token:\n%s", raw)
	}
}

// TestEnvTokenPreservesFile pins that writing the token keeps comments and
// other keys, replaces an existing value, and removes duplicates; clearing
// removes the key entirely.
func TestEnvTokenPreservesFile(t *testing.T) {
	dir := t.TempDir()
	path := EnvPath(dir)
	original := "# secrets\nexport OTHER=keep\nDISCORD_BOT_TOKEN=old\nDISCORD_BOT_TOKEN=dupe\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteEnvToken(dir, "new"); err != nil {
		t.Fatalf("WriteEnvToken: %v", err)
	}
	raw, _ := os.ReadFile(path)
	content := string(raw)
	if !strings.Contains(content, "# secrets") || !strings.Contains(content, "OTHER=keep") {
		t.Errorf("other lines lost:\n%s", content)
	}
	if strings.Count(content, "DISCORD_BOT_TOKEN=") != 1 || !strings.Contains(content, "DISCORD_BOT_TOKEN=new") {
		t.Errorf("token line = %q, want one DISCORD_BOT_TOKEN=new", content)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("existing file mode = %o, want 600", info.Mode().Perm())
	}

	// Clearing removes the key but keeps the rest.
	if err := WriteEnvToken(dir, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if strings.Contains(string(raw), "DISCORD_BOT_TOKEN") {
		t.Errorf("token key survived clear:\n%s", raw)
	}
	if !strings.Contains(string(raw), "OTHER=keep") {
		t.Errorf("clear dropped other keys:\n%s", raw)
	}
}

// TestSetTokenWritesEnv pins the runtime path: Set("token", …) writes .env
// and config.yml stays token-free; empty clears the secret.
func TestSetTokenWritesEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{FilePath: filepath.Join(dir, "config.yml")}
	if err := Save(cfg, dir); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("token", "  tok-xyz  "); err != nil {
		t.Fatalf("Set token: %v", err)
	}
	if got := ReadEnvToken(dir); got != "tok-xyz" {
		t.Errorf("ReadEnvToken = %q, want tok-xyz (trimmed)", got)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
	if strings.Contains(string(raw), "tok-xyz") || botTokenInFile(t, filepath.Join(dir, "config.yml")) != "" {
		t.Errorf("config.yml leaked the token:\n%s", raw)
	}
	if err := cfg.Set("token", ""); err != nil {
		t.Fatalf("clear token: %v", err)
	}
	if got := ReadEnvToken(dir); got != "" {
		t.Errorf("token after clear = %q, want empty", got)
	}
}

// TestMigrateTokenToEnv pins the production upgrade path: a legacy plaintext
// bot.token is moved into .env, config.yml is rewritten without it, the live
// config keeps the token in memory, and a second run is a no-op.
func TestMigrateTokenToEnv(t *testing.T) {
	dir := t.TempDir()
	legacy := "bot:\n  token: legacy-tok\n  prefix: '!'\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Bot.Token != "" {
		t.Fatalf("Load read the legacy token from config.yml: %q", cfg.Bot.Token)
	}

	migrated, err := MigrateTokenToEnv(cfg, dir)
	if err != nil || !migrated {
		t.Fatalf("MigrateTokenToEnv = %v, %v; want true, nil", migrated, err)
	}
	if got := ReadEnvToken(dir); got != "legacy-tok" {
		t.Errorf(".env token = %q, want legacy-tok", got)
	}
	if cfg.Bot.Token != "legacy-tok" {
		t.Errorf("live config token = %q, want legacy-tok", cfg.Bot.Token)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
	if strings.Contains(string(raw), "legacy-tok") || botTokenInFile(t, filepath.Join(dir, "config.yml")) != "" {
		t.Errorf("config.yml still contains the token:\n%s", raw)
	}
	// Prefix survives the rewrite.
	if !strings.Contains(string(raw), "prefix: '!'") {
		t.Errorf("config.yml lost unrelated fields:\n%s", raw)
	}

	// Idempotent.
	migrated, err = MigrateTokenToEnv(cfg, dir)
	if err != nil || migrated {
		t.Fatalf("second migration = %v, %v; want false, nil", migrated, err)
	}
}

// TestMigrateEnvWins pins the rotation rule: an existing .env token is never
// overwritten by a stale plaintext copy, which is still deleted.
func TestMigrateEnvWins(t *testing.T) {
	dir := t.TempDir()
	if err := WriteEnvToken(dir, "env-tok"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("bot:\n  token: stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := MigrateTokenToEnv(cfg, dir)
	if err != nil || !migrated {
		t.Fatalf("migration = %v, %v; want true, nil", migrated, err)
	}
	if got := ReadEnvToken(dir); got != "env-tok" {
		t.Errorf(".env token = %q, want env-tok (existing secret wins)", got)
	}
	if cfg.Bot.Token != "env-tok" {
		t.Errorf("live token = %q, want env-tok", cfg.Bot.Token)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
	if strings.Contains(string(raw), "stale") {
		t.Errorf("plaintext token not stripped:\n%s", raw)
	}
}

// TestMigrateNoopWithoutToken pins the no-token path (fresh installs).
func TestMigrateNoopWithoutToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("bot:\n  prefix: '!'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load(dir)
	migrated, err := MigrateTokenToEnv(cfg, dir)
	if err != nil || migrated {
		t.Fatalf("migration = %v, %v; want false, nil", migrated, err)
	}
	if _, err := os.Stat(EnvPath(dir)); !os.IsNotExist(err) {
		t.Errorf(".env created without a token")
	}
}
