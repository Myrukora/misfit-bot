package updater

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// state is the updater's persisted knowledge, kept in updater_state.json
// (gitignored). It lets the bot survive restarts without re-notifying about
// already-seen PRs and commits.
type state struct {
	LastCommitSHA string       `json:"last_commit_sha"` // remote HEAD of the tracked branch, last time we looked
	SeenPRs       map[int]bool `json:"seen_prs"`        // PR numbers we have already notified about
	Seeded        bool         `json:"seeded"`          // first poll completed silently (records, posts nothing)
}

func (m *Manager) statePath() string {
	return filepath.Join(m.Dir, "updater_state.json")
}

// loadState returns the current state, loading it from disk on first use.
// A missing or corrupt file yields an empty state (safe defaults).
func (m *Manager) loadState() *state {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != nil {
		return m.state
	}
	st := &state{SeenPRs: map[int]bool{}}
	if data, err := os.ReadFile(m.statePath()); err == nil {
		if err := json.Unmarshal(data, st); err != nil {
			m.Logger.Warn("Updater: ignoring corrupt state file: %v", err)
		}
	}
	if st.SeenPRs == nil {
		st.SeenPRs = map[int]bool{}
	}
	m.state = st
	return st
}

// saveState persists the current state atomically (temp file + rename).
func (m *Manager) saveState() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == nil {
		return nil
	}
	data, err := json.MarshalIndent(m.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, m.statePath())
}
