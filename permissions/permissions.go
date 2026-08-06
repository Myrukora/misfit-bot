package permissions

import (
	"sync"

	"github.com/disgoorg/disgo/discord"
)

type Manager struct {
	ownerID     string
	elevatedIDs map[string]bool
	mu          sync.RWMutex
	onSave      func([]string) // callback to persist changes
}

func NewManager(ownerID string, onSave func([]string)) *Manager {
	return &Manager{
		ownerID:     ownerID,
		elevatedIDs: make(map[string]bool),
		onSave:      onSave,
	}
}

func (m *Manager) LoadElevated(ids []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		m.elevatedIDs[id] = true
	}
}

func (m *Manager) IsOwner(userID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return userID == m.ownerID
}

func (m *Manager) IsElevated(userID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.elevatedIDs[userID]
}

func (m *Manager) AddElevated(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.elevatedIDs[userID] = true
	if m.onSave != nil {
		m.onSave(m.getElevatedIDsLocked())
	}
}

func (m *Manager) RemoveElevated(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.elevatedIDs, userID)
	if m.onSave != nil {
		m.onSave(m.getElevatedIDsLocked())
	}
}

func (m *Manager) getElevatedIDsLocked() []string {
	ids := make([]string, 0, len(m.elevatedIDs))
	for id := range m.elevatedIDs {
		ids = append(ids, id)
	}
	return ids
}

func (m *Manager) GetElevated() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.elevatedIDs))
	for id := range m.elevatedIDs {
		ids = append(ids, id)
	}
	return ids
}

func (m *Manager) SetOwner(userID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ownerID = userID
}

func (m *Manager) GetOwner() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ownerID
}

func (m *Manager) CanUse(userID string, userPerms discord.Permissions, requiredPerm discord.Permissions, ownerOnly bool, guildOwnerID string) bool {
	if m.IsOwner(userID) || m.IsElevated(userID) {
		return true
	}
	if ownerOnly {
		return false
	}
	if guildOwnerID != "" && userID == guildOwnerID {
		return true
	}
	if requiredPerm == 0 {
		return true
	}
	return userPerms.Has(requiredPerm)
}
