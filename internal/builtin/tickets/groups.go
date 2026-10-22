package tickets

// groups.go — the template-rendering view of a ticket type plus the type/panel
// registry helpers. The dashboard-managed TypeConfig is the source of truth;
// GroupConfig only feeds the {group} / {ticket.group} template tokens.

// GroupConfig is the template view of a ticket type.
type GroupConfig struct {
	Key   string
	Label string
}

// ── Type registry helpers (on TicketsModule) ─────────────────────────────

// typeOf returns the current config for one type key in a guild.
func (m *TicketsModule) typeOf(guildID, key string) (TypeConfig, bool) {
	cfg := m.guildConfig(guildID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := cfg.Types[key]
	if !ok || t == nil {
		return TypeConfig{}, false
	}
	return *t, true
}

// typesSnapshot returns copies of all configured types for a guild, sorted by key.
func (m *TicketsModule) typesSnapshot(guildID string) []TypeConfig {
	cfg := m.guildConfig(guildID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]TypeConfig, 0, len(cfg.Types))
	for _, t := range cfg.Types {
		if t != nil {
			out = append(out, *t)
		}
	}
	for i := 1; i < len(out); i++ { // tiny n; insertion sort keeps deps minimal
		for j := i; j > 0 && out[j].Key < out[j-1].Key; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// panelsSnapshot returns copies of all registered panels for a guild, sorted by name.
func (m *TicketsModule) panelsSnapshot(guildID string) []PanelConfig {
	cfg := m.guildConfig(guildID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]PanelConfig, 0, len(cfg.Panels))
	for _, p := range cfg.Panels {
		out = append(out, p)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
