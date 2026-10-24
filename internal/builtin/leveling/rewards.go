package leveling

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/embed"
	"github.com/misfit/bot/modules"
)

const (
	rewardSyncBatch    = 500
	rewardSyncBudget   = 2 * time.Minute
	rewardSyncInterval = 100 * time.Millisecond
)

// handleLevelUps grants every reward unlocked by the new level and posts the
// level-up announcement. It never blocks the message dispatcher (callers run
// it in a goroutine) and reports every failure through LastError.
func (m *LevelingModule) handleLevelUps(guildID, userID string, prevXP, curXP int64, cfg modules.LevelingConfig, announce bool) {
	defer func() {
		if r := recover(); r != nil {
			m.logError("Leveling: panic in level-up handler: %v", r)
		}
	}()
	oldLevel, _, _ := levelForXP(prevXP, cfg.MaxLevel)
	newLevel, _, _ := levelForXP(curXP, cfg.MaxLevel)
	if newLevel <= oldLevel {
		return
	}
	entry, _, err := m.store.GetEntry(guildID, userID)
	if err != nil {
		m.logError("Leveling: read entry for %s: %v", userID, err)
		return
	}
	held := map[string]bool{}
	if m.ctx != nil && m.ctx.Bot != nil {
		if member := m.ctx.Bot.GetCachedMember(guildID, userID); member != nil {
			for _, roleID := range member.RoleIDs {
				held[roleID.String()] = true
			}
		}
	}
	_, granted, failed := m.grantRewards(guildID, userID, newLevel, held, entry.TrackedRoles, cfg)
	if granted > 0 && failed == 0 {
		m.setLastError(guildID, "")
	}
	if announce {
		m.announceLevelUp(guildID, userID, newLevel, cfg)
	}
}

// grantRewards adds reward roles unlocked at level that the member neither
// holds nor already has tracked. It returns the updated tracked list plus
// success/failure counts; rewards stack, so every unlocked entry is applied.
func (m *LevelingModule) grantRewards(guildID, userID string, level int, held map[string]bool, tracked []string, cfg modules.LevelingConfig) ([]string, int, int) {
	out := append([]string(nil), tracked...)
	if m.ctx == nil || m.ctx.Rest == nil {
		return out, 0, 0
	}
	guild, err := snowflake.Parse(guildID)
	if err != nil {
		return out, 0, 0
	}
	user, err := snowflake.Parse(userID)
	if err != nil {
		return out, 0, 0
	}
	granted, failed := 0, 0
	for _, reward := range cfg.Rewards {
		if reward.Level > level {
			break // rewards are sorted by level
		}
		if held[reward.RoleID] || containsID(out, reward.RoleID) {
			continue
		}
		roleID, err := snowflake.Parse(reward.RoleID)
		if err != nil {
			continue
		}
		if err := m.ctx.Rest.AddMemberRole(guild, user, roleID,
			rest.WithReason(fmt.Sprintf("Leveling reward: level %d", reward.Level))); err != nil {
			failed++
			m.setLastError(guildID, fmt.Sprintf("Grant reward role %s: %v", reward.RoleID, err))
			continue
		}
		held[reward.RoleID] = true
		out = append(out, reward.RoleID)
		granted++
		if err := m.store.SetTrackedRoles(guildID, userID, out); err != nil {
			m.logError("Leveling: persist tracked roles for %s: %v", userID, err)
		}
	}
	return out, granted, failed
}

// announceLevelUp posts the rendered level-up message. There is no fallback
// channel: an empty AnnounceChannel means silent.
func (m *LevelingModule) announceLevelUp(guildID, userID string, level int, cfg modules.LevelingConfig) {
	if !cfg.AnnounceEnabled || cfg.AnnounceChannel == "" {
		return
	}
	if m.ctx == nil || m.ctx.Rest == nil {
		return
	}
	channelID, err := snowflake.Parse(cfg.AnnounceChannel)
	if err != nil {
		return
	}
	guildName := ""
	if m.ctx.Bot != nil {
		if guild := m.ctx.Bot.GetCachedGuild(guildID); guild != nil {
			guildName = guild.Name
		}
	}
	text := renderAnnouncement(cfg.AnnounceMessage, userID, level, guildName)
	_, err = m.ctx.Rest.CreateMessage(channelID, discord.MessageCreate{
		Embeds: []discord.Embed{embed.Info("Level up!", text)},
		AllowedMentions: &discord.AllowedMentions{
			Parse: []discord.AllowedMentionType{discord.AllowedMentionTypeUsers},
		},
	})
	if err != nil {
		m.setLastError(guildID, fmt.Sprintf("Announce level %d: %v", level, err))
		m.logError("Leveling: announce level-up for guild %s: %v", guildID, err)
	}
}

// renderAnnouncement expands the supported placeholders in the template.
func renderAnnouncement(tmpl, userID string, level int, guildName string) string {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = defaultConfig().AnnounceMessage
	}
	if guildName == "" {
		guildName = "this server"
	}
	return strings.NewReplacer(
		"{user}", "<@"+userID+">",
		"{user.name}", userID,
		"{level}", strconv.Itoa(level),
		"{server}", guildName,
	).Replace(tmpl)
}

// ResetEntry deletes a member's XP and removes every reward role the module
// granted them.
func (m *LevelingModule) ResetEntry(guildID, userID string) error {
	if m.store == nil {
		return errNotLoaded
	}
	roles, err := m.store.RemoveEntry(guildID, userID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.cooldown, guildID)
	m.mu.Unlock()
	if len(roles) == 0 || m.ctx == nil || m.ctx.Rest == nil {
		return nil
	}
	guild, err := snowflake.Parse(guildID)
	if err != nil {
		return nil
	}
	user, err := snowflake.Parse(userID)
	if err != nil {
		return nil
	}
	for _, role := range roles {
		roleID, err := snowflake.Parse(role)
		if err != nil {
			continue
		}
		if err := m.ctx.Rest.RemoveMemberRole(guild, user, roleID, rest.WithReason("Leveling reset")); err != nil {
			m.setLastError(guildID, fmt.Sprintf("Remove reward role %s: %v", role, err))
			m.logError("Leveling: remove reward role %s in guild %s: %v", role, guildID, err)
		}
	}
	return nil
}

// StartRewardSync backfills reward roles for already-eligible members. It
// returns the candidate count and runs the (rate-limited) pass in the
// background; Stats exposes progress.
func (m *LevelingModule) StartRewardSync(guildID string) (int, error) {
	if m.store == nil {
		return 0, errNotLoaded
	}
	if m.ctx == nil || m.ctx.Rest == nil {
		return 0, errors.New("Discord REST client unavailable")
	}
	cfg, err := m.store.GetConfig(guildID)
	if err != nil {
		return 0, err
	}
	if len(cfg.Rewards) == 0 {
		return 0, errors.New("no reward roles configured")
	}
	// Candidate members are those who already reached the lowest reward level;
	// the XP threshold comes straight from the curve table.
	lowest := cfg.Rewards[0].Level
	if lowest < 0 {
		lowest = 0
	}
	minXP := levelFloor[lowest]
	entries, err := m.store.ListEntriesAtLeastXP(guildID, minXP)
	if err != nil {
		return 0, err
	}
	candidates := make([]entryWithID, 0, len(entries))
	for _, e := range entries {
		if level, _, _ := levelForXP(e.XP, cfg.MaxLevel); level >= lowest {
			candidates = append(candidates, e)
		}
	}

	m.mu.Lock()
	st := m.sync[guildID]
	if st == nil {
		st = &syncState{}
		m.sync[guildID] = st
	}
	if st.running {
		total := st.total
		m.mu.Unlock()
		return total, errors.New("reward sync already running")
	}
	if len(candidates) == 0 {
		st.total = 0
		st.processed = 0
		st.failed = 0
		st.lastErr = ""
		m.mu.Unlock()
		return 0, nil
	}
	if st.cursor > len(candidates) {
		st.cursor = 0
	}
	start := st.cursor
	st.running = true
	st.total = len(candidates)
	st.processed = start
	st.failed = 0
	st.lastErr = ""
	m.mu.Unlock()

	go m.runRewardSync(guildID, cfg, candidates, start)
	return len(candidates), nil
}

func (m *LevelingModule) runRewardSync(guildID string, cfg modules.LevelingConfig, candidates []entryWithID, start int) {
	defer func() {
		if r := recover(); r != nil {
			m.logError("Leveling: panic in reward sync: %v", r)
		}
	}()
	guild, err := snowflake.Parse(guildID)
	if err != nil {
		m.finishRewardSync(guildID, 0)
		return
	}
	deadline := time.Now().Add(rewardSyncBudget)
	index := start
	done := 0
	for index = start; index < len(candidates); index++ {
		if done >= rewardSyncBatch || time.Now().After(deadline) {
			break
		}
		done++
		e := candidates[index]
		user, err := snowflake.Parse(e.UserID)
		if err != nil {
			continue
		}
		member, err := m.ctx.Rest.GetMember(guild, user)
		if err != nil || member == nil {
			if isUnknownMember(err) {
				m.advanceRewardSync(guildID, index+1)
				continue
			}
			m.advanceRewardSyncFailed(guildID, index+1, fmt.Sprintf("Fetch member %s: %v", e.UserID, err))
			time.Sleep(rewardSyncInterval)
			continue
		}
		held := map[string]bool{}
		for _, roleID := range member.RoleIDs {
			held[roleID.String()] = true
		}
		level, _, _ := levelForXP(e.XP, cfg.MaxLevel)
		if _, _, failed := m.grantRewards(guildID, e.UserID, level, held, e.TrackedRoles, cfg); failed > 0 {
			m.setRewardSyncError(guildID, fmt.Sprintf("Member %s: %d role grant(s) failed", e.UserID, failed))
		}
		m.advanceRewardSync(guildID, index+1)
		time.Sleep(rewardSyncInterval)
	}
	m.finishRewardSync(guildID, index)
}

// finishRewardSync marks the pass done and resets the cursor after the last
// candidate so the next click starts from the top.
func (m *LevelingModule) finishRewardSync(guildID string, nextCursor int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.sync[guildID]
	if st == nil {
		return
	}
	st.running = false
	if nextCursor >= st.total {
		st.cursor = 0
		st.processed = st.total
		return
	}
	st.cursor = nextCursor
}

func (m *LevelingModule) advanceRewardSync(guildID string, processed int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.sync[guildID]; st != nil {
		st.processed = processed
	}
}

func (m *LevelingModule) advanceRewardSyncFailed(guildID string, processed int, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.sync[guildID]; st != nil {
		st.processed = processed
		st.failed++
		st.lastErr = msg
	}
}

func (m *LevelingModule) setRewardSyncError(guildID, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.sync[guildID]; st != nil {
		st.lastErr = msg
	}
}

// isUnknownMember reports a 404 (member left / unknown user), which the sync
// skips without counting it as a failure.
func isUnknownMember(err error) bool {
	var restErr *rest.Error
	if !errors.As(err, &restErr) {
		return false
	}
	return restErr.Response != nil && restErr.Response.StatusCode == http.StatusNotFound
}
