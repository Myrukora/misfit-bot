package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoggerWritesConfiguredPath pins logging.file_path end-to-end: the
// rotating writer must use the configured directory and basename, not the
// historical hardcoded logs/bot.log.
func TestLoggerWritesConfiguredPath(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir, "custom/access.log", "debug", true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	l.Info("hello from custom path")
	l.Close()

	matches, err := filepath.Glob(filepath.Join(dir, "custom", "access-*.log"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one custom/access-*.log, got %v (err %v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "hello from custom path") {
		t.Fatalf("log does not contain the entry: %s", data)
	}
}
