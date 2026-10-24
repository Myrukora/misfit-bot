package leveling

import (
	"testing"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
)

// TestCatchupPagination replays 250 messages after a checkpoint across three
// REST pages: every message is processed once, the checkpoint lands on the
// newest ID, and the per-member cooldown still limits the awards.
func TestCatchupPagination(t *testing.T) {
	m, r, _ := newHarness(t)
	const guildID, channelID, userID = "1", "2", "3"
	saveConfig(t, m, guildID, enabledConfig())
	r.channels = []discord.GuildChannel{textChannel(t, channelID, "general", 0)}

	base := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	if err := m.store.TouchSeen(guildID, base); err != nil {
		t.Fatalf("TouchSeen: %v", err)
	}
	if err := m.store.AdvanceCheckpoint(guildID, channelID, 1000, base); err != nil {
		t.Fatalf("AdvanceCheckpoint: %v", err)
	}

	// 250 messages, exactly one per cooldown window, so all 250 award.
	ascending := make([]discord.Message, 0, 250)
	for i := range 250 {
		ascending = append(ascending, testMessage(
			snowflake.ID(1001+i), 2, 3, base.Add(time.Duration(i+1)*time.Minute)))
	}
	r.setMessages(2, newestFirst(ascending))

	m.catchupGuild(guildID)

	cp, err := m.store.Checkpoint(guildID, channelID)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if cp.LastMessageID != 1250 {
		t.Fatalf("checkpoint = %d, want 1250", cp.LastMessageID)
	}
	entry, ok, err := m.store.GetEntry(guildID, userID)
	if err != nil || !ok {
		t.Fatalf("GetEntry = (%+v, %v, %v)", entry, ok, err)
	}
	if entry.Messages != 250 || entry.XP != 2500 {
		t.Fatalf("entry = %+v, want 250 messages / 2500 XP", entry)
	}
	stats, err := m.Stats(guildID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.CatchupProcessed != 250 || stats.CatchupAwarded != 250 {
		t.Fatalf("stats = %+v, want 250 processed / 250 awarded", stats)
	}
	if stats.CatchupTruncated || stats.CatchupError != "" {
		t.Fatalf("stats = %+v, want a clean scan", stats)
	}
	if !m.catchupOK[guildID] {
		t.Fatal("a complete catch-up must mark the guild caught up")
	}
}

// TestCatchupTruncatedByCap pins that hitting the per-channel message cap
// leaves the guild uncatch-up so the window is retried on the next restart.
func TestCatchupTruncatedByCap(t *testing.T) {
	m, r, _ := newHarness(t)
	const guildID, channelID = "1", "2"
	cfg := enabledConfig()
	cfg.CatchupMaxMessages = 150
	saveConfig(t, m, guildID, cfg)
	r.channels = []discord.GuildChannel{textChannel(t, channelID, "general", 0)}

	base := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	if err := m.store.TouchSeen(guildID, base); err != nil {
		t.Fatalf("TouchSeen: %v", err)
	}
	if err := m.store.AdvanceCheckpoint(guildID, channelID, 1000, base); err != nil {
		t.Fatalf("AdvanceCheckpoint: %v", err)
	}
	msgs := make([]discord.Message, 0, 400)
	for i := range 400 {
		msgs = append(msgs, testMessage(snowflake.ID(1001+i), 2, 3, base.Add(time.Duration(i+1)*time.Minute)))
	}
	r.setMessages(2, newestFirst(msgs))

	m.catchupGuild(guildID)

	stats, err := m.Stats(guildID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if !stats.CatchupTruncated {
		t.Fatalf("stats = %+v, want truncated", stats)
	}
	if stats.CatchupProcessed != 150 {
		t.Fatalf("processed = %d, want the 150-message cap", stats.CatchupProcessed)
	}
	if m.catchupOK[guildID] {
		t.Fatal("a truncated catch-up must not mark the guild caught up")
	}
}

// TestCatchupNoCheckpoint pins the first-ever scan: page back from the newest
// message and stop at the moment the bot went offline.
func TestCatchupNoCheckpoint(t *testing.T) {
	m, r, _ := newHarness(t)
	const guildID, channelID, userID = "1", "2", "3"
	saveConfig(t, m, guildID, enabledConfig())
	r.channels = []discord.GuildChannel{textChannel(t, channelID, "general", 0)}

	base := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	if err := m.store.TouchSeen(guildID, base); err != nil {
		t.Fatalf("TouchSeen: %v", err)
	}
	// Five messages predate the outage, three arrived while the bot was down.
	var msgs []discord.Message
	for i := range 5 {
		msgs = append(msgs, testMessage(
			snowflake.ID(1+i), 2, 3, base.Add(-time.Duration(5-i)*time.Minute)))
	}
	for i := range 3 {
		msgs = append(msgs, testMessage(
			snowflake.ID(6+i), 2, 3, base.Add(time.Duration(i+1)*time.Minute)))
	}
	r.setMessages(2, newestFirst(msgs))

	m.catchupGuild(guildID)

	entry, ok, err := m.store.GetEntry(guildID, userID)
	if err != nil || !ok {
		t.Fatalf("GetEntry = (%+v, %v, %v)", entry, ok, err)
	}
	if entry.Messages != 3 {
		t.Fatalf("messages = %d, want the 3 sent during downtime", entry.Messages)
	}
	cp, err := m.store.Checkpoint(guildID, channelID)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if cp.LastMessageID != 8 {
		t.Fatalf("checkpoint = %d, want the newest message 8", cp.LastMessageID)
	}
}

// TestCatchupBuffer pins that a live message arriving during catch-up is
// counted exactly once: the drain skips anything the checkpoint already
// covers.
func TestCatchupBuffer(t *testing.T) {
	m, _, _ := newHarness(t)
	const guildID, channelID, userID = "1", "2", "3"
	cfg := enabledConfig()
	saveConfig(t, m, guildID, cfg)

	base := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	if err := m.store.AdvanceCheckpoint(guildID, channelID, 5, base); err != nil {
		t.Fatalf("AdvanceCheckpoint: %v", err)
	}
	// 4 is below the checkpoint, 5 is at it, 6 is new: only 6 counts.
	for _, id := range []snowflake.ID{4, 5, 6} {
		m.bufferLive(bufferedMessage{
			GuildID: guildID, ChannelID: channelID, MessageID: id,
			UserID: userID, Timestamp: base.Add(time.Duration(id) * time.Minute),
		})
	}
	m.drainBuffered(guildID, cfg)

	entry, ok, err := m.store.GetEntry(guildID, userID)
	if err != nil || !ok {
		t.Fatalf("GetEntry = (%+v, %v, %v)", entry, ok, err)
	}
	if entry.Messages != 1 || entry.XP != 10 {
		t.Fatalf("entry = %+v, want exactly one award", entry)
	}
	cp, err := m.store.Checkpoint(guildID, channelID)
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if cp.LastMessageID != 6 {
		t.Fatalf("checkpoint = %d, want 6", cp.LastMessageID)
	}
}

// TestCatchupSkipsIgnoredChannels pins that an ignored channel is never scanned
// and keeps no checkpoint.
func TestCatchupSkipsIgnoredChannels(t *testing.T) {
	m, r, _ := newHarness(t)
	const guildID, channelID = "1", "2"
	cfg := enabledConfig()
	cfg.IgnoredChannels = []string{channelID}
	saveConfig(t, m, guildID, cfg)
	r.channels = []discord.GuildChannel{textChannel(t, channelID, "staff", 0)}

	base := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	if err := m.store.TouchSeen(guildID, base); err != nil {
		t.Fatalf("TouchSeen: %v", err)
	}
	r.setMessages(2, []discord.Message{testMessage(10, 2, 3, base.Add(time.Minute))})

	m.catchupGuild(guildID)

	if entry, ok, _ := m.store.GetEntry(guildID, "3"); ok && entry.Messages > 0 {
		t.Fatalf("ignored channel awarded XP: %+v", entry)
	}
}

// newestFirst reverses messages into Discord's newest-first order.
func newestFirst(ascending []discord.Message) []discord.Message {
	out := make([]discord.Message, len(ascending))
	for i, msg := range ascending {
		out[len(ascending)-1-i] = msg
	}
	return out
}
