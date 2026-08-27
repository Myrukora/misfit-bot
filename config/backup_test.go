package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupServiceCreateAndList(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("bot:\n  token: x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	b := NewBackupService(dir)

	created, err := b.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created == "" {
		t.Fatal("Create returned empty filename")
	}
	// File must exist.
	if _, err := os.Stat(filepath.Join(dir, created)); err != nil {
		t.Fatalf("backup file not written: %v", err)
	}

	list, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0] != created {
		t.Errorf("List = %v, want [%s]", list, created)
	}
}

func TestBackupServiceVerify(t *testing.T) {
	dir := t.TempDir()
	b := NewBackupService(dir)

	// Missing file.
	if _, err := b.Verify("config_backup_xxx.yml"); err == nil {
		t.Error("expected error for missing backup file")
	}

	// Valid backup.
	valid := filepath.Join(dir, "config_backup_ok.yml")
	if err := os.WriteFile(valid, []byte("bot:\n  token: x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if warn, err := b.Verify("config_backup_ok.yml"); err != nil {
		t.Fatalf("Verify valid: %v", err)
	} else if warn != "" {
		t.Errorf("expected empty warning, got %q", warn)
	}

	// Invalid YAML.
	bad := filepath.Join(dir, "config_backup_bad.yml")
	if err := os.WriteFile(bad, []byte("bot: [unclosed"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Verify("config_backup_bad.yml"); err == nil {
		t.Error("expected error for invalid YAML")
	}

	// Valid YAML but missing bot section.
	nobot := filepath.Join(dir, "config_backup_nobot.yml")
	if err := os.WriteFile(nobot, []byte("other:\n  key: val\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if warn, err := b.Verify("config_backup_nobot.yml"); err != nil {
		t.Fatalf("Verify no-bot: %v", err)
	} else if warn == "" {
		t.Error("expected warning for missing bot section")
	}
}

func TestBackupServiceRestoreRequiresConfirm(t *testing.T) {
	dir := t.TempDir()
	b := NewBackupService(dir)

	// Seed a backup + current config.
	src := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(src, []byte("bot:\n  token: NEW\n"), 0644); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "config_backup_old.yml")
	if err := os.WriteFile(backup, []byte("bot:\n  token: OLD\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Without confirm -> error, config untouched.
	if _, err := b.Restore("config_backup_old.yml", false); err == nil {
		t.Error("expected confirmation-required error")
	}
	got, _ := os.ReadFile(src)
	if string(got) != "bot:\n  token: NEW\n" {
		t.Errorf("config changed without confirm: %q", got)
	}

	// With confirm -> restores OLD, leaves a pre-restore backup.
	safe, err := b.Restore("config_backup_old.yml", true)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if safe == "" {
		t.Error("expected pre-restore backup filename")
	}
	got, _ = os.ReadFile(src)
	if string(got) != "bot:\n  token: OLD\n" {
		t.Errorf("config not restored: %q", got)
	}
	if _, err := os.Stat(safe); err != nil {
		t.Errorf("pre-restore backup missing: %v", err)
	}
}

func TestBackupServiceRestoreInvalid(t *testing.T) {
	dir := t.TempDir()
	b := NewBackupService(dir)
	bad := filepath.Join(dir, "config_backup_bad.yml")
	if err := os.WriteFile(bad, []byte("bot: [unclosed"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Restore("config_backup_bad.yml", true); err == nil {
		t.Error("expected error restoring invalid YAML")
	}
}

// TestBackupServiceTimestampOrdering verifies that Create uses the clock and
// that List sorts. The clock is deterministic via the now override.
func TestBackupServiceTimestampOrdering(t *testing.T) {
	dir := t.TempDir()
	cur := time.Now()
	b := &BackupService{configDir: dir, now: func() time.Time { return cur }}
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("bot:\n  token: x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := b.Create(); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		cur = cur.Add(time.Second)
	}
	list, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3 backups, got %d (%v)", len(list), list)
	}
	// Sorted ascending by name.
	for i := 1; i < len(list); i++ {
		if list[i-1] > list[i] {
			t.Errorf("List not sorted: %v", list)
		}
	}
}

// TestBackupServiceRejectsPathTraversal pins the pathFor hardening: absolute
// paths, separators, traversal segments and weird characters must be rejected
// before filepath.Join, so Verify/Restore can never touch files outside
// configDir.
func TestBackupServiceRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	svc := NewBackupService(dir)

	for _, bad := range []string{
		"../config.yml",
		"../../etc/passwd",
		"sub/dir/backup.yml",
		"/etc/passwd",
		"a\\b.yml",
		"..",
		"",
	} {
		if _, err := svc.pathFor(bad); err == nil {
			t.Errorf("pathFor(%q) accepted; want error", bad)
		}
	}
	// Benign names still resolve inside configDir.
	got, err := svc.pathFor("config_backup_20260101_000000")
	if err != nil {
		t.Fatalf("pathFor benign: %v", err)
	}
	want := filepath.Join(dir, "config_backup_20260101_000000.yml")
	if got != want {
		t.Errorf("pathFor = %q, want %q", got, want)
	}
}
