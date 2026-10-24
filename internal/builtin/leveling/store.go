package leveling

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/disgoorg/snowflake/v2"

	"github.com/misfit/bot/modules"
)

// xpEntry is one member's XP row.
type xpEntry struct {
	XP           int64
	Messages     int64
	VoiceSeconds int64
	TrackedRoles []string
	UpdatedAt    time.Time
}

// checkpoint is the newest message ID already accounted for in a channel.
type checkpoint struct {
	LastMessageID snowflake.ID
	LastMessageAt time.Time
}

// guildMeta is the cheap summary catch-up needs before scanning channels.
type guildMeta struct {
	LastSeenAt time.Time
}

// entryWithID pairs a member ID with their entry for sorted listings.
type entryWithID struct {
	UserID string
	xpEntry
}

// queryer is satisfied by both *sql.DB and *sql.Tx.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

// ── guild config ─────────────────────────────────────────────────────────

// GetConfig returns the guild's effective config, cached in memory. A guild
// with no saved row gets the defaults (leveling off).
func (s *levelStore) GetConfig(guildID string) (modules.LevelingConfig, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return modules.LevelingConfig{}, err
	}
	key := idString(gid)
	s.mu.RLock()
	if cfg, ok := s.configs[key]; ok {
		s.mu.RUnlock()
		return cfg, nil
	}
	s.mu.RUnlock()

	cfg, err := s.loadConfig(gid)
	if err != nil {
		return modules.LevelingConfig{}, err
	}
	s.mu.Lock()
	s.configs[key] = cfg
	s.mu.Unlock()
	return cfg, nil
}

func (s *levelStore) loadConfig(gid int64) (modules.LevelingConfig, error) {
	var (
		cfg                                            modules.LevelingConfig
		enabled, msgEnabled, voiceEnabled, voiceOthers int
		announceEnabled, catchupEnabled                int
		announceChannel                                int64
	)
	row := s.db.QueryRow(`
		SELECT enabled, message_enabled, message_xp_min, message_xp_max,
		       message_cooldown_seconds, voice_enabled, voice_xp_per_minute,
		       voice_require_others, max_level, announce_enabled,
		       announce_channel, announce_message, catchup_enabled,
		       catchup_max_messages, catchup_max_channels
		FROM guild_config WHERE guild_id = ?`, gid)
	err := row.Scan(
		&enabled, &msgEnabled, &cfg.MessageXPMin, &cfg.MessageXPMax,
		&cfg.MessageCooldownSeconds, &voiceEnabled, &cfg.VoiceXPPerMinute,
		&voiceOthers, &cfg.MaxLevel, &announceEnabled,
		&announceChannel, &cfg.AnnounceMessage, &catchupEnabled,
		&cfg.CatchupMaxMessages, &cfg.CatchupMaxChannels,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultConfig(), nil
	}
	if err != nil {
		return modules.LevelingConfig{}, fmt.Errorf("read leveling config: %w", err)
	}
	cfg.Enabled = enabled != 0
	cfg.MessageEnabled = msgEnabled != 0
	cfg.VoiceEnabled = voiceEnabled != 0
	cfg.VoiceRequireOthers = voiceOthers != 0
	cfg.AnnounceEnabled = announceEnabled != 0
	cfg.CatchupEnabled = catchupEnabled != 0
	if announceChannel != 0 {
		cfg.AnnounceChannel = idString(announceChannel)
	}

	rewards, err := s.loadRewards(gid)
	if err != nil {
		return modules.LevelingConfig{}, err
	}
	cfg.Rewards = rewards
	ignored, err := s.loadIgnored(gid)
	if err != nil {
		return modules.LevelingConfig{}, err
	}
	cfg.IgnoredChannels = ignored["channel"]
	cfg.IgnoredRoles = ignored["role"]

	norm, err := normalizeConfig(cfg)
	if err != nil {
		return modules.LevelingConfig{}, fmt.Errorf("invalid leveling config for guild %s: %w", idString(gid), err)
	}
	return norm, nil
}

func (s *levelStore) loadRewards(gid int64) ([]modules.LevelingReward, error) {
	rows, err := s.db.Query(`SELECT level, role_id FROM guild_reward WHERE guild_id = ? ORDER BY level, role_id`, gid)
	if err != nil {
		return nil, fmt.Errorf("read leveling rewards: %w", err)
	}
	defer rows.Close()
	var out []modules.LevelingReward
	for rows.Next() {
		var level int
		var roleID int64
		if err := rows.Scan(&level, &roleID); err != nil {
			return nil, err
		}
		out = append(out, modules.LevelingReward{Level: level, RoleID: idString(roleID)})
	}
	return out, rows.Err()
}

func (s *levelStore) loadIgnored(gid int64) (map[string][]string, error) {
	rows, err := s.db.Query(`SELECT kind, target_id FROM guild_ignored WHERE guild_id = ? ORDER BY kind, target_id`, gid)
	if err != nil {
		return nil, fmt.Errorf("read ignored leveling targets: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var kind string
		var targetID int64
		if err := rows.Scan(&kind, &targetID); err != nil {
			return nil, err
		}
		out[kind] = append(out[kind], idString(targetID))
	}
	return out, rows.Err()
}

// SaveConfig validates the config and replaces the guild's row in one
// transaction, so a failure can never leave half the settings saved.
func (s *levelStore) SaveConfig(guildID string, cfg modules.LevelingConfig) error {
	gid, err := idInt(guildID)
	if err != nil {
		return err
	}
	norm, err := normalizeConfig(cfg)
	if err != nil {
		return err
	}
	if err := validateConfig(norm); err != nil {
		return err
	}
	var announceChannel int64
	if norm.AnnounceChannel != "" {
		announceChannel, err = idInt(norm.AnnounceChannel)
		if err != nil {
			return fmt.Errorf("announcement channel: %w", err)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("save leveling config: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`
		INSERT INTO guild_config (
			guild_id, enabled, message_enabled, message_xp_min, message_xp_max,
			message_cooldown_seconds, voice_enabled, voice_xp_per_minute,
			voice_require_others, max_level, announce_enabled, announce_channel,
			announce_message, catchup_enabled, catchup_max_messages,
			catchup_max_channels, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(guild_id) DO UPDATE SET
			enabled = excluded.enabled,
			message_enabled = excluded.message_enabled,
			message_xp_min = excluded.message_xp_min,
			message_xp_max = excluded.message_xp_max,
			message_cooldown_seconds = excluded.message_cooldown_seconds,
			voice_enabled = excluded.voice_enabled,
			voice_xp_per_minute = excluded.voice_xp_per_minute,
			voice_require_others = excluded.voice_require_others,
			max_level = excluded.max_level,
			announce_enabled = excluded.announce_enabled,
			announce_channel = excluded.announce_channel,
			announce_message = excluded.announce_message,
			catchup_enabled = excluded.catchup_enabled,
			catchup_max_messages = excluded.catchup_max_messages,
			catchup_max_channels = excluded.catchup_max_channels,
			updated_at = excluded.updated_at`,
		gid, boolInt(norm.Enabled), boolInt(norm.MessageEnabled), norm.MessageXPMin,
		norm.MessageXPMax, norm.MessageCooldownSeconds, boolInt(norm.VoiceEnabled),
		norm.VoiceXPPerMinute, boolInt(norm.VoiceRequireOthers), norm.MaxLevel,
		boolInt(norm.AnnounceEnabled), announceChannel, norm.AnnounceMessage,
		boolInt(norm.CatchupEnabled), norm.CatchupMaxMessages,
		norm.CatchupMaxChannels, time.Now().UTC().UnixMilli(),
	); err != nil {
		return fmt.Errorf("write leveling config: %w", err)
	}

	if err := replaceRows(tx, "guild_reward", gid, func() error {
		for _, r := range norm.Rewards {
			roleID, err := idInt(r.RoleID)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO guild_reward (guild_id, level, role_id) VALUES (?,?,?)`, gid, r.Level, roleID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("write leveling rewards: %w", err)
	}

	if err := replaceRows(tx, "guild_ignored", gid, func() error {
		for _, ch := range norm.IgnoredChannels {
			chID, err := idInt(ch)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO guild_ignored (guild_id, kind, target_id) VALUES (?,?,?)`, gid, "channel", chID); err != nil {
				return err
			}
		}
		for _, role := range norm.IgnoredRoles {
			roleID, err := idInt(role)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO guild_ignored (guild_id, kind, target_id) VALUES (?,?,?)`, gid, "role", roleID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("write ignored leveling targets: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit leveling config: %w", err)
	}
	s.mu.Lock()
	s.configs[idString(gid)] = norm
	s.mu.Unlock()
	return nil
}

// replaceRows deletes a guild's rows from table, then runs insert to write the
// new set inside the caller's transaction.
func replaceRows(tx *sql.Tx, table string, gid int64, insert func() error) error {
	if _, err := tx.Exec(`DELETE FROM `+table+` WHERE guild_id = ?`, gid); err != nil {
		return err
	}
	return insert()
}

// GuildIDs lists every guild with saved settings, ascending.
func (s *levelStore) GuildIDs() ([]string, error) {
	rows, err := s.db.Query(`SELECT guild_id FROM guild_config ORDER BY guild_id`)
	if err != nil {
		return nil, fmt.Errorf("list leveling guilds: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var gid int64
		if err := rows.Scan(&gid); err != nil {
			return nil, err
		}
		out = append(out, idString(gid))
	}
	return out, rows.Err()
}

// ── XP ───────────────────────────────────────────────────────────────────

// AwardMessage records one guild message: the channel checkpoint always
// advances (so catch-up never re-scans it), and XP/message counters move only
// when amount > 0.
func (s *levelStore) AwardMessage(guildID, channelID, userID string, msgID snowflake.ID, ts time.Time, amount int64) (prevXP int64, cur xpEntry, awarded bool, err error) {
	gid, err := idInt(guildID)
	if err != nil {
		return 0, xpEntry{}, false, err
	}
	cid, err := idInt(channelID)
	if err != nil {
		return 0, xpEntry{}, false, err
	}
	uid, err := idInt(userID)
	if err != nil {
		return 0, xpEntry{}, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, xpEntry{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if msgID != 0 {
		if _, err := tx.Exec(`
			INSERT INTO channel_checkpoint (guild_id, channel_id, last_message_id, last_message_at)
			VALUES (?,?,?,?)
			ON CONFLICT(guild_id, channel_id) DO UPDATE SET
				last_message_id = excluded.last_message_id,
				last_message_at = excluded.last_message_at
			WHERE excluded.last_message_id > channel_checkpoint.last_message_id`,
			gid, cid, int64(msgID), toMillis(ts)); err != nil {
			return 0, xpEntry{}, false, fmt.Errorf("advance checkpoint: %w", err)
		}
	}

	if amount <= 0 {
		entry, ok, err := getEntryTx(tx, gid, uid)
		if err != nil {
			return 0, xpEntry{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, xpEntry{}, false, err
		}
		if !ok {
			return 0, xpEntry{}, false, nil
		}
		return entry.XP, entry, false, nil
	}

	prevEntry, _, err := getEntryTx(tx, gid, uid)
	if err != nil {
		return 0, xpEntry{}, false, err
	}
	prevXP = prevEntry.XP
	now := time.Now().UTC().UnixMilli()
	if _, err := tx.Exec(`
		INSERT INTO member_xp (guild_id, user_id, xp, messages, voice_seconds, updated_at)
		VALUES (?,?,?,1,0,?)
		ON CONFLICT(guild_id, user_id) DO UPDATE SET
			xp = member_xp.xp + excluded.xp,
			messages = member_xp.messages + 1,
			updated_at = excluded.updated_at`,
		gid, uid, amount, now); err != nil {
		return 0, xpEntry{}, false, fmt.Errorf("award message xp: %w", err)
	}
	cur, _, err = getEntryTx(tx, gid, uid)
	if err != nil {
		return 0, xpEntry{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, xpEntry{}, false, err
	}
	return prevXP, cur, true, nil
}

// AwardVoice credits one voice minute. Non-positive amounts are ignored.
func (s *levelStore) AwardVoice(guildID, userID string, amount int64) (prevXP int64, cur xpEntry, err error) {
	gid, err := idInt(guildID)
	if err != nil {
		return 0, xpEntry{}, err
	}
	uid, err := idInt(userID)
	if err != nil {
		return 0, xpEntry{}, err
	}
	if amount <= 0 {
		entry, _, err := getEntryDB(s.db, gid, uid)
		return entry.XP, entry, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, xpEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()

	prevEntry, _, err := getEntryTx(tx, gid, uid)
	if err != nil {
		return 0, xpEntry{}, err
	}
	prevXP = prevEntry.XP
	now := time.Now().UTC().UnixMilli()
	if _, err := tx.Exec(`
		INSERT INTO member_xp (guild_id, user_id, xp, messages, voice_seconds, updated_at)
		VALUES (?,?,?,0,60,?)
		ON CONFLICT(guild_id, user_id) DO UPDATE SET
			xp = member_xp.xp + excluded.xp,
			voice_seconds = member_xp.voice_seconds + 60,
			updated_at = excluded.updated_at`,
		gid, uid, amount, now); err != nil {
		return 0, xpEntry{}, fmt.Errorf("award voice xp: %w", err)
	}
	cur, _, err = getEntryTx(tx, gid, uid)
	if err != nil {
		return 0, xpEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return 0, xpEntry{}, err
	}
	return prevXP, cur, nil
}

const maxXPValue = int64(1_000_000_000_000)

// SetXP overwrites a member's total XP, leaving counters untouched.
func (s *levelStore) SetXP(guildID, userID string, xp int64) (xpEntry, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return xpEntry{}, err
	}
	uid, err := idInt(userID)
	if err != nil {
		return xpEntry{}, err
	}
	if xp < 0 || xp > maxXPValue {
		return xpEntry{}, fmt.Errorf("xp must be between 0 and %d", maxXPValue)
	}
	if _, err := s.db.Exec(`
		INSERT INTO member_xp (guild_id, user_id, xp, messages, voice_seconds, updated_at)
		VALUES (?,?,?,0,0,?)
		ON CONFLICT(guild_id, user_id) DO UPDATE SET xp = excluded.xp, updated_at = excluded.updated_at`,
		gid, uid, xp, time.Now().UTC().UnixMilli()); err != nil {
		return xpEntry{}, fmt.Errorf("set member xp: %w", err)
	}
	entry, _, err := getEntryDB(s.db, gid, uid)
	return entry, err
}

// RemoveEntry deletes a member's XP and granted reward roles, returning the
// roles so the caller can revoke them on Discord. Roles are read directly so a
// reset still revokes grants even if the XP row is already gone.
func (s *levelStore) RemoveEntry(guildID, userID string) ([]string, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return nil, err
	}
	uid, err := idInt(userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT role_id FROM member_reward_role WHERE guild_id = ? AND user_id = ? ORDER BY role_id`, gid, uid)
	if err != nil {
		return nil, fmt.Errorf("read member reward roles: %w", err)
	}
	var roles []string
	for rows.Next() {
		var roleID int64
		if err := rows.Scan(&roleID); err != nil {
			rows.Close()
			return nil, err
		}
		roles = append(roles, idString(roleID))
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM member_xp WHERE guild_id = ? AND user_id = ?`, gid, uid); err != nil {
		return nil, fmt.Errorf("remove member xp: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM member_reward_role WHERE guild_id = ? AND user_id = ?`, gid, uid); err != nil {
		return nil, fmt.Errorf("remove member reward roles: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return roles, nil
}

func (s *levelStore) GetEntry(guildID, userID string) (xpEntry, bool, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return xpEntry{}, false, err
	}
	uid, err := idInt(userID)
	if err != nil {
		return xpEntry{}, false, err
	}
	return getEntryDB(s.db, gid, uid)
}

// ListEntries returns up to limit entries starting at offset, ranked by XP
// desc then user ID asc. limit <= 0 returns every entry.
func (s *levelStore) ListEntries(guildID string, offset, limit int) ([]entryWithID, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return nil, err
	}
	query := `
		SELECT m.user_id, m.xp, m.messages, m.voice_seconds, m.updated_at,
		       COALESCE(GROUP_CONCAT(r.role_id), '')
		FROM member_xp m
		LEFT JOIN member_reward_role r ON r.guild_id = m.guild_id AND r.user_id = m.user_id
		WHERE m.guild_id = ?
		GROUP BY m.user_id
		ORDER BY m.xp DESC, m.user_id ASC`
	args := []any{gid}
	if limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, limit, max(offset, 0))
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list leveling entries: %w", err)
	}
	defer rows.Close()
	var out []entryWithID
	for rows.Next() {
		var (
			uid     int64
			entry   xpEntry
			updated int64
			roles   string
		)
		if err := rows.Scan(&uid, &entry.XP, &entry.Messages, &entry.VoiceSeconds, &updated, &roles); err != nil {
			return nil, err
		}
		entry.UpdatedAt = fromMillis(updated)
		entry.TrackedRoles = splitIDs(roles)
		out = append(out, entryWithID{UserID: idString(uid), xpEntry: entry})
	}
	return out, rows.Err()
}

// ListEntriesAtLeastXP returns every member with at least minXP, ranked the
// same way. The reward sync uses it to skip members below the lowest reward.
func (s *levelStore) ListEntriesAtLeastXP(guildID string, minXP int64) ([]entryWithID, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return nil, err
	}
	if minXP < 0 {
		minXP = 0
	}
	rows, err := s.db.Query(`
		SELECT m.user_id, m.xp, m.messages, m.voice_seconds, m.updated_at,
		       COALESCE(GROUP_CONCAT(r.role_id), '')
		FROM member_xp m
		LEFT JOIN member_reward_role r ON r.guild_id = m.guild_id AND r.user_id = m.user_id
		WHERE m.guild_id = ? AND m.xp >= ?
		GROUP BY m.user_id
		ORDER BY m.xp DESC, m.user_id ASC`, gid, minXP)
	if err != nil {
		return nil, fmt.Errorf("list leveling candidates: %w", err)
	}
	defer rows.Close()
	var out []entryWithID
	for rows.Next() {
		var (
			uid     int64
			entry   xpEntry
			updated int64
			roles   string
		)
		if err := rows.Scan(&uid, &entry.XP, &entry.Messages, &entry.VoiceSeconds, &updated, &roles); err != nil {
			return nil, err
		}
		entry.UpdatedAt = fromMillis(updated)
		entry.TrackedRoles = splitIDs(roles)
		out = append(out, entryWithID{UserID: idString(uid), xpEntry: entry})
	}
	return out, rows.Err()
}

// CountEntries returns how many members are tracked.
func (s *levelStore) CountEntries(guildID string) (int, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return 0, err
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM member_xp WHERE guild_id = ?`, gid).Scan(&count); err != nil {
		return 0, fmt.Errorf("count leveling entries: %w", err)
	}
	return count, nil
}

// SumXP returns the guild's total awarded XP.
func (s *levelStore) SumXP(guildID string) (int64, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return 0, err
	}
	var total sql.NullInt64
	if err := s.db.QueryRow(`SELECT SUM(xp) FROM member_xp WHERE guild_id = ?`, gid).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum leveling xp: %w", err)
	}
	return total.Int64, nil
}

// EntryRank returns a member's 1-based rank among the guild's tracked members
// and whether they are tracked at all.
func (s *levelStore) EntryRank(guildID, userID string) (int, bool, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return 0, false, err
	}
	uid, err := idInt(userID)
	if err != nil {
		return 0, false, err
	}
	entry, ok, err := getEntryDB(s.db, gid, uid)
	if err != nil || !ok {
		return 0, false, err
	}
	var above int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM member_xp
		WHERE guild_id = ? AND (xp > ? OR (xp = ? AND user_id < ?))`,
		gid, entry.XP, entry.XP, uid).Scan(&above); err != nil {
		return 0, false, fmt.Errorf("rank leveling entry: %w", err)
	}
	return above + 1, true, nil
}

// SetTrackedRoles replaces the roles the module granted to a member.
func (s *levelStore) SetTrackedRoles(guildID, userID string, roles []string) error {
	gid, err := idInt(guildID)
	if err != nil {
		return err
	}
	uid, err := idInt(userID)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(roles))
	for _, role := range roles {
		roleID, err := idInt(role)
		if err != nil {
			return err
		}
		ids = append(ids, roleID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().UnixMilli()
	// A reward grant implies the member is tracked, so make sure the XP row
	// exists: tracked roles hang off it for ranking and lookups.
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO member_xp (guild_id, user_id, xp, messages, voice_seconds, updated_at)
		VALUES (?,?,0,0,0,?)`, gid, uid, now); err != nil {
		return fmt.Errorf("ensure member row: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM member_reward_role WHERE guild_id = ? AND user_id = ?`, gid, uid); err != nil {
		return fmt.Errorf("clear tracked roles: %w", err)
	}
	for _, roleID := range ids {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO member_reward_role (guild_id, user_id, role_id, granted_at) VALUES (?,?,?,?)`,
			gid, uid, roleID, now); err != nil {
			return fmt.Errorf("write tracked roles: %w", err)
		}
	}
	return tx.Commit()
}

// ── catch-up state ───────────────────────────────────────────────────────

func (s *levelStore) Meta(guildID string) (guildMeta, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return guildMeta{}, err
	}
	var lastSeen int64
	err = s.db.QueryRow(`SELECT last_seen_at FROM guild_state WHERE guild_id = ?`, gid).Scan(&lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return guildMeta{}, nil
	}
	if err != nil {
		return guildMeta{}, fmt.Errorf("read leveling state: %w", err)
	}
	return guildMeta{LastSeenAt: fromMillis(lastSeen)}, nil
}

// TouchSeen records that the bot is caught up as of ts.
func (s *levelStore) TouchSeen(guildID string, ts time.Time) error {
	gid, err := idInt(guildID)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`
		INSERT INTO guild_state (guild_id, last_seen_at) VALUES (?,?)
		ON CONFLICT(guild_id) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		gid, toMillis(ts)); err != nil {
		return fmt.Errorf("touch leveling state: %w", err)
	}
	return nil
}

// AdvanceCheckpoint moves a channel's checkpoint forward without awarding XP.
func (s *levelStore) AdvanceCheckpoint(guildID, channelID string, msgID snowflake.ID, ts time.Time) error {
	gid, err := idInt(guildID)
	if err != nil {
		return err
	}
	cid, err := idInt(channelID)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`
		INSERT INTO channel_checkpoint (guild_id, channel_id, last_message_id, last_message_at)
		VALUES (?,?,?,?)
		ON CONFLICT(guild_id, channel_id) DO UPDATE SET
			last_message_id = excluded.last_message_id,
			last_message_at = excluded.last_message_at
		WHERE excluded.last_message_id > channel_checkpoint.last_message_id`,
		gid, cid, int64(msgID), toMillis(ts)); err != nil {
		return fmt.Errorf("advance checkpoint: %w", err)
	}
	return nil
}

// Checkpoint returns a channel's stored checkpoint.
func (s *levelStore) Checkpoint(guildID, channelID string) (checkpoint, error) {
	gid, err := idInt(guildID)
	if err != nil {
		return checkpoint{}, err
	}
	cid, err := idInt(channelID)
	if err != nil {
		return checkpoint{}, err
	}
	var (
		msgID int64
		msgAt int64
	)
	err = s.db.QueryRow(`SELECT last_message_id, last_message_at FROM channel_checkpoint WHERE guild_id = ? AND channel_id = ?`, gid, cid).
		Scan(&msgID, &msgAt)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint{}, nil
	}
	if err != nil {
		return checkpoint{}, fmt.Errorf("read checkpoint: %w", err)
	}
	return checkpoint{LastMessageID: snowflake.ID(msgID), LastMessageAt: fromMillis(msgAt)}, nil
}

// ── row helpers ──────────────────────────────────────────────────────────

const entrySelect = `
	SELECT m.xp, m.messages, m.voice_seconds, m.updated_at,
	       COALESCE(GROUP_CONCAT(r.role_id), '')
	FROM member_xp m
	LEFT JOIN member_reward_role r ON r.guild_id = m.guild_id AND r.user_id = m.user_id
	WHERE m.guild_id = ? AND m.user_id = ?
	GROUP BY m.user_id`

func getEntryDB(q queryer, gid, uid int64) (xpEntry, bool, error) {
	return scanEntry(q.QueryRow(entrySelect, gid, uid))
}

func getEntryTx(tx *sql.Tx, gid, uid int64) (xpEntry, bool, error) {
	return scanEntry(tx.QueryRow(entrySelect, gid, uid))
}

func scanEntry(row *sql.Row) (xpEntry, bool, error) {
	var (
		entry   xpEntry
		updated int64
		roles   string
	)
	err := row.Scan(&entry.XP, &entry.Messages, &entry.VoiceSeconds, &updated, &roles)
	if errors.Is(err, sql.ErrNoRows) {
		return xpEntry{}, false, nil
	}
	if err != nil {
		return xpEntry{}, false, fmt.Errorf("read leveling entry: %w", err)
	}
	entry.UpdatedAt = fromMillis(updated)
	entry.TrackedRoles = splitIDs(roles)
	return entry, true, nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
