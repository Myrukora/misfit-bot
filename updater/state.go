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

	// LatestVersion is the newest release tag seen on the tracked branch, and
	// Announced the update targets already posted to the notify channel. Together
	// they make the version announcement edge-triggered: one embed per new
	// release, not one every check_interval.
	LatestVersion string   `json:"latest_version,omitempty"`
	Announced     []string `json:"announced_releases,omitempty"`
}

// clone returns a deep copy. Callers that mutate the state outside Manager.mu
// (the notification pass holds no lock across its Discord sends) work on a
// copy and publish it with commit, so saveState can never marshal a struct
// half-written by another goroutine.
func (s *state) clone() *state {
	out := *s
	out.SeenPRs = make(map[int]bool, len(s.SeenPRs))
	for n, v := range s.SeenPRs {
		out.SeenPRs[n] = v
	}
	if s.Announced != nil {
		out.Announced = append([]string{}, s.Announced...)
	}
	return &out
}

func (m *Manager) statePath() string {
	return filepath.Join(m.Dir, "updater_state.json")
}

// loadState returns the shared state, loading it from disk on first use. A
// missing or corrupt file yields an empty state (safe defaults).
//
// The returned pointer is the manager's own: read it under Manager.mu (through
// Status/LatestVersion) and never mutate it outside updateState/editState —
// saveState marshals the very same struct.
func (m *Manager) loadState() *state {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadStateLocked()
}

// loadStateLocked is loadState for callers that already hold Manager.mu.
func (m *Manager) loadStateLocked() *state {
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

// editNotifications hands out a private copy of the notification bookkeeping —
// the fields checkNotifications owns — plus a commit function that publishes
// exactly those fields and persists, under Manager.mu. The short version for a
// plain compare-set-persist is updateState.
//
// Publishing only those three fields matters: Check and announceUpdate write
// LatestVersion and Announced, and the notification pass runs its GitHub and
// Discord calls with no lock held. Committing a whole stale struct would roll
// back a version another goroutine recorded in the meantime.
func (m *Manager) editNotifications() (*state, func() error) {
	m.mu.Lock()
	st := m.loadStateLocked().clone()
	m.mu.Unlock()
	return st, func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		cur := m.loadStateLocked()
		cur.LastCommitSHA, cur.Seeded, cur.SeenPRs = st.LastCommitSHA, st.Seeded, st.SeenPRs
		return m.saveStateLocked()
	}
}

// stateSnapshot returns a private copy of the current state, safe to read
// without holding Manager.mu. Readers that only report values (Status, the
// dashboard) use it instead of touching the shared struct the poll loop writes.
func (m *Manager) stateSnapshot() *state {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadStateLocked().clone()
}

// updateState applies fn to the state and persists the result with one lock
// acquisition, so a compare-and-set cannot interleave with another writer.
func (m *Manager) updateState(fn func(st *state)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.loadStateLocked())
	if err := m.saveStateLocked(); err != nil {
		m.Logger.Warn("Updater: failed to save state: %v", err)
	}
}

// saveState persists the current state atomically (temp file + rename). It
// takes the lock; callers already holding it use saveStateLocked.
func (m *Manager) saveState() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveStateLocked()
}

func (m *Manager) saveStateLocked() error {
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
