package modules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// captureLogger records Info and Warn messages for assertions.
type captureLogger struct {
	mu    sync.Mutex
	infos []string
	warns []string
}

func (l *captureLogger) Debug(string, ...any) {}
func (l *captureLogger) Info(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, fmt.Sprintf(format, args...))
}
func (l *captureLogger) Warn(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(format, args...))
}
func (l *captureLogger) Error(string, ...any) {}

func (l *captureLogger) infoList() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.infos...)
}

func (l *captureLogger) warnList() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warns...)
}

func containsAny(list []string, substr string) bool {
	for _, s := range list {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// TestAdoptLegacyBuiltinData covers the adoption helper's cases:
// (a) fresh install no-op, (b) legacy-only → adopted, (c) both-present → no
// overwrite + warning, (d) idempotent second call, (e) a legacy dir containing
// a .so → the .so is dropped, not adopted as a live artifact.
func TestAdoptLegacyBuiltinData(t *testing.T) {
	t.Run("fresh install no-op", func(t *testing.T) {
		dir := t.TempDir()
		log := &captureLogger{}
		AdoptLegacyBuiltinData(dir, "tickets", log)
		if _, err := os.Stat(filepath.Join(dir, "tickets")); !os.IsNotExist(err) {
			t.Fatalf("fresh install should create no dir, got %v", err)
		}
		if len(log.infoList()) != 0 || len(log.warnList()) != 0 {
			t.Fatalf("fresh install should be silent, infos=%v warns=%v", log.infoList(), log.warnList())
		}
	})

	t.Run("legacy-only adopted", func(t *testing.T) {
		dir := t.TempDir()
		legacy := filepath.Join(dir, "Go", "tickets")
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "config.yml"), []byte("k: v\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		log := &captureLogger{}
		AdoptLegacyBuiltinData(dir, "tickets", log)
		if _, err := os.Stat(filepath.Join(dir, "tickets", "config.yml")); err != nil {
			t.Fatalf("adopted config missing: %v", err)
		}
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("legacy dir should be gone after adoption, got %v", err)
		}
		if !containsAny(log.infoList(), "adopted builtin tickets") {
			t.Fatalf("expected an adoption INFO, got %v", log.infoList())
		}
	})

	t.Run("both-present no overwrite + warning", func(t *testing.T) {
		dir := t.TempDir()
		legacy := filepath.Join(dir, "Go", "tickets")
		fresh := filepath.Join(dir, "tickets")
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(fresh, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "legacy.yml"), []byte("l\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fresh, "fresh.yml"), []byte("f\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		log := &captureLogger{}
		AdoptLegacyBuiltinData(dir, "tickets", log)
		if _, err := os.Stat(filepath.Join(legacy, "legacy.yml")); err != nil {
			t.Fatalf("legacy file should be untouched: %v", err)
		}
		if _, err := os.Stat(filepath.Join(fresh, "fresh.yml")); err != nil {
			t.Fatalf("fresh file should be untouched: %v", err)
		}
		if !containsAny(log.warnList(), "not merging") {
			t.Fatalf("expected a both-present WARN, got %v", log.warnList())
		}
	})

	t.Run("idempotent second call", func(t *testing.T) {
		dir := t.TempDir()
		legacy := filepath.Join(dir, "Go", "tickets")
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "config.yml"), []byte("k: v\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		log := &captureLogger{}
		AdoptLegacyBuiltinData(dir, "tickets", log)
		AdoptLegacyBuiltinData(dir, "tickets", log) // second run: legacy gone → no-op
		if got := len(log.infoList()); got != 1 {
			t.Fatalf("second call should be a no-op, got %d INFO lines: %v", got, log.infoList())
		}
	})

	t.Run("legacy .so dropped, data adopted", func(t *testing.T) {
		dir := t.TempDir()
		legacy := filepath.Join(dir, "Go", "tickets")
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "tickets.so"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "config.yml"), []byte("k: v\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		log := &captureLogger{}
		AdoptLegacyBuiltinData(dir, "tickets", log)
		if _, err := os.Stat(filepath.Join(dir, "tickets", "config.yml")); err != nil {
			t.Fatalf("adopted config missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "tickets", "tickets.so")); !os.IsNotExist(err) {
			t.Fatalf("legacy .so should be dropped, got %v", err)
		}
	})

	t.Run("legacy .so-only is a no-op", func(t *testing.T) {
		dir := t.TempDir()
		legacy := filepath.Join(dir, "Go", "tickets")
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "tickets.so"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		log := &captureLogger{}
		AdoptLegacyBuiltinData(dir, "tickets", log)
		if _, err := os.Stat(filepath.Join(dir, "tickets")); !os.IsNotExist(err) {
			t.Fatalf("a .so-only legacy dir should not be adopted, got %v", err)
		}
		if len(log.infoList()) != 0 {
			t.Fatalf(".so-only legacy should be silent, got %v", log.infoList())
		}
	})
}
