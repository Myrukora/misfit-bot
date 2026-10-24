package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// secretsInFile reads the three secret fields straight from the YAML file
// (all empty when the keys are absent). Other sections legitimately carry
// their own token keys (MCP).
func secretsInFile(t *testing.T, path string) (bot, oauth, updater string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var file struct {
		Bot struct {
			Token string `yaml:"token"`
		} `yaml:"bot"`
		OAuth struct {
			ClientSecret string `yaml:"client_secret"`
		} `yaml:"oauth"`
		Updater struct {
			Token string `yaml:"token"`
		} `yaml:"updater"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file.Bot.Token, file.OAuth.ClientSecret, file.Updater.Token
}

// TestSecretsEnvRoundTrip pins the secrets-file contract: every secret is
// written 0600, Load applies them, and Save never puts them into config.yml.
func TestSecretsEnvRoundTrip(t *testing.T) {
	dir := t.TempDir()
	values := map[string]string{
		EnvBotToken:          "bot-123",
		EnvOAuthClientSecret: "oauth-xyz",
		EnvUpdaterToken:      "ghp_test",
	}
	for key, value := range values {
		if err := WriteEnvValue(dir, key, value); err != nil {
			t.Fatalf("WriteEnvValue(%s): %v", key, err)
		}
	}
	info, err := os.Stat(EnvPath(dir))
	if err != nil {
		t.Fatalf("stat .env: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf(".env mode = %o, want 600", perm)
	}
	for key, want := range values {
		if got := ReadEnvValue(dir, key); got != want {
			t.Errorf("ReadEnvValue(%s) = %q, want %q", key, got, want)
		}
	}

	// A minimal config.yml + .env: Load must surface all three secrets.
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("bot:\n  name: 'Bot'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Bot.Token != "bot-123" || cfg.OAuth.ClientSecret != "oauth-xyz" || cfg.Updater.Token != "ghp_test" {
		t.Fatalf("Load secrets = (%q, %q, %q)", cfg.Bot.Token, cfg.OAuth.ClientSecret, cfg.Updater.Token)
	}

	// Save must never serialize any of them.
	if err := Save(cfg, dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"bot-123", "oauth-xyz", "ghp_test"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("config.yml leaked %q:\n%s", leak, raw)
		}
	}
	if b, o, u := secretsInFile(t, filepath.Join(dir, "config.yml")); b != "" || o != "" || u != "" {
		t.Errorf("config.yml secret fields = (%q, %q, %q), want empty", b, o, u)
	}
}

// TestEnvFilePreservesOtherKeys pins that writing a key keeps comments and
// other keys, replaces an existing value, drops duplicates; clearing removes
// the key entirely.
func TestEnvFilePreservesOtherKeys(t *testing.T) {
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

// TestSetSecretKeysWriteEnv pins the runtime path for all three secrets:
// Set writes .env, config.yml stays secret-free, and empty clears the key.
func TestSetSecretKeysWriteEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{FilePath: filepath.Join(dir, "config.yml")}
	if err := Save(cfg, dir); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		value string
		key   string
		inCfg func() string
	}{
		"token":               {"bot-xyz", EnvBotToken, func() string { return cfg.Bot.Token }},
		"oauth_client_secret": {"oauth-xyz", EnvOAuthClientSecret, func() string { return cfg.OAuth.ClientSecret }},
		"updater_token":       {"ghp-xyz", EnvUpdaterToken, func() string { return cfg.Updater.Token }},
	}
	for name, tc := range cases {
		if err := cfg.Set(name, "  "+tc.value+"  "); err != nil {
			t.Fatalf("Set(%s): %v", name, err)
		}
		if got := ReadEnvValue(dir, tc.key); got != tc.value {
			t.Errorf("%s: .env value = %q, want %q (trimmed)", name, got, tc.value)
		}
		if got := tc.inCfg(); got != tc.value {
			t.Errorf("%s: live value = %q, want %q", name, got, tc.value)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.yml"))
	for _, leak := range []string{"bot-xyz", "oauth-xyz", "ghp-xyz"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("config.yml leaked %q:\n%s", leak, raw)
		}
	}
	// Clearing removes the key.
	if err := cfg.Set("oauth_client_secret", ""); err != nil {
		t.Fatalf("clear oauth: %v", err)
	}
	if got := ReadEnvValue(dir, EnvOAuthClientSecret); got != "" {
		t.Errorf("oauth secret after clear = %q, want empty", got)
	}
}
