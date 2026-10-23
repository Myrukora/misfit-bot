package tickets

import (
	"fmt"
	"strconv"

	"github.com/misfit/bot/modules"
)

// webconfig.go — the tickets module's GLOBAL (bot-wide) settings: retention
// and the open-time modal master switch. Per-guild management (types, panels,
// log channel) is done on the dashboard's Tickets page through
// modules.TicketAdmin — the [p]tickets / /tickets configuration command tree
// was removed in favour of that surface. Closing is in-channel only.

func (m *TicketsModule) WebConfigSchema() []modules.ConfigField {
	return []modules.ConfigField{
		{Key: "storage_retention_days", Label: "Retention (days)", Help: "Days to keep closed tickets before pruning (0 = keep forever).", Type: modules.FieldTypeNumber, Scope: "global", Placeholder: "30", Min: new(0.0), Step: new(1.0)},
		{Key: "modals_enabled", Label: "Open-time question modals", Help: "Allow panels to ask questions via a modal when a ticket is opened.", Type: modules.FieldTypeToggle, Scope: "global"},
	}
}

func (m *TicketsModule) WebGetConfig(guildID string) (map[string]string, error) {
	if guildID != "" {
		return map[string]string{}, nil
	}
	if !m.isLoaded() {
		return nil, fmt.Errorf("tickets module is not loaded")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]string{
		"storage_retention_days": strconv.Itoa(m.module.RetentionDays()),
		"modals_enabled":         strconv.FormatBool(m.module.ModalsOn()),
	}, nil
}

func (m *TicketsModule) WebSetConfig(guildID, key, value string) error {
	if guildID != "" {
		return fmt.Errorf("tickets settings are global; guild %q is not supported", guildID)
	}
	// Same nil-deref hazard as the read path: m.module is nil until OnLoad and
	// after OnUnload, so a dashboard write in that window must error, not panic.
	// Checked BEFORE taking m.mu (isLoaded takes the read lock).
	if !m.isLoaded() {
		return fmt.Errorf("tickets module is not loaded")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch key {
	case "storage_retention_days":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return fmt.Errorf("storage_retention_days must be a non-negative integer")
		}
		m.module.Retention = retentionDays{value: n, set: true}
	case "modals_enabled":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("modals_enabled must be a boolean")
		}
		m.module.ModalsEnabled = new(b)
	default:
		return fmt.Errorf("unknown tickets setting %q", key)
	}
	return m.module.save(m.ctx.DataDir)
}
