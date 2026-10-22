package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// secrets.go — the Discord bot token lives in <dir>/.env (mode 0600), NOT in
// config.yml. One plaintext secrets file the owner can lock down/back up
// separately; config.yml stays shareable and can never leak the token.
//
// Key: DISCORD_BOT_TOKEN. BotConfig.Token is yaml:"-" — Save never writes it.
//
// The other secrets (OAuth client secret, updater PAT, MCP bearer token) stay
// in config.yml for now; the bot token is the first one moved.

const envTokenKey = "DISCORD_BOT_TOKEN"

// EnvPath returns the secrets-file path for a bot directory.
func EnvPath(dir string) string { return filepath.Join(dir, ".env") }

// ReadEnvToken returns the bot token from <dir>/.env ("" when absent).
func ReadEnvToken(dir string) string {
	values, _ := readEnvFile(EnvPath(dir))
	return values[envTokenKey]
}

// WriteEnvToken stores (or clears, when token is empty) the bot token in
// <dir>/.env at mode 0600, preserving every other line/comment/key.
func WriteEnvToken(dir, token string) error {
	token = strings.TrimSpace(token)
	path := EnvPath(dir)
	lines, err := readEnvLines(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines = setEnvLine(lines, envTokenKey, token)
	if err := writeEnvLines(path, lines); err != nil {
		return err
	}
	// An existing file may have looser permissions; enforce 0600.
	return os.Chmod(path, 0o600)
}

// MigrateTokenToEnv moves a legacy plaintext bot.token from config.yml into
// <dir>/.env and rewrites config.yml without it. Returns migrated=true only
// when the file actually contained a token (idempotent on later starts).
//
// An existing .env token wins — a rotated secret must never be overwritten by
// a stale plaintext copy — but the plaintext copy is still stripped.
func MigrateTokenToEnv(cfg *Config, dir string) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var file struct {
		Bot struct {
			Token string `yaml:"token"`
		} `yaml:"bot"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return false, fmt.Errorf("parsing config.yml for the legacy bot token: %w", err)
	}
	legacy := strings.TrimSpace(file.Bot.Token)
	if legacy == "" {
		return false, nil
	}
	effective := legacy
	if envTok := ReadEnvToken(dir); envTok != "" {
		effective = envTok
	} else if err := WriteEnvToken(dir, legacy); err != nil {
		return false, err
	}
	cfg.Bot.Token = effective
	// Save serializes BotConfig.Token as "-" → the plaintext copy disappears.
	if err := Save(cfg, dir); err != nil {
		return false, fmt.Errorf("rewriting config.yml without the bot token: %w", err)
	}
	return true, nil
}

// readEnvFile parses KEY=VALUE lines. Comments/blanks are ignored; an optional
// "export " prefix and matching single/double quotes are stripped.
func readEnvFile(path string) (map[string]string, error) {
	lines, err := readEnvLines(path)
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(lines))
	for _, line := range lines {
		if k, v, ok := parseEnvLine(line); ok {
			values[k] = v
		}
	}
	return values, nil
}

// readEnvLines returns the file's lines (nil when the file does not exist).
func readEnvLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.Split(normalized, "\n"), nil
}

// parseEnvLine extracts one KEY=VALUE line.
func parseEnvLine(line string) (key, value string, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false
	}
	s = strings.TrimPrefix(s, "export ")
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(s[:i])
	value = strings.TrimSpace(s[i+1:])
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	return key, value, true
}

// setEnvLine replaces (or appends) the KEY=value line, dropping duplicates and
// any blank trailing line noise. An empty value removes the key.
func setEnvLine(lines []string, key, value string) []string {
	out := make([]string, 0, len(lines)+1)
	seen := false
	for _, l := range lines {
		if k, _, ok := parseEnvLine(l); ok && k == key {
			if value == "" || seen {
				continue // clear, or drop duplicate assignments
			}
			seen = true
			out = append(out, key+"="+value)
			continue
		}
		// Drop the trailing empty line strings.Split leaves behind; the
		// writer re-adds exactly one final newline.
		if strings.TrimSpace(l) == "" && len(out) > 0 {
			continue
		}
		out = append(out, l)
	}
	if value != "" && !seen {
		out = append(out, key+"="+value)
	}
	return out
}

// writeEnvLines writes the lines with a single trailing newline, creating the
// file 0600 and truncating an existing one.
func writeEnvLines(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create secrets directory: %w", err)
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}
