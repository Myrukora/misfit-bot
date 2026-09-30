package tickets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misfit/bot/modules"
)

// TestValidGuildID pins the snowflake trust boundary. snowflake.Parse("null")
// returns (0, nil) by contract, so a bare error check accepted "null" and let
// it reach the filesystem paths.
func TestValidGuildID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"111222333444555666", true},
		{"1", true},
		{"", false},
		{"null", false},
		{"NULL", false},
		{"0", false},
		{"000", false},
		{"not-a-snowflake", false},
		{"-1", false},
		{"11122233344455566666666666666", false}, // > uint64 max
		{" 111222333444555666", false},
	}
	for _, tc := range cases {
		if got := validGuildID(tc.in); got != tc.want {
			t.Errorf("validGuildID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestWebGetConfigUnloaded is the regression guard for the missing loaded
// check: WebGetConfig dereferenced m.module without one, so a dashboard read
// of an unloaded module panicked (nil pointer) instead of returning an error —
// a panic in-process would take the whole bot down.
func TestWebGetConfigUnloaded(t *testing.T) {
	m := &TicketsModule{guilds: map[string]*Config{}}

	// Guild-scoped reads are a no-op for this global-only module.
	got, err := m.WebGetConfig("111222333444555666")
	if err != nil {
		t.Fatalf("guild-scoped WebGetConfig should be a no-op, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("guild-scoped WebGetConfig = %v, want empty", got)
	}

	// The global read must error, never panic, while unloaded.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("WebGetConfig panicked while unloaded: %v", r)
			}
		}()
		if _, err := m.WebGetConfig(""); err == nil {
			t.Fatal("WebGetConfig on an unloaded module should error")
		}
	}()
}

// TestWebSetConfigUnloaded covers the write path: WebSetConfig dereferenced
// m.module the same way WebGetConfig did, so a dashboard write against a
// module that was just unloaded panicked in-process (taking the bot down)
// instead of returning the "not loaded" error. The read guard alone left the
// same nil-deref reachable through the write.
func TestWebSetConfigUnloaded(t *testing.T) {
	dir := t.TempDir()
	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dir, Logger: testLogger{}},
		guilds: map[string]*Config{},
	}

	// Guild-scoped writes are rejected for this global-only module, but the
	// rejection must come from the scope check, not the loaded guard.
	if err := m.WebSetConfig("111222333444555666", "modals_enabled", "true"); err == nil {
		t.Fatal("guild-scoped WebSetConfig should be rejected")
	}

	// Every write branch must error, never panic, while unloaded.
	for _, key := range []string{"storage_retention_days", "modals_enabled", "allow_dashboard_close"} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("WebSetConfig(%q) panicked while unloaded: %v", key, r)
				}
			}()
			err := m.WebSetConfig("", key, "1")
			if err == nil {
				t.Fatalf("WebSetConfig(%q) on an unloaded module should error", key)
			}
			if !strings.Contains(err.Error(), "not loaded") {
				t.Fatalf("WebSetConfig(%q) error = %v, want a not-loaded error", key, err)
			}
		}()
	}

	// Nothing may have been persisted (the guard precedes any module write).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read data dir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("unloaded WebSetConfig wrote to the data dir: %v", names)
	}
}

func TestValidMediaFilename(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"dot", ".", false},
		{"dotdot", "..", false},
		{"slash", "a/b", false},
		{"backslash", `a\b`, false},
		{"dotdot-pct", "..%2f", false},
		{"dotdot-in", "a..b", false},
		{"valid", "file.png", true},
		{"valid-no-ext", "file", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validMediaFilename(tc.in); got != tc.want {
				t.Fatalf("validMediaFilename(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTicketFilePathTraversal(t *testing.T) {
	dataDir := t.TempDir()
	st, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	filesDir := filepath.Join(dataDir, "tickets", "111", "support-1", "files")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, "img.png"), []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := &TicketsModule{
		ctx:    &modules.Context{DataDir: dataDir, Logger: testLogger{}},
		store:  st,
		loaded: true,
	}

	// Happy path: returns the absolute path to the mirrored file.
	p, err := m.TicketFilePath("111", "support-1", "img.png")
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if p != filepath.Join(filesDir, "img.png") {
		t.Fatalf("path = %q, want %q", p, filepath.Join(filesDir, "img.png"))
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "png-bytes" {
		t.Fatalf("data = %q, want png-bytes", string(data))
	}

	// Traversal attempts must be rejected.
	bad := []string{"..", "a/b", `a\b`, "..%2f", ".", "", "nonexistent.png"}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := m.TicketFilePath("111", "support-1", name)
			if err == nil {
				t.Fatalf("TicketFilePath(%q) should fail", name)
			}
		})
	}

	// A directory (not a file) must be rejected.
	if err := os.MkdirAll(filepath.Join(filesDir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if _, err := m.TicketFilePath("111", "support-1", "subdir"); err == nil {
		t.Fatal("TicketFilePath(subdir) should fail (not a regular file)")
	}

	// Invalid guildID / ticketID.
	if _, err := m.TicketFilePath("not-a-snowflake", "support-1", "img.png"); err == nil {
		t.Fatal("invalid guildID should fail")
	}
	if _, err := m.TicketFilePath("111", "not-a-snowflake", "img.png"); err == nil {
		t.Fatal("invalid ticketID should fail")
	}
}
