package tickets

import (
	"fmt"
	"sort"
	"strings"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// admin.go — modules.TicketAdmin implementation. The dashboard is the ONLY
// management surface for ticket types, panels and per-guild settings; the
// in-channel commands are limited to end-user actions (close/rename/add/remove).
//
// Every mutation follows the same shape: validate the input, mutate the
// in-memory guild config under m.mu, persist, then best-effort sync the
// Discord panel message. A failed Discord call returns an error but never
// leaves the config unsaved — the dashboard can retry with "resend".

// adminReady validates the module/guild state every admin call needs.
func (m *TicketsModule) adminReady(guildID string) error {
	if !m.isLoaded() {
		return fmt.Errorf("tickets module is not loaded")
	}
	if !validGuildID(guildID) {
		return fmt.Errorf("invalid guildID")
	}
	return nil
}

// validChannelID reports whether s is a non-zero Discord snowflake.
func validChannelID(s string) bool {
	id, err := snowflake.Parse(strings.TrimSpace(s))
	return err == nil && id != 0
}

// normalizeRoleIDs trims, validates, dedupes and sorts a role ID list.
func normalizeRoleIDs(in []string) ([]string, error) {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		n, err := snowflake.Parse(id)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("invalid role ID %q", id)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ── type conversion ──────────────────────────────────────────────────────

// typeDetail converts the stored type to the dashboard shape.
func typeDetail(t *TypeConfig) modules.TypeDetail {
	return modules.TypeDetail{
		Key:            t.Key,
		Label:          t.Label,
		Enabled:        t.Enabled,
		Category:       t.Category,
		PingRoles:      append([]string{}, t.PingRoles...),
		HelperRoles:    append([]string{}, t.HelperRoles...),
		AccessRoles:    append([]string{}, t.AccessRoles...),
		ChannelName:    t.ChannelName,
		WelcomeMessage: t.WelcomeMsg,
		EmbedBody:      t.EmbedBody,
		ButtonLabel:    t.ButtonLabel,
		ButtonEmoji:    t.ButtonEmoji,
		Color:          int(t.Color),
		AllowClose:     t.AllowCloseOn(),
	}
}

// typeConfigFrom validates a dashboard type write and builds the stored shape.
// prev carries the immutable sequence (nil on create).
func typeConfigFrom(key string, in modules.TypeDetail, prev *TypeConfig) (TypeConfig, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if !validTypeKey(key) {
		return TypeConfig{}, fmt.Errorf("type keys use lowercase letters, digits, - and _ (1–64 chars)")
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		return TypeConfig{}, fmt.Errorf("label cannot be empty")
	}
	category := strings.TrimSpace(in.Category)
	if category != "" && !validChannelID(category) {
		return TypeConfig{}, fmt.Errorf("category must be a channel ID")
	}
	if in.Enabled && category == "" {
		return TypeConfig{}, fmt.Errorf("set a category before enabling the type")
	}
	ping, err := normalizeRoleIDs(in.PingRoles)
	if err != nil {
		return TypeConfig{}, fmt.Errorf("ping roles: %w", err)
	}
	helper, err := normalizeRoleIDs(in.HelperRoles)
	if err != nil {
		return TypeConfig{}, fmt.Errorf("helper roles: %w", err)
	}
	access, err := normalizeRoleIDs(in.AccessRoles)
	if err != nil {
		return TypeConfig{}, fmt.Errorf("access roles: %w", err)
	}
	channelName := strings.TrimSpace(in.ChannelName)
	if len([]rune(channelName)) > 100 {
		return TypeConfig{}, fmt.Errorf("channel name template must be at most 100 characters")
	}
	if in.Color < 0 || in.Color > 0xFFFFFF {
		return TypeConfig{}, fmt.Errorf("color must be between 0 and 16777215")
	}
	button := strings.TrimSpace(in.ButtonLabel)
	if len([]rune(button)) > 80 {
		return TypeConfig{}, fmt.Errorf("button label must be at most 80 characters")
	}
	emoji := strings.TrimSpace(in.ButtonEmoji)
	if !validEmoji(emoji) {
		return TypeConfig{}, fmt.Errorf("button emoji is not a unicode emoji or <:name:id>")
	}
	t := TypeConfig{
		Key:         key,
		Label:       label,
		Enabled:     in.Enabled,
		Category:    category,
		PingRoles:   ping,
		HelperRoles: helper,
		AccessRoles: access,
		ChannelName: channelName,
		WelcomeMsg:  in.WelcomeMessage,
		EmbedBody:   in.EmbedBody,
		ButtonLabel: button,
		ButtonEmoji: emoji,
		Color:       colorValue(in.Color),
		AllowClose:  boolPtr(in.AllowClose),
	}
	if prev != nil {
		t.Seq = prev.Seq
	}
	return t, nil
}

// nextTypeSeq returns the next creation order for a guild (caller holds no lock;
// the helper locks internally).
func (m *TicketsModule) nextTypeSeq(guildID string) int {
	cfg := m.guildConfig(guildID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	max := 0
	for _, t := range cfg.Types {
		if t != nil && t.Seq > max {
			max = t.Seq
		}
	}
	return max + 1
}

func (m *TicketsModule) ListTypeDetails(guildID string) ([]modules.TypeDetail, error) {
	if err := m.adminReady(guildID); err != nil {
		return nil, err
	}
	types := m.typesSnapshot(guildID)
	out := make([]modules.TypeDetail, 0, len(types))
	for i := range types {
		out = append(out, typeDetail(&types[i]))
	}
	return out, nil
}

func (m *TicketsModule) GetType(guildID, key string) (modules.TypeDetail, error) {
	if err := m.adminReady(guildID); err != nil {
		return modules.TypeDetail{}, err
	}
	t, ok := m.typeOf(guildID, strings.ToLower(strings.TrimSpace(key)))
	if !ok {
		return modules.TypeDetail{}, fmt.Errorf("unknown type %q", key)
	}
	return typeDetail(&t), nil
}

func (m *TicketsModule) CreateType(guildID string, in modules.TypeDetail) (modules.TypeDetail, error) {
	if err := m.adminReady(guildID); err != nil {
		return modules.TypeDetail{}, err
	}
	key := strings.ToLower(strings.TrimSpace(in.Key))
	cfg, err := typeConfigFrom(key, in, nil)
	if err != nil {
		return modules.TypeDetail{}, err
	}
	cfg.Seq = m.nextTypeSeq(guildID)

	g := m.guildConfig(guildID)
	m.mu.Lock()
	if _, exists := g.Types[key]; exists {
		m.mu.Unlock()
		return modules.TypeDetail{}, fmt.Errorf("type %q already exists", key)
	}
	g.Types[key] = &cfg
	err = m.saveGuildLocked(guildID)
	m.mu.Unlock()
	if err != nil {
		return modules.TypeDetail{}, err
	}
	return typeDetail(&cfg), nil
}

func (m *TicketsModule) UpdateType(guildID, key string, in modules.TypeDetail) (modules.TypeDetail, error) {
	if err := m.adminReady(guildID); err != nil {
		return modules.TypeDetail{}, err
	}
	key = strings.ToLower(strings.TrimSpace(key))
	g := m.guildConfig(guildID)
	m.mu.Lock()
	prev, ok := g.Types[key]
	if !ok {
		m.mu.Unlock()
		return modules.TypeDetail{}, fmt.Errorf("unknown type %q", key)
	}
	next, err := typeConfigFrom(key, in, prev)
	if err != nil {
		m.mu.Unlock()
		return modules.TypeDetail{}, err
	}
	g.Types[key] = &next
	saveErr := m.saveGuildLocked(guildID)
	m.mu.Unlock()
	if saveErr != nil {
		return modules.TypeDetail{}, saveErr
	}
	// Panels render their bound type's label/color/button — refresh them so an
	// edit is visible without a manual resend.
	m.refreshPanelsForType(guildID, key)
	return typeDetail(&next), nil
}

func (m *TicketsModule) DeleteType(guildID, key string) error {
	if err := m.adminReady(guildID); err != nil {
		return err
	}
	key = strings.ToLower(strings.TrimSpace(key))
	g := m.guildConfig(guildID)
	m.mu.Lock()
	if _, ok := g.Types[key]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown type %q", key)
	}
	delete(g.Types, key)
	removed := make([]PanelConfig, 0)
	for name, p := range g.Panels {
		if p.TypeKey == key {
			removed = append(removed, p)
			delete(g.Panels, name)
		}
	}
	saveErr := m.saveGuildLocked(guildID)
	m.mu.Unlock()
	if saveErr != nil {
		return saveErr
	}
	for _, p := range removed {
		if p.MessageID != "" {
			m.tryDeleteMessage(p.ChannelID, p.MessageID)
		}
	}
	return nil
}

// refreshPanelsForType reposts every panel bound to a type (best effort).
func (m *TicketsModule) refreshPanelsForType(guildID, typeKey string) {
	for _, p := range m.panelsSnapshot(guildID) {
		if p.TypeKey != typeKey {
			continue
		}
		panel := p
		if err := m.postOrUpdatePanel(guildID, &panel); err != nil {
			m.ctx.Logger.Warn("Tickets: refreshing panel %s after type edit failed: %v", p.Name, err)
		}
	}
}

// ── panels ───────────────────────────────────────────────────────────────

func panelSummary(p PanelConfig) modules.PanelSummary {
	qs := make([]modules.PanelQuestion, 0, len(p.Questions))
	for _, q := range p.Questions {
		qs = append(qs, modules.PanelQuestion{
			Label: q.Label, Placeholder: q.Placeholder, Style: q.Style,
			Required: q.Required, Value: q.Value,
		})
	}
	return modules.PanelSummary{
		Name: p.Name, TypeKey: p.TypeKey, ChannelID: p.ChannelID,
		Title: p.Title, Description: p.Description, ModalTitle: p.ModalTitle,
		Suspended: p.Suspended, Questions: qs,
	}
}

// panelConfigFrom validates a dashboard panel write.
func panelConfigFrom(in modules.PanelInput) (PanelConfig, error) {
	name := strings.TrimSpace(in.Name)
	if !validPanelName(name) {
		return PanelConfig{}, fmt.Errorf("panel names use letters, digits, - and _ (1–64 chars)")
	}
	ch := strings.TrimSpace(in.ChannelID)
	if !validChannelID(ch) {
		return PanelConfig{}, fmt.Errorf("channel must be a channel ID")
	}
	typeKey := strings.ToLower(strings.TrimSpace(in.TypeKey))
	if !validTypeKey(typeKey) {
		return PanelConfig{}, fmt.Errorf("type must be a valid type key")
	}
	title := strings.TrimSpace(in.Title)
	if len([]rune(title)) > 256 {
		return PanelConfig{}, fmt.Errorf("panel title must be at most 256 characters")
	}
	desc := strings.TrimSpace(in.Description)
	if len([]rune(desc)) > 4096 {
		return PanelConfig{}, fmt.Errorf("panel description must be at most 4096 characters")
	}
	modalTitle := strings.TrimSpace(in.ModalTitle)
	if len([]rune(modalTitle)) > 45 {
		return PanelConfig{}, fmt.Errorf("modal title must be at most 45 characters")
	}
	qs := make([]QuestionConfig, 0, len(in.Questions))
	for _, q := range in.Questions {
		qs = append(qs, QuestionConfig{
			Label: q.Label, Placeholder: q.Placeholder, Style: q.Style,
			Required: q.Required, Value: q.Value,
		})
	}
	if err := validateQuestions(qs); err != nil {
		return PanelConfig{}, err
	}
	return PanelConfig{
		Name: name, ChannelID: ch, TypeKey: typeKey,
		Title: title, Description: desc, ModalTitle: modalTitle,
		Questions: qs,
	}, nil
}

func (m *TicketsModule) GetPanel(guildID, name string) (modules.PanelSummary, error) {
	if err := m.adminReady(guildID); err != nil {
		return modules.PanelSummary{}, err
	}
	for _, p := range m.panelsSnapshot(guildID) {
		if p.Name == name {
			return panelSummary(p), nil
		}
	}
	return modules.PanelSummary{}, fmt.Errorf("unknown panel %q", name)
}

func (m *TicketsModule) CreatePanel(guildID string, in modules.PanelInput) (modules.PanelSummary, error) {
	if err := m.adminReady(guildID); err != nil {
		return modules.PanelSummary{}, err
	}
	p, err := panelConfigFrom(in)
	if err != nil {
		return modules.PanelSummary{}, err
	}
	if _, ok := m.typeOf(guildID, p.TypeKey); !ok {
		return modules.PanelSummary{}, fmt.Errorf("unknown type %q", p.TypeKey)
	}
	g := m.guildConfig(guildID)
	m.mu.Lock()
	if _, exists := g.Panels[p.Name]; exists {
		m.mu.Unlock()
		return modules.PanelSummary{}, fmt.Errorf("panel %q already exists", p.Name)
	}
	m.mu.Unlock()

	if err := m.postOrUpdatePanel(guildID, &p); err != nil {
		// No half-created panel in the registry when the post failed.
		m.mu.Lock()
		delete(g.Panels, p.Name)
		_ = m.saveGuildLocked(guildID)
		m.mu.Unlock()
		return modules.PanelSummary{}, err
	}
	return panelSummary(p), nil
}

func (m *TicketsModule) UpdatePanel(guildID, name string, in modules.PanelInput) (modules.PanelSummary, error) {
	if err := m.adminReady(guildID); err != nil {
		return modules.PanelSummary{}, err
	}
	in.Name = name // identity is the path; the body name is ignored
	next, err := panelConfigFrom(in)
	if err != nil {
		return modules.PanelSummary{}, err
	}
	if _, ok := m.typeOf(guildID, next.TypeKey); !ok {
		return modules.PanelSummary{}, fmt.Errorf("unknown type %q", next.TypeKey)
	}
	g := m.guildConfig(guildID)
	m.mu.Lock()
	prev, ok := g.Panels[name]
	if !ok {
		m.mu.Unlock()
		return modules.PanelSummary{}, fmt.Errorf("unknown panel %q", name)
	}
	next.MessageID = prev.MessageID
	next.Suspended = prev.Suspended
	if next.ChannelID != prev.ChannelID {
		next.MessageID = "" // moved: post in the new channel
	}
	g.Panels[name] = next
	saveErr := m.saveGuildLocked(guildID)
	m.mu.Unlock()
	if saveErr != nil {
		return modules.PanelSummary{}, saveErr
	}
	if err := m.postOrUpdatePanel(guildID, &next); err != nil {
		// Restore the previous registry entry (including its message ID) so a
		// failed sync cannot leave subsequent edits posting duplicates.
		m.mu.Lock()
		g.Panels[name] = prev
		_ = m.saveGuildLocked(guildID)
		m.mu.Unlock()
		return modules.PanelSummary{}, err
	}
	return panelSummary(next), nil
}

func (m *TicketsModule) DeletePanel(guildID, name string) error {
	if err := m.adminReady(guildID); err != nil {
		return err
	}
	g := m.guildConfig(guildID)
	m.mu.Lock()
	p, ok := g.Panels[name]
	if ok {
		delete(g.Panels, name)
	}
	saveErr := m.saveGuildLocked(guildID)
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown panel %q", name)
	}
	if saveErr != nil {
		return saveErr
	}
	if p.MessageID != "" {
		m.tryDeleteMessage(p.ChannelID, p.MessageID)
	}
	return nil
}

func (m *TicketsModule) PanelAction(guildID, name, action string) (modules.PanelSummary, error) {
	if err := m.adminReady(guildID); err != nil {
		return modules.PanelSummary{}, err
	}
	switch action {
	case "suspend", "resume":
		p, err := m.setPanelSuspended(guildID, name, action == "suspend")
		if err != nil {
			return modules.PanelSummary{}, err
		}
		return panelSummary(p), nil
	case "resend":
		g := m.guildConfig(guildID)
		m.mu.Lock()
		p, ok := g.Panels[name]
		if !ok {
			m.mu.Unlock()
			return modules.PanelSummary{}, fmt.Errorf("unknown panel %q", name)
		}
		p.MessageID = "" // force a fresh post
		g.Panels[name] = p
		saveErr := m.saveGuildLocked(guildID)
		m.mu.Unlock()
		if saveErr != nil {
			return modules.PanelSummary{}, saveErr
		}
		if err := m.postOrUpdatePanel(guildID, &p); err != nil {
			return modules.PanelSummary{}, err
		}
		return panelSummary(p), nil
	default:
		return modules.PanelSummary{}, fmt.Errorf("unsupported panel action %q", action)
	}
}

// ── per-guild settings ───────────────────────────────────────────────────

func (m *TicketsModule) GetGuildSettings(guildID string) (modules.TicketGuildSettings, error) {
	if err := m.adminReady(guildID); err != nil {
		return modules.TicketGuildSettings{}, err
	}
	g := m.guildConfig(guildID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	return modules.TicketGuildSettings{LogChannel: g.LogChannel}, nil
}

func (m *TicketsModule) SetGuildSettings(guildID string, in modules.TicketGuildSettings) error {
	if err := m.adminReady(guildID); err != nil {
		return err
	}
	logCh := strings.TrimSpace(in.LogChannel)
	if logCh != "" && !validChannelID(logCh) {
		return fmt.Errorf("log channel must be a channel ID (or empty)")
	}
	g := m.guildConfig(guildID)
	m.mu.Lock()
	g.LogChannel = logCh
	err := m.saveGuildLocked(guildID)
	m.mu.Unlock()
	return err
}

// tryDeleteMessage best-effort deletes the panel embed after removal.
func (m *TicketsModule) tryDeleteMessage(channelID, messageID string) {
	cid, e1 := snowflake.Parse(channelID)
	mid, e2 := snowflake.Parse(messageID)
	if e1 != nil || e2 != nil {
		return
	}
	if err := m.ctx.Rest.DeleteMessage(cid, mid); err != nil {
		m.ctx.Logger.Warn("Tickets: panel embed delete failed: %v", err)
	}
}
