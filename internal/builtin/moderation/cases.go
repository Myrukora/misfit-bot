package moderation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/misfit/bot/modules"
)

// maxCasesPerGuild bounds disk/memory growth: the oldest entries are dropped
// once a guild exceeds it. Warnings are included, so extremely old warnings
// eventually fall out of the audit (documented in the README).
const maxCasesPerGuild = 20000

type caseFile struct {
	NextID int                      `json:"next_id"`
	Cases  []modules.ModerationCase `json:"cases"`
}

type caseStore struct {
	mu    sync.Mutex
	dir   string
	cache map[string]*caseFile
}

func openCaseStore(dir string) (*caseStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create moderation case dir: %w", err)
	}
	return &caseStore{dir: dir, cache: map[string]*caseFile{}}, nil
}

func (s *caseStore) path(guildID string) (string, error) {
	if !validGuildID(guildID) {
		return "", fmt.Errorf("invalid guild id")
	}
	return filepath.Join(s.dir, guildID+".json"), nil
}

// loadLocked requires s.mu. A missing file is an empty store.
func (s *caseStore) loadLocked(guildID string) (*caseFile, error) {
	if f, ok := s.cache[guildID]; ok {
		return f, nil
	}
	path, err := s.path(guildID)
	if err != nil {
		return nil, err
	}
	f := &caseFile{NextID: 1}
	raw, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(raw, f); err != nil {
			return nil, fmt.Errorf("corrupt moderation cases for guild %s: %w", guildID, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read moderation cases: %w", err)
	}
	if f.NextID < 1 {
		f.NextID = 1
	}
	s.cache[guildID] = f
	return f, nil
}

func (s *caseStore) saveLocked(guildID string, f *caseFile) error {
	path, err := s.path(guildID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode moderation cases: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write moderation cases: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("save moderation cases: %w", err)
	}
	return nil
}

// Add appends a case, assigning its per-guild ID and creation time. The cache
// is replaced only after the file write succeeds, so a failed save cannot leave
// an unpersisted case (or a consumed ID) in memory.
func (s *caseStore) Add(c modules.ModerationCase) (modules.ModerationCase, error) {
	if !validGuildID(c.GuildID) {
		return c, fmt.Errorf("invalid guild id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked(c.GuildID)
	if err != nil {
		return c, err
	}
	c.ID = f.NextID
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	next := caseFile{NextID: f.NextID + 1}
	next.Cases = make([]modules.ModerationCase, 0, len(f.Cases)+1)
	next.Cases = append(next.Cases, f.Cases...)
	next.Cases = append(next.Cases, c)
	if len(next.Cases) > maxCasesPerGuild {
		trimmed := make([]modules.ModerationCase, maxCasesPerGuild)
		copy(trimmed, next.Cases[len(next.Cases)-maxCasesPerGuild:])
		next.Cases = trimmed
	}
	if err := s.saveLocked(c.GuildID, &next); err != nil {
		return c, err
	}
	*f = next
	return c, nil
}

// List returns cases newest-first, filtered by type and target user.
func (s *caseStore) List(guildID string, filter modules.ModerationCaseFilter) ([]modules.ModerationCase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked(guildID)
	if err != nil {
		return nil, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	typ := strings.TrimSpace(filter.Type)
	userID := strings.TrimSpace(filter.UserID)
	out := make([]modules.ModerationCase, 0, limit)
	for i := len(f.Cases) - 1; i >= 0 && len(out) < limit; i-- {
		c := f.Cases[i]
		if typ != "" && c.Type != typ {
			continue
		}
		if userID != "" && c.TargetID != userID {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *caseStore) Get(guildID string, id int) (modules.ModerationCase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked(guildID)
	if err != nil {
		return modules.ModerationCase{}, err
	}
	for _, c := range f.Cases {
		if c.ID == id {
			return c, nil
		}
	}
	return modules.ModerationCase{}, fmt.Errorf("case #%d not found", id)
}

// RemoveWarning marks a warning case removed. Returns the updated case. The
// cache is replaced only after a successful write.
func (s *caseStore) RemoveWarning(guildID string, id int, byID, byTag, reason string) (modules.ModerationCase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked(guildID)
	if err != nil {
		return modules.ModerationCase{}, err
	}
	idx := -1
	var found modules.ModerationCase
	for i := range f.Cases {
		if f.Cases[i].ID == id {
			idx = i
			found = f.Cases[i]
			break
		}
	}
	if idx < 0 {
		return modules.ModerationCase{}, fmt.Errorf("case #%d not found", id)
	}
	if found.Type != modules.ModerationTypeWarn {
		return found, fmt.Errorf("case #%d is a %s, not a warning", id, found.Type)
	}
	if found.Removed {
		return found, fmt.Errorf("warning #%d was already removed", id)
	}
	now := time.Now().UTC()
	found.Removed = true
	found.RemovedAt = &now
	found.RemovedByID = byID
	found.RemovedByTag = byTag
	found.RemovedReason = strings.TrimSpace(reason)

	next := caseFile{NextID: f.NextID, Cases: make([]modules.ModerationCase, len(f.Cases))}
	copy(next.Cases, f.Cases)
	next.Cases[idx] = found
	if err := s.saveLocked(guildID, &next); err != nil {
		return found, err
	}
	*f = next
	return found, nil
}

// ActiveWarnings returns a user's warnings that are not removed or expired.
func (s *caseStore) ActiveWarnings(guildID, userID string, now time.Time) ([]modules.ModerationCase, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked(guildID)
	if err != nil {
		return nil, err
	}
	out := make([]modules.ModerationCase, 0, 4)
	for _, c := range f.Cases {
		if c.Type != modules.ModerationTypeWarn || c.TargetID != userID || c.Removed {
			continue
		}
		if c.ExpiresAt != nil && !c.ExpiresAt.After(now) {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *caseStore) CountActiveWarnings(guildID, userID string, now time.Time) (int, error) {
	warns, err := s.ActiveWarnings(guildID, userID, now)
	if err != nil {
		return 0, err
	}
	return len(warns), nil
}
