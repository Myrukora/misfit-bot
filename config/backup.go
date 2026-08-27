package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// BackupService encapsulates config backup operations: create, list, verify,
// and restore. It is shared by the [p]backup command and the dashboard
// Configuration tab so both reuse the exact same logic.
//
// Backups are written as config_backup_<timestamp>.yml in configDir and are
// validated as YAML (with a best-effort bot-section check) before restore.
type BackupService struct {
	configDir string
	// now returns the current time; overridable in tests.
	now func() time.Time
}

// NewBackupService builds a BackupService rooted at configDir.
func NewBackupService(configDir string) *BackupService {
	return &BackupService{configDir: configDir, now: time.Now}
}

const timestampFormat = "20060102_150405"

// Create writes a timestamped copy of config.yml into configDir and returns the
// backup filename. It returns an error if config.yml cannot be read.
func (b *BackupService) Create() (string, error) {
	src := filepath.Join(b.configDir, "config.yml")
	input, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("failed to read config: %w", err)
	}
	name := fmt.Sprintf("config_backup_%s.yml", b.now().Format(timestampFormat))
	dst := filepath.Join(b.configDir, name)
	if err := os.WriteFile(dst, input, 0644); err != nil {
		return "", fmt.Errorf("failed to write backup: %w", err)
	}
	return name, nil
}

// BackupFile lists all backup files in configDir, sorted by name.
func (b *BackupService) List() ([]string, error) {
	entries, err := os.ReadDir(b.configDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read config directory: %w", err)
	}
	var backups []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "config_backup_") && (strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")) {
			backups = append(backups, name)
		}
	}
	sort.Strings(backups)
	return backups, nil
}

// verify parses a backup file as YAML and checks for a bot section. It returns
// a non-nil error if the file is invalid YAML, and a warning string if the
// bot section is missing (restore may fail).
func verifyBackup(data []byte) (string, error) {
	var testMap map[string]interface{}
	if err := yaml.Unmarshal(data, &testMap); err != nil {
		return "", fmt.Errorf("invalid YAML: %w", err)
	}
	if testMap["bot"] == nil {
		return "missing bot section", nil
	}
	return "", nil
}

// pathFor resolves a backup filename (adding .yml if needed) to an absolute path.
func (b *BackupService) pathFor(name string) string {
	if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
		name += ".yml"
	}
	return filepath.Join(b.configDir, name)
}

// Verify checks that a backup file exists, parses as YAML, and contains a bot
// section. It returns a warning string for the missing-bot-section case.
func (b *BackupService) Verify(name string) (string, error) {
	path := b.pathFor(name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("backup file `%s` not found", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read backup file: %w", err)
	}
	return verifyBackup(data)
}

// Restore backs up the current config, verifies the target backup, then copies
// it over config.yml. It returns the pre-restore backup filename. Requires
// confirm=true; otherwise it returns a confirmation-required error.
func (b *BackupService) Restore(name string, confirm bool) (string, error) {
	if !confirm {
		return "", fmt.Errorf("confirmation required: restore `%s` with confirm=true", name)
	}
	path := b.pathFor(name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", fmt.Errorf("backup file `%s` not found", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read backup file: %w", err)
	}
	if _, err := verifyBackup(data); err != nil {
		return "", fmt.Errorf("invalid backup: %w", err)
	}
	// Back up the current config before overwriting.
	timestamp := b.now().Format(timestampFormat)
	safeBackup := filepath.Join(b.configDir, fmt.Sprintf("config_pre_restore_%s.yml", timestamp))
	if curConfig, readErr := os.ReadFile(filepath.Join(b.configDir, "config.yml")); readErr == nil {
		os.WriteFile(safeBackup, curConfig, 0644)
	}
	restoredPath := filepath.Join(b.configDir, "config.yml")
	if err := os.WriteFile(restoredPath, data, 0644); err != nil {
		return "", fmt.Errorf("failed to restore config: %w", err)
	}
	return safeBackup, nil
}
