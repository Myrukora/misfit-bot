package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// secrets.go — secrets live in <dir>/.env (mode 0600), NOT in config.yml:
// one plaintext secrets file the owner can lock down/back up separately, so
// config.yml stays shareable and can never leak them.
//
// Keys:
//
//	DISCORD_BOT_TOKEN           — the bot token (BotConfig.Token)
//	DISCORD_OAUTH_CLIENT_SECRET — the Discord app OAuth2 secret (OAuthConfig)
//	UPDATER_GITHUB_TOKEN        — the self-updater's GitHub PAT (UpdaterConfig)
//
// The corresponding config struct fields are yaml:"-", so Save never writes
// them. The MCP bearer token stays in config.yml for now.
const (
	EnvBotToken          = "DISCORD_BOT_TOKEN"
	EnvOAuthClientSecret = "DISCORD_OAUTH_CLIENT_SECRET"
	EnvUpdaterToken      = "UPDATER_GITHUB_TOKEN"
)

// EnvPath returns the secrets-file path for a bot directory.
func EnvPath(dir string) string { return filepath.Join(dir, ".env") }

// ReadEnvValue returns one key from <dir>/.env ("" when absent).
func ReadEnvValue(dir, key string) string {
	values, _ := readEnvFile(EnvPath(dir))
	return values[key]
}

// WriteEnvValue stores (or clears, when value is empty) one key in
// <dir>/.env at mode 0600, preserving every other line/comment/key.
func WriteEnvValue(dir, key, value string) error {
	value = strings.TrimSpace(value)
	path := EnvPath(dir)
	lines, err := readEnvLines(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	lines = setEnvLine(lines, key, value)
	if err := writeEnvLines(path, lines); err != nil {
		return err
	}
	// An existing file may have looser permissions; enforce 0600.
	return os.Chmod(path, 0o600)
}

// ReadEnvToken returns the bot token from <dir>/.env ("" when absent).
func ReadEnvToken(dir string) string { return ReadEnvValue(dir, EnvBotToken) }

// WriteEnvToken stores (or clears) the bot token in <dir>/.env.
func WriteEnvToken(dir, token string) error { return WriteEnvValue(dir, EnvBotToken, token) }

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
