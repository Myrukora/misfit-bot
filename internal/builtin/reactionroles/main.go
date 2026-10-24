package reactionroles

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/commands"
	"github.com/misfit/bot/modules"
)

// ReactionRolesModule posts configurable role panels and grants/removes roles
// when members add/remove the mapped reactions. Panels are managed on the
// dashboard only; the module has no Discord commands.
type ReactionRolesModule struct {
	ctx      *modules.Context
	cfgStore *configStore

	mu      sync.Mutex
	lastErr map[string]string
}

func New() modules.Module { return &ReactionRolesModule{} }

func (m *ReactionRolesModule) Name() string    { return "reactionroles" }
func (m *ReactionRolesModule) Version() string { return "1.0.0" }
func (m *ReactionRolesModule) Description() string {
	return "Reaction role panels with Unicode and custom emojis"
}
func (m *ReactionRolesModule) Author() string { return "misfit" }

func (m *ReactionRolesModule) OnLoad(ctx *modules.Context) error {
	m.ctx = ctx
	m.lastErr = map[string]string{}
	store, err := openConfigStore(filepath.Join(ctx.DataDir, "guilds"))
	if err != nil {
		return err
	}
	m.cfgStore = store
	if ctx.Events != nil {
		ctx.Events.AddMessageReactionAdd(m.onReactionAdd)
		ctx.Events.AddMessageReactionRemove(m.onReactionRemove)
	}
	ctx.Logger.Info("Reaction roles module loaded")
	return nil
}

func (m *ReactionRolesModule) OnUnload() error { return nil }

func (m *ReactionRolesModule) OnDisable() error { return nil }

func (m *ReactionRolesModule) Dependencies() []string { return nil }

func (m *ReactionRolesModule) SlashCommands() []commands.SlashCommand { return nil }

// ── modules.ReactionRolesAdmin ───────────────────────────────────────────

var _ modules.ReactionRolesAdmin = (*ReactionRolesModule)(nil)

func (m *ReactionRolesModule) GetGuildConfig(guildID string) (modules.ReactionRolesConfig, error) {
	if m.cfgStore == nil {
		return modules.ReactionRolesConfig{}, fmt.Errorf("reaction roles module not loaded")
	}
	return m.cfgStore.Get(guildID)
}

func (m *ReactionRolesModule) CreatePanel(guildID string, panel modules.ReactionPanel) (modules.ReactionPanel, error) {
	if m.cfgStore == nil {
		return panel, fmt.Errorf("reaction roles module not loaded")
	}
	cfg, err := m.cfgStore.Get(guildID)
	if err != nil {
		return panel, err
	}
	if len(cfg.Panels) >= maxPanels {
		return panel, fmt.Errorf("at most %d panels are allowed", maxPanels)
	}
	panel.ID = cfg.NextID
	if panel.ID < 1 {
		panel.ID = 1
	}
	panel.MessageID = ""
	panel.PostedChannelID = ""
	panel.PostedEmojis = nil
	norm, err := normalizePanel(panel)
	if err != nil {
		return panel, err
	}
	cfg.NextID = panel.ID + 1
	cfg.Panels = append(cfg.Panels, norm)
	if err := m.cfgStore.Save(guildID, cfg); err != nil {
		return panel, err
	}
	return norm, nil
}

func (m *ReactionRolesModule) UpdatePanel(guildID string, panel modules.ReactionPanel) (modules.ReactionPanel, error) {
	if m.cfgStore == nil {
		return panel, fmt.Errorf("reaction roles module not loaded")
	}
	cfg, err := m.cfgStore.Get(guildID)
	if err != nil {
		return panel, err
	}
	idx := -1
	for i := range cfg.Panels {
		if cfg.Panels[i].ID == panel.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return panel, fmt.Errorf("panel %d not found", panel.ID)
	}
	// Posted state is module-owned; a dashboard save must not clear it.
	panel.MessageID = cfg.Panels[idx].MessageID
	panel.PostedChannelID = cfg.Panels[idx].PostedChannelID
	panel.PostedEmojis = cfg.Panels[idx].PostedEmojis
	norm, err := normalizePanel(panel)
	if err != nil {
		return panel, err
	}
	cfg.Panels[idx] = norm
	if err := m.cfgStore.Save(guildID, cfg); err != nil {
		return panel, err
	}
	return norm, nil
}

func (m *ReactionRolesModule) DeletePanel(guildID string, panelID int, deleteMessage bool) error {
	if m.cfgStore == nil {
		return fmt.Errorf("reaction roles module not loaded")
	}
	cfg, err := m.cfgStore.Get(guildID)
	if err != nil {
		return err
	}
	idx := -1
	for i := range cfg.Panels {
		if cfg.Panels[i].ID == panelID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("panel %d not found", panelID)
	}
	panel := cfg.Panels[idx]
	cfg.Panels = append(cfg.Panels[:idx], cfg.Panels[idx+1:]...)
	if err := m.cfgStore.Save(guildID, cfg); err != nil {
		return err
	}
	if deleteMessage && panel.MessageID != "" && panel.PostedChannelID != "" && m.ctx.Rest != nil {
		chID, chErr := snowflake.Parse(panel.PostedChannelID)
		msgID, msgErr := snowflake.Parse(panel.MessageID)
		if chErr == nil && msgErr == nil {
			if err := m.ctx.Rest.DeleteMessage(chID, msgID); err != nil {
				m.ctx.Logger.Debug("Reaction roles: panel %d message delete failed: %v", panelID, err)
			}
		}
	}
	return nil
}

func (m *ReactionRolesModule) LastError(guildID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr[guildID]
}

func (m *ReactionRolesModule) setLastError(guildID, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(msg) == "" {
		delete(m.lastErr, guildID)
		return
	}
	m.lastErr[guildID] = msg
}

// ── reaction handling ────────────────────────────────────────────────────

func (m *ReactionRolesModule) onReactionAdd(e *events.MessageReactionAdd) {
	m.handleReaction(e.GenericReaction, e.Member, true)
}

func (m *ReactionRolesModule) onReactionRemove(e *events.MessageReactionRemove) {
	m.handleReaction(e.GenericReaction, nil, false)
}

func (m *ReactionRolesModule) handleReaction(e *events.GenericReaction, member *discord.Member, adding bool) {
	if m.ctx == nil || m.cfgStore == nil || e == nil || e.GuildID == nil {
		return
	}
	guildID := *e.GuildID
	selfID, err := snowflake.Parse(m.ctx.Bot.GetSelfUserID())
	if err != nil || e.UserID == selfID {
		return
	}
	if member != nil && member.User.Bot {
		return
	}
	if member == nil {
		if cached := m.ctx.Bot.GetCachedMember(guildID.String(), e.UserID.String()); cached != nil && cached.User.Bot {
			return
		}
	}
	cfg, err := m.cfgStore.Get(guildID.String())
	if err != nil {
		m.ctx.Logger.Error("Reaction roles: %v", err)
		return
	}
	panel := panelByMessage(cfg, e.MessageID.String())
	if panel == nil || !panel.Enabled {
		return
	}
	mapping := mappingForEmoji(panel, e.Emoji)
	if mapping == nil {
		return
	}
	roleID, err := snowflake.Parse(mapping.RoleID)
	if err != nil || roleID == 0 {
		return
	}
	if adding {
		m.grantRole(guildID, e.UserID, roleID, mapping.Emoji)
	} else if panel.RemoveOnUnreact {
		m.removeRole(guildID, e.UserID, roleID, mapping.Emoji)
	}
}

func panelByMessage(cfg modules.ReactionRolesConfig, messageID string) *modules.ReactionPanel {
	if messageID == "" {
		return nil
	}
	for i := range cfg.Panels {
		if cfg.Panels[i].MessageID == messageID {
			return &cfg.Panels[i]
		}
	}
	return nil
}

func mappingForEmoji(panel *modules.ReactionPanel, emoji discord.PartialEmoji) *modules.ReactionRoleMapping {
	for i := range panel.Mappings {
		if emojiMatches(panel.Mappings[i].Emoji, emoji) {
			return &panel.Mappings[i]
		}
	}
	return nil
}

func (m *ReactionRolesModule) grantRole(guildID, userID, roleID snowflake.ID, emoji string) {
	if err := m.ensureAssignableRole(guildID, roleID); err != nil {
		m.setLastError(guildID.String(), err.Error())
		m.ctx.Logger.Error("Reaction roles: cannot grant role %s in guild %s: %v", roleID, guildID, err)
		return
	}
	if member := m.ctx.Bot.GetCachedMember(guildID.String(), userID.String()); member != nil && hasRole(member, roleID) {
		return
	}
	if m.ctx.Rest == nil {
		return
	}
	if err := m.ctx.Rest.AddMemberRole(guildID, userID, roleID, rest.WithReason("Reaction role: "+emoji)); err != nil {
		m.setLastError(guildID.String(), err.Error())
		m.ctx.Logger.Error("Reaction roles: add role %s to %s failed: %v", roleID, userID, err)
		return
	}
	m.setLastError(guildID.String(), "")
}

func (m *ReactionRolesModule) removeRole(guildID, userID, roleID snowflake.ID, emoji string) {
	if err := m.ensureAssignableRole(guildID, roleID); err != nil {
		m.setLastError(guildID.String(), err.Error())
		return
	}
	if member := m.ctx.Bot.GetCachedMember(guildID.String(), userID.String()); member != nil && !hasRole(member, roleID) {
		return
	}
	if m.ctx.Rest == nil {
		return
	}
	if err := m.ctx.Rest.RemoveMemberRole(guildID, userID, roleID, rest.WithReason("Reaction role removed: "+emoji)); err != nil {
		// Removing a role the member no longer has is a normal no-op.
		m.ctx.Logger.Debug("Reaction roles: remove role %s from %s: %v", roleID, userID, err)
		return
	}
	m.setLastError(guildID.String(), "")
}

func hasRole(member *discord.Member, roleID snowflake.ID) bool {
	for _, id := range member.RoleIDs {
		if id == roleID {
			return true
		}
	}
	return false
}

// ensureAssignableRole verifies the role still exists, is not managed, and sits
// below the bot's highest role (and that the bot keeps Manage Roles).
func (m *ReactionRolesModule) ensureAssignableRole(guildID, roleID snowflake.ID) error {
	if m.ctx.Rest == nil {
		return fmt.Errorf("Discord REST client unavailable")
	}
	role := m.ctx.Bot.GetCachedRole(guildID.String(), roleID.String())
	if role == nil {
		var err error
		role, err = m.ctx.Rest.GetRole(guildID, roleID)
		if err != nil || role == nil {
			return fmt.Errorf("role no longer exists")
		}
	}
	if role.ID == guildID {
		return fmt.Errorf("the @everyone role cannot be assigned")
	}
	if role.Managed {
		return fmt.Errorf("the role **%s** is managed by an integration and cannot be assigned", role.Name)
	}
	selfID, err := snowflake.Parse(m.ctx.Bot.GetSelfUserID())
	if err != nil {
		return fmt.Errorf("bot identity unavailable")
	}
	if !m.ctx.Bot.GetUserPermissions(selfID.String(), guildID.String()).Has(discord.PermissionManageRoles) {
		return fmt.Errorf("I am missing the **Manage Roles** permission")
	}
	botMember := m.ctx.Bot.GetCachedMember(guildID.String(), selfID.String())
	if botMember == nil {
		if fetched, err := m.ctx.Rest.GetMember(guildID, selfID); err == nil {
			botMember = fetched
		}
	}
	if botMember == nil {
		return fmt.Errorf("could not resolve my member record")
	}
	if role.Position >= m.memberTopPosition(guildID, botMember) {
		return fmt.Errorf("the role **%s** is above my highest role", role.Name)
	}
	return nil
}

func (m *ReactionRolesModule) memberTopPosition(guildID snowflake.ID, member *discord.Member) int {
	top := 0
	for _, roleID := range member.RoleIDs {
		role := m.ctx.Bot.GetCachedRole(guildID.String(), roleID.String())
		if role != nil && role.Position > top {
			top = role.Position
		}
	}
	return top
}
