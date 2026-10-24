package leveling

import (
	"sort"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// maxLiveBuffer bounds the per-guild buffer of messages that arrive while
// catch-up is still scanning. Oldest entries are dropped first.
const maxLiveBuffer = 1000

func (m *LevelingModule) onMessageCreate(e *events.GuildMessageCreate) {
	defer func() {
		if r := recover(); r != nil {
			m.logError("Leveling: panic in message handler: %v", r)
		}
	}()
	if e == nil || m.store == nil {
		return
	}
	if e.GuildID == 0 {
		return
	}
	msg := e.Message
	if msg.Type.System() || msg.Author.Bot || msg.WebhookID != nil || msg.ApplicationID != nil {
		return
	}
	if msg.Author.ID == 0 {
		return
	}

	guildID := e.GuildID.String()
	cfg, err := m.store.GetConfig(guildID)
	if err != nil {
		m.logError("Leveling: load config for guild %s: %v", guildID, err)
		return
	}
	if !cfg.Enabled {
		return
	}

	channelID := msg.ChannelID.String()
	userID := msg.Author.ID.String()
	roleIDs := m.resolveRoles(guildID, userID, msg.Member)
	ts := msg.CreatedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	// Catch-up is still choosing its window: buffer instead of touching the
	// checkpoint, or the replay would skip everything after this message.
	if m.isCatching(guildID) {
		m.bufferLive(bufferedMessage{
			GuildID:   guildID,
			ChannelID: channelID,
			MessageID: msg.ID,
			UserID:    userID,
			Timestamp: ts,
			RoleIDs:   roleIDs,
		})
		return
	}

	m.awardMessage(cfg, guildID, channelID, userID, roleIDs, msg.ID, ts, true)
}

// awardMessage applies the guild's filters to one message and records it. The
// channel checkpoint always advances for a non-bot message in an enabled
// guild, even when the award is blocked. It reports whether XP was awarded and
// whether the member crossed a level boundary.
func (m *LevelingModule) awardMessage(cfg modules.LevelingConfig, guildID, channelID, userID string, roleIDs []string, msgID snowflake.ID, ts time.Time, announce bool) (awarded, crossed bool) {
	var amount int64
	eligible := m.messageEligible(cfg, channelID, roleIDs)
	if eligible && cfg.MessageEnabled &&
		m.allowAward(guildID, userID, ts, time.Duration(cfg.MessageCooldownSeconds)*time.Second) {
		span := cfg.MessageXPMax - cfg.MessageXPMin
		if span < 0 {
			span = 0
		}
		amount = int64(m.randInt(span+1) + cfg.MessageXPMin)
	}

	prevXP, cur, paid, err := m.store.AwardMessage(guildID, channelID, userID, msgID, ts, amount)
	if err != nil {
		m.logError("Leveling: award message for guild %s: %v", guildID, err)
		return false, false
	}
	if !paid || cur.XP <= prevXP {
		return false, false
	}
	oldLevel, _, _ := levelForXP(prevXP, cfg.MaxLevel)
	newLevel, _, _ := levelForXP(cur.XP, cfg.MaxLevel)
	if newLevel <= oldLevel {
		return true, false
	}
	if announce {
		go m.handleLevelUps(guildID, userID, prevXP, cur.XP, cfg, true)
	}
	return true, true
}

// messageEligible reports whether the message counts for XP at all: the
// channel is not ignored and the author holds no ignored role.
func (m *LevelingModule) messageEligible(cfg modules.LevelingConfig, channelID string, roleIDs []string) bool {
	if containsID(cfg.IgnoredChannels, channelID) {
		return false
	}
	for _, roleID := range roleIDs {
		if containsID(cfg.IgnoredRoles, roleID) {
			return false
		}
	}
	return true
}

// allowAward enforces the per-member messaging cooldown, shared by the live
// and catch-up paths so a replay earns XP at the same rate as being online.
func (m *LevelingModule) allowAward(guildID, userID string, ts time.Time, cooldown time.Duration) bool {
	if cooldown <= 0 {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cooldown == nil {
		m.cooldown = map[string]map[string]time.Time{}
	}
	users := m.cooldown[guildID]
	if users == nil {
		users = make(map[string]time.Time)
		m.cooldown[guildID] = users
	}
	if last, ok := users[userID]; ok && !last.IsZero() && ts.Sub(last) < cooldown {
		return false
	}
	users[userID] = ts
	return true
}

func (m *LevelingModule) isCatching(guildID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.catching[guildID]
}

func (m *LevelingModule) setCatching(guildID string, v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.catching == nil {
		m.catching = map[string]bool{}
	}
	if v {
		m.catching[guildID] = true
		return
	}
	delete(m.catching, guildID)
}

func (m *LevelingModule) setCatchupOK(guildID string, v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.catchupOK == nil {
		m.catchupOK = map[string]bool{}
	}
	m.catchupOK[guildID] = v
}

func (m *LevelingModule) bufferLive(bm bufferedMessage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.liveBuf == nil {
		m.liveBuf = map[string][]bufferedMessage{}
	}
	buf := append(m.liveBuf[bm.GuildID], bm)
	if len(buf) > maxLiveBuffer {
		buf = buf[len(buf)-maxLiveBuffer:]
	}
	m.liveBuf[bm.GuildID] = buf
}

// drainBuffered replays messages that arrived during catch-up. It skips
// anything the checkpoint already covers, so no message is counted twice.
func (m *LevelingModule) drainBuffered(guildID string, cfg modules.LevelingConfig) bool {
	m.mu.Lock()
	buf := m.liveBuf[guildID]
	delete(m.liveBuf, guildID)
	m.mu.Unlock()
	if len(buf) == 0 || !cfg.Enabled {
		return false
	}
	sort.Slice(buf, func(i, j int) bool { return buf[i].MessageID < buf[j].MessageID })
	crossed := false
	for _, bm := range buf {
		cp, err := m.store.Checkpoint(guildID, bm.ChannelID)
		if err != nil {
			m.logError("Leveling: read checkpoint for channel %s: %v", bm.ChannelID, err)
			continue
		}
		if bm.MessageID <= cp.LastMessageID {
			continue
		}
		if _, crossedNow := m.awardMessage(cfg, guildID, bm.ChannelID, bm.UserID, bm.RoleIDs, bm.MessageID, bm.Timestamp, false); crossedNow {
			crossed = true
		}
	}
	return crossed
}

// resolveRoles returns the author's role IDs from the message member, falling
// back to the cache (gated mode without the members intent leaves it nil).
func (m *LevelingModule) resolveRoles(guildID, userID string, member *discord.Member) []string {
	if member != nil {
		return roleIDStrings(member.RoleIDs)
	}
	return m.memberRoles(guildID, userID)
}

// memberRoles resolves one member's roles, preferring the live cache.
func (m *LevelingModule) memberRoles(guildID, userID string) []string {
	if m.ctx == nil || m.ctx.Bot == nil || userID == "" {
		return nil
	}
	member := m.ctx.Bot.GetCachedMember(guildID, userID)
	if member == nil {
		return nil
	}
	return roleIDStrings(member.RoleIDs)
}

func roleIDStrings(roleIDs []snowflake.ID) []string {
	if len(roleIDs) == 0 {
		return nil
	}
	out := make([]string, 0, len(roleIDs))
	for _, id := range roleIDs {
		out = append(out, id.String())
	}
	return out
}
