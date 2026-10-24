package leveling

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

const (
	catchupPageSize    = 100
	catchupRequestGap  = 100 * time.Millisecond
	catchupGuildBudget = 10 * time.Minute
)

// catchupChannel is one channel (or active thread) to scan.
type catchupChannel struct {
	ID            snowflake.ID
	Name          string
	Position      int
	Thread        bool
	HasCheckpoint bool
}

type catchupResult struct {
	processed int64
	awarded   int64
	crossed   bool
	truncated bool
	hardError string
}

// runCatchupQueue replays missed messages for every queued guild, one guild
// at a time so a large server cannot starve the REST client.
func (m *LevelingModule) runCatchupQueue(queue []string) {
	defer func() {
		if r := recover(); r != nil {
			m.logError("Leveling: panic in catch-up queue: %v", r)
		}
	}()
	for _, guildID := range queue {
		m.catchupGuild(guildID)
	}
}

func (m *LevelingModule) catchupGuild(guildID string) {
	cfg, err := m.store.GetConfig(guildID)
	if err != nil {
		m.logError("Leveling: catch-up config for guild %s: %v", guildID, err)
		m.finishCatchup(guildID, catchupResult{hardError: err.Error()})
		return
	}
	if !cfg.Enabled || !cfg.CatchupEnabled {
		m.finishCatchup(guildID, catchupResult{})
		return
	}
	meta, err := m.store.Meta(guildID)
	if err != nil {
		m.finishCatchup(guildID, catchupResult{hardError: err.Error()})
		return
	}
	if meta.LastSeenAt.IsZero() {
		// First run for this guild: record "caught up as of now" and never
		// backfill history that predates leveling.
		m.touchSeen(guildID)
		m.finishCatchup(guildID, catchupResult{})
		return
	}
	if m.ctx == nil || m.ctx.Rest == nil {
		m.finishCatchup(guildID, catchupResult{hardError: "Discord REST client unavailable"})
		return
	}

	guild, err := snowflake.Parse(guildID)
	if err != nil {
		m.finishCatchup(guildID, catchupResult{hardError: "invalid guild id"})
		return
	}

	m.setCatchupState(guildID, func(cs *catchupState) {
		cs.running = true
		cs.processed = 0
		cs.awarded = 0
		cs.truncated = false
		cs.lastErr = ""
	})

	channels, truncated, err := m.catchupChannels(guild, cfg)
	if err != nil {
		m.finishCatchup(guildID, catchupResult{hardError: err.Error()})
		return
	}
	result := catchupResult{truncated: truncated}
	deadline := time.Now().Add(catchupGuildBudget)

	for _, ch := range channels {
		if time.Now().After(deadline) {
			result.truncated = true
			break
		}
		// A dashboard edit mid-replay (disable, or catch-up off) aborts the
		// rest of the scan immediately.
		current, err := m.store.GetConfig(guildID)
		if err != nil || !current.Enabled || !current.CatchupEnabled {
			result.truncated = true
			break
		}
		cfg = current
		res, err := m.catchupChannel(guild, cfg, ch, meta.LastSeenAt, deadline)
		result.processed += res.processed
		result.awarded += res.awarded
		result.crossed = result.crossed || res.crossed
		result.truncated = result.truncated || res.truncated
		if res.hardError != "" {
			result.hardError = res.hardError
		}
		if err != nil {
			m.logError("Leveling: catch-up channel %s in guild %s: %v", ch.ID, guildID, err)
		}
	}
	m.finishCatchup(guildID, result)
}

// finishCatchup records the outcome: a clean, complete scan advances
// last_seen_at; a failure or cap leaves it alone so the next restart retries
// (already-processed channels resume from their checkpoints).
func (m *LevelingModule) finishCatchup(guildID string, result catchupResult) {
	complete := result.hardError == "" && !result.truncated
	if complete {
		m.touchSeen(guildID)
	}
	m.setCatchupState(guildID, func(cs *catchupState) {
		cs.running = false
		cs.processed = result.processed
		cs.awarded = result.awarded
		cs.truncated = result.truncated
		cs.lastErr = result.hardError
	})
	m.setCatchupOK(guildID, complete)
	m.setCatching(guildID, false)

	cfg, err := m.store.GetConfig(guildID)
	if err != nil {
		return
	}
	crossed := m.drainBuffered(guildID, cfg)
	if crossed || result.crossed {
		// Automatic role backfill after a replay; no announcements.
		if _, err := m.StartRewardSync(guildID); err != nil {
			m.logError("Leveling: reward sync after catch-up for guild %s: %v", guildID, err)
		}
	}
}

func (m *LevelingModule) touchSeen(guildID string) {
	if err := m.store.TouchSeen(guildID, time.Now().UTC()); err != nil {
		m.logError("Leveling: touch last_seen for guild %s: %v", guildID, err)
	}
}

func (m *LevelingModule) setCatchupState(guildID string, fn func(*catchupState)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.catchup == nil {
		m.catchup = map[string]*catchupState{}
	}
	cs := m.catchup[guildID]
	if cs == nil {
		cs = &catchupState{}
		m.catchup[guildID] = cs
	}
	fn(cs)
}

// catchupChannels lists the text/news channels and active threads to scan.
// Channels that already have a checkpoint come first (they resume mid-history),
// then by position order.
func (m *LevelingModule) catchupChannels(guild snowflake.ID, cfg modules.LevelingConfig) ([]catchupChannel, bool, error) {
	raw, err := m.ctx.Rest.GetGuildChannels(guild)
	if err != nil {
		return nil, false, fmt.Errorf("list guild channels: %w", err)
	}
	var channels []catchupChannel
	for _, ch := range raw {
		switch ch.Type() {
		case discord.ChannelTypeGuildText, discord.ChannelTypeGuildNews:
		default:
			continue
		}
		id := ch.ID()
		if containsID(cfg.IgnoredChannels, id.String()) {
			continue
		}
		channels = append(channels, catchupChannel{
			ID:       id,
			Name:     ch.Name(),
			Position: ch.Position(),
		})
	}

	// Archived threads and forum posts are not backfilled; live gateway
	// events cover them while the bot is online.
	threads, err := m.ctx.Rest.GetActiveGuildThreads(guild)
	if err != nil {
		m.logError("Leveling: list active threads for guild %s: %v", guild, err)
	} else if threads != nil {
		for _, th := range threads.Threads {
			switch th.Type() {
			case discord.ChannelTypeGuildPublicThread, discord.ChannelTypeGuildPrivateThread, discord.ChannelTypeGuildNewsThread:
			default:
				continue
			}
			if containsID(cfg.IgnoredChannels, th.ID().String()) {
				continue
			}
			position := th.Position()
			if parent := th.ParentID(); parent != nil {
				for _, ch := range channels {
					if ch.ID == *parent {
						position = ch.Position
						break
					}
				}
			}
			channels = append(channels, catchupChannel{
				ID:       th.ID(),
				Name:     th.Name(),
				Position: position,
				Thread:   true,
			})
		}
	}

	for i := range channels {
		if cp, err := m.store.Checkpoint(guild.String(), channels[i].ID.String()); err == nil {
			channels[i].HasCheckpoint = cp.LastMessageID != 0
		}
	}
	sort.SliceStable(channels, func(i, j int) bool {
		if channels[i].HasCheckpoint != channels[j].HasCheckpoint {
			return channels[i].HasCheckpoint
		}
		if channels[i].Thread != channels[j].Thread {
			return !channels[i].Thread
		}
		if channels[i].Position != channels[j].Position {
			return channels[i].Position < channels[j].Position
		}
		return channels[i].ID < channels[j].ID
	})

	truncated := false
	if len(channels) > cfg.CatchupMaxChannels {
		channels = channels[:cfg.CatchupMaxChannels]
		truncated = true
	}
	return channels, truncated, nil
}

// catchupChannel replays one channel, bounded by the per-channel message cap.
func (m *LevelingModule) catchupChannel(guild snowflake.ID, cfg modules.LevelingConfig, ch catchupChannel, lastSeen time.Time, deadline time.Time) (catchupResult, error) {
	var result catchupResult
	guildID := guild.String()
	maxMessages := cfg.CatchupMaxMessages
	if maxMessages < 1 {
		maxMessages = defaultConfig().CatchupMaxMessages
	}
	maxPages := (maxMessages + catchupPageSize - 1) / catchupPageSize

	cp, err := m.store.Checkpoint(guildID, ch.ID.String())
	if err != nil {
		return result, err
	}

	var collected []discord.Message
	if cp.LastMessageID != 0 {
		after := cp.LastMessageID
		for page := 0; page < maxPages && len(collected) < maxMessages; page++ {
			batch, err := m.ctx.Rest.GetMessages(ch.ID, 0, 0, after, catchupPageSize)
			if err != nil {
				if isSkippableChannelError(err) {
					return result, nil
				}
				result.hardError = fmt.Sprintf("Read channel %s: %v", ch.ID, err)
				return result, err
			}
			if len(batch) == 0 {
				break
			}
			newest := batch[0].ID
			sort.Slice(batch, func(i, j int) bool { return batch[i].ID < batch[j].ID })
			for _, msg := range batch {
				if len(collected) >= maxMessages {
					result.truncated = true
					break
				}
				collected = append(collected, msg)
			}
			if result.truncated {
				break
			}
			after = newest
			if len(batch) < catchupPageSize {
				break
			}
			if !m.sleepBefore(deadline) {
				result.truncated = true
				break
			}
		}
	} else {
		// No checkpoint: page backwards from the newest message until the
		// scan reaches the moment the bot went offline.
		var before snowflake.ID
		for page := 0; page < maxPages && len(collected) < maxMessages; page++ {
			batch, err := m.ctx.Rest.GetMessages(ch.ID, 0, before, 0, catchupPageSize)
			if err != nil {
				if isSkippableChannelError(err) {
					return result, nil
				}
				result.hardError = fmt.Sprintf("Read channel %s: %v", ch.ID, err)
				return result, err
			}
			if len(batch) == 0 {
				break
			}
			newest := batch[0]
			stop := false
			// The batch is newest-first, so walk forward and stop at the first
			// message that predates the outage: everything after it is older.
			for i := range batch {
				msg := batch[i]
				if !msg.CreatedAt.After(lastSeen) {
					stop = true
					break
				}
				if len(collected) >= maxMessages {
					stop = true
					result.truncated = true
					break
				}
				collected = append(collected, msg)
			}
			if stop || len(batch) < catchupPageSize {
				// A quiet channel must not be rescanned on every startup.
				if err := m.store.AdvanceCheckpoint(guildID, ch.ID.String(), newest.ID, newest.CreatedAt); err != nil {
					m.logError("Leveling: advance checkpoint for channel %s: %v", ch.ID, err)
				}
				break
			}
			before = batch[len(batch)-1].ID
			if !m.sleepBefore(deadline) {
				result.truncated = true
				break
			}
		}
		// collected is newest-first from the backward scan.
		sort.Slice(collected, func(i, j int) bool { return collected[i].ID < collected[j].ID })
	}

	for _, msg := range collected {
		result.processed++
		awarded, crossed := m.processCatchupMessage(cfg, guildID, ch.ID.String(), msg)
		if awarded {
			result.awarded++
		}
		result.crossed = result.crossed || crossed
	}
	return result, nil
}

// processCatchupMessage records one replayed message. The checkpoint always
// advances, even for bots and system messages, so a channel is never scanned
// repeatedly for messages that can never earn XP.
func (m *LevelingModule) processCatchupMessage(cfg modules.LevelingConfig, guildID, channelID string, msg discord.Message) (awarded, crossed bool) {
	if msg.Author.ID == 0 {
		return false, false
	}
	userID := msg.Author.ID.String()
	if msg.Type.System() || msg.Author.Bot || msg.WebhookID != nil || msg.ApplicationID != nil {
		if _, _, _, err := m.store.AwardMessage(guildID, channelID, userID, msg.ID, msg.CreatedAt, 0); err != nil {
			m.logError("Leveling: catch-up checkpoint for guild %s: %v", guildID, err)
		}
		return false, false
	}
	roleIDs := m.resolveRoles(guildID, userID, msg.Member)
	return m.awardMessage(cfg, guildID, channelID, userID, roleIDs, msg.ID, msg.CreatedAt, false)
}

// sleepBefore waits the inter-request gap and reports whether the guild
// budget still allows another request.
func (m *LevelingModule) sleepBefore(deadline time.Time) bool {
	if time.Now().Add(catchupRequestGap).After(deadline) {
		return false
	}
	time.Sleep(catchupRequestGap)
	return true
}

// isSkippableChannelError reports channels the bot cannot read (or that no
// longer exist): those are skipped, not treated as a failed catch-up.
func isSkippableChannelError(err error) bool {
	var restErr *rest.Error
	if !errors.As(err, &restErr) {
		return false
	}
	if restErr.Code == rest.JSONErrorCodeUnknownChannel {
		return true
	}
	if restErr.Response == nil {
		return false
	}
	switch restErr.Response.StatusCode {
	case http.StatusNotFound, http.StatusForbidden:
		return true
	}
	return false
}
