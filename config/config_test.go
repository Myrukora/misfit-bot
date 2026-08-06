package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNormalizeListen confirms the listen-address coercion that lets owners paste
// a URL-shaped value ("http://127.0.0.1:9090/") wherever a bare host:port is
// expected. Without this, `[p]dashboard set listen http://127.0.0.1:9090/` would
// fail at bind with "too many colons in address".
func TestNormalizeListen(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:9090":                 "127.0.0.1:9090", // already bare
		":8080":                          ":8080",          // all interfaces
		"0.0.0.0:8080":                   "0.0.0.0:8080",
		"http://127.0.0.1:9090":          "127.0.0.1:9090",   // scheme stripped
		"https://127.0.0.1:9090/":        "127.0.0.1:9090",   // scheme + trailing slash
		"http://127.0.0.1:9090/callback": "127.0.0.1:9090",   // scheme + path
		" tcp://example.com:8080/health": "example.com:8080", // leading space + scheme + path
		"[::1]:8080":                     "[::1]:8080",       // IPv6 survives
		"  127.0.0.1:9090  ":             "127.0.0.1:9090",   // whitespace trimmed
		"":                               "",                 // empty stays empty (caller applies default)
	}
	for in, want := range cases {
		if got := NormalizeListen(in); got != want {
			t.Errorf("NormalizeListen(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDashboardKeysRoundTrip guards the dashboard's ability to pin its bind port
// from the main config.yml: Set must accept dashboard_listen / dashboard_public_url,
// persist them, and Load must read them back. This is how the dashboard module
// resolves an effective listen address from the core config (see
// DashboardModule.effectiveListen), e.g. when the default 127.0.0.1:8080 is
// already taken.
func TestDashboardKeysRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{FilePath: filepath.Join(dir, "config.yml")}
	if err := Save(cfg, dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := cfg.Set("dashboard_listen", "127.0.0.1:9090"); err != nil {
		t.Fatalf("set dashboard_listen: %v", err)
	}
	if err := cfg.Set("dashboard_public_url", "https://dashboard.example.com/"); err != nil {
		t.Fatalf("set dashboard_public_url: %v", err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Dashboard.Listen != "127.0.0.1:9090" {
		t.Errorf("dashboard_listen = %q, want 127.0.0.1:9090", got.Dashboard.Listen)
	}
	// public_url trailing slash must be trimmed (match the OAuth redirect URI construction)
	if got.Dashboard.PublicURL != "https://dashboard.example.com" {
		t.Errorf("dashboard_public_url = %q, want https://dashboard.example.com", got.Dashboard.PublicURL)
	}

	// A URL-shaped listen value must be normalized to host:port on Set, so a
	// later [p]dashboard set listen http://127.0.0.1:9090/ doesn't break the bind.
	if err := cfg.Set("dashboard_listen", "http://127.0.0.1:9090/"); err != nil {
		t.Fatalf("set url-shaped dashboard_listen: %v", err)
	}
	got2, err := Load(dir)
	if err != nil {
		t.Fatalf("reload after url-shaped listen: %v", err)
	}
	if got2.Dashboard.Listen != "127.0.0.1:9090" {
		t.Errorf("url-shaped dashboard_listen normalized to %q, want 127.0.0.1:9090", got2.Dashboard.Listen)
	}

	// The OAuth client secret lives in core config's `oauth:` section (shared by
	// the dashboard and any future OAuth-using module). Set must persist it and
	// Load must read it back, trimmed.
	if err := cfg.Set("oauth_client_secret", "  sekret-xyz  "); err != nil {
		t.Fatalf("set oauth_client_secret: %v", err)
	}
	got3, err := Load(dir)
	if err != nil {
		t.Fatalf("reload after oauth_client_secret: %v", err)
	}
	if got3.OAuth.ClientSecret != "sekret-xyz" {
		t.Errorf("oauth_client_secret = %q, want sekret-xyz (trimmed)", got3.OAuth.ClientSecret)
	}

	// Unknown keys still rejected (defensive).
	if err := cfg.Set("dashboard_bogus", "x"); err == nil {
		t.Fatal("expected error for unknown dashboard_* key")
	}
}

// TestLoadAppliesDefaults verifies that sections missing from config.yml get
// the DefaultConfig values — e.g. a fresh `updater:` block on an existing
// install must come up enabled with a 5-minute interval, not zero-valued
// (which would silently disable the self-updater).
func TestLoadAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("bot:\n    prefix: '!'\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Updater.Enabled || cfg.Updater.Branch != "main" || cfg.Updater.CheckInterval != 300 || !cfg.Updater.AutoPull {
		t.Errorf("updater defaults not applied: %+v", cfg.Updater)
	}
	if cfg.Bot.Prefix != "!" {
		t.Errorf("yaml overlay lost: prefix = %q", cfg.Bot.Prefix)
	}
}

// updater_* keys, persist them, and Load must read them back. The token lives
// only in config.yml (gitignored) — this test writes to a temp dir.
// TestUpdaterKeysRoundTrip guards the self-update config: Set must accept all
// updater_* keys, persist them, and Load must read them back. The token lives
// only in config.yml (gitignored) — this test writes to a temp dir.
func TestUpdaterKeysRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{FilePath: filepath.Join(dir, "config.yml")}

	cases := map[string]string{
		"updater_enabled":        "true",
		"updater_repo":           "Myrukora/misfit-bot",
		"updater_branch":         "main",
		"updater_token":          "ghp_<redacted-for-test>",
		"updater_interval":       "120",
		"updater_auto_pull":      "yes",
		"updater_notify_channel": "123456789012345678",
	}
	for k, v := range cases {
		if err := cfg.Set(k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.Updater.Enabled {
		t.Error("updater_enabled not persisted as true")
	}
	if got.Updater.Repo != "Myrukora/misfit-bot" {
		t.Errorf("updater_repo = %q", got.Updater.Repo)
	}
	if got.Updater.Branch != "main" {
		t.Errorf("updater_branch = %q", got.Updater.Branch)
	}
	if got.Updater.Token != "ghp_<redacted-for-test>" {
		t.Errorf("updater_token not persisted")
	}
	if got.Updater.CheckInterval != 120 {
		t.Errorf("updater_interval = %d", got.Updater.CheckInterval)
	}
	if !got.Updater.AutoPull {
		t.Error("updater_auto_pull not persisted as true")
	}
	if got.Updater.NotifyChannel != "123456789012345678" {
		t.Errorf("updater_notify_channel = %q", got.Updater.NotifyChannel)
	}
}

// TestUpdaterKeysValidation guards against bad values being persisted: empty
// repo, non owner/name repo, empty branch, too-small interval, and ambiguous
// booleans must all be rejected.
func TestUpdaterKeysValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{FilePath: filepath.Join(dir, "config.yml")}

	bad := []struct{ key, value string }{
		{"updater_repo", ""},
		{"updater_repo", "norepo"},
		{"updater_repo", "/leading-slash"},
		{"updater_branch", ""},
		{"updater_interval", "10"},
		{"updater_interval", "abc"},
		{"updater_enabled", "maybe"},
		{"updater_auto_pull", "sure"},
	}
	for _, c := range bad {
		if err := cfg.Set(c.key, c.value); err == nil {
			t.Errorf("Set(%s, %q) accepted; want error", c.key, c.value)
		}
	}

	// Good values still accepted after the rejects.
	if err := cfg.Set("updater_repo", "Myrukora/misfit-bot"); err != nil {
		t.Errorf("valid repo rejected: %v", err)
	}
	if err := cfg.Set("updater_interval", "30"); err != nil {
		t.Errorf("minimum interval rejected: %v", err)
	}
	if err := cfg.Set("updater_auto_pull", "off"); err != nil {
		t.Errorf("valid boolean rejected: %v", err)
	}
	if err := cfg.Set("updater_enabled", "0"); err != nil {
		t.Errorf("valid boolean rejected: %v", err)
	}
}

// TestChannelMentionNormalization covers the "entered #updates as a channel"
// bug: a <#id> mention must be stored as the bare numeric ID (channel names
// can't be resolved from config), and names must be rejected loudly instead of
// being stored and failing later with "invalid notify channel".
func TestChannelMentionNormalization(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{FilePath: filepath.Join(dir, "config.yml")}

	// Mention form → bare ID, for both channel config keys.
	if err := cfg.Set("updater_notify_channel", "<#1534790217545027745>"); err != nil {
		t.Fatalf("set mention: %v", err)
	}
	if cfg.Updater.NotifyChannel != "1534790217545027745" {
		t.Errorf("notify_channel = %q, want bare ID 1534790217545027745", cfg.Updater.NotifyChannel)
	}
	if err := cfg.Set("log_channel", "<#1534790217545027745>"); err != nil {
		t.Fatalf("set log_channel mention: %v", err)
	}
	if cfg.Logging.Channel != "1534790217545027745" {
		t.Errorf("log_channel = %q, want bare ID", cfg.Logging.Channel)
	}

	// Bare ID passes through unchanged.
	if err := cfg.Set("updater_notify_channel", "123456789012345678"); err != nil {
		t.Fatalf("set bare id: %v", err)
	}
	if cfg.Updater.NotifyChannel != "123456789012345678" {
		t.Errorf("bare id mangled: %q", cfg.Updater.NotifyChannel)
	}

	// Channel names must be rejected at Set time.
	for _, v := range []string{"updates", "#updates", "general"} {
		if err := cfg.Set("updater_notify_channel", v); err == nil {
			t.Errorf("channel name %q accepted; want error", v)
		}
	}
	// Empty clears the channel.
	if err := cfg.Set("updater_notify_channel", ""); err != nil {
		t.Fatalf("clear channel: %v", err)
	}
	if cfg.Updater.NotifyChannel != "" {
		t.Errorf("channel not cleared: %q", cfg.Updater.NotifyChannel)
	}
}

// TestLogEnabledValidation guards the [p]logs command against silent typos:
// anything that isn't a recognizable boolean must be rejected instead of
// silently disabling logging ("enable" used to coerce to false).
func TestLogEnabledValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{FilePath: filepath.Join(dir, "config.yml")}

	for _, v := range []string{"true", "1", "yes", "on", "TRUE", "Yes"} {
		if err := cfg.Set("log_enabled", v); err != nil {
			t.Errorf("log_enabled %q rejected: %v", v, err)
		}
		if !cfg.Logging.Enabled {
			t.Errorf("log_enabled %q did not enable logging", v)
		}
	}
	for _, v := range []string{"false", "0", "no", "off"} {
		if err := cfg.Set("log_enabled", v); err != nil {
			t.Errorf("log_enabled %q rejected: %v", v, err)
		}
		if cfg.Logging.Enabled {
			t.Errorf("log_enabled %q did not disable logging", v)
		}
	}
	// Ambiguous values must be rejected, not silently coerced.
	for _, v := range []string{"enable", "disable", "maybe", ""} {
		if err := cfg.Set("log_enabled", v); err == nil {
			t.Errorf("log_enabled %q accepted; want error", v)
		}
	}
}

// TestDashboardPublicURLValidation ensures a scheme-less public_url (which
// would produce a broken OAuth redirect URI like "example.com/callback") is
// rejected at Set time, while empty (unset) and http(s):// values pass.
func TestDashboardPublicURLValidation(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{FilePath: filepath.Join(dir, "config.yml")}

	for _, v := range []string{"https://dashboard.example.com", "http://127.0.0.1:8080/", ""} {
		if err := cfg.Set("dashboard_public_url", v); err != nil {
			t.Errorf("dashboard_public_url %q rejected: %v", v, err)
		}
	}
	for _, v := range []string{"example.com", "ftp://example.com", "//example.com"} {
		if err := cfg.Set("dashboard_public_url", v); err == nil {
			t.Errorf("dashboard_public_url %q accepted; want error", v)
		}
	}
}
