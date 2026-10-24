package leveling

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3" // cgo SQLite driver: cgo is already required by the voice stack

	"github.com/misfit/bot/modules"
)

// schemaVersion is bumped whenever schemaSQL changes; user_version records it.
const schemaVersion = 1

// schemaSQL is the leveling module's entire durable state: per-guild settings,
// XP rows, granted reward roles, per-channel catch-up checkpoints and the
// last-seen watermark. Everything lives in one SQLite file so a restart is
// atomic and no partial YAML/JSON file can corrupt the module.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS guild_config (
	guild_id                 INTEGER PRIMARY KEY,
	enabled                  INTEGER NOT NULL DEFAULT 0,
	message_enabled          INTEGER NOT NULL DEFAULT 1,
	message_xp_min           INTEGER NOT NULL DEFAULT 15,
	message_xp_max           INTEGER NOT NULL DEFAULT 25,
	message_cooldown_seconds INTEGER NOT NULL DEFAULT 60,
	voice_enabled            INTEGER NOT NULL DEFAULT 1,
	voice_xp_per_minute      INTEGER NOT NULL DEFAULT 10,
	voice_require_others     INTEGER NOT NULL DEFAULT 1,
	max_level                INTEGER NOT NULL DEFAULT 500,
	announce_enabled         INTEGER NOT NULL DEFAULT 1,
	announce_channel         INTEGER NOT NULL DEFAULT 0,
	announce_message         TEXT    NOT NULL DEFAULT '',
	catchup_enabled          INTEGER NOT NULL DEFAULT 1,
	catchup_max_messages     INTEGER NOT NULL DEFAULT 2000,
	catchup_max_channels     INTEGER NOT NULL DEFAULT 50,
	updated_at               INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS guild_reward (
	guild_id INTEGER NOT NULL,
	level    INTEGER NOT NULL,
	role_id  INTEGER NOT NULL,
	PRIMARY KEY (guild_id, level, role_id)
);

CREATE TABLE IF NOT EXISTS guild_ignored (
	guild_id  INTEGER NOT NULL,
	kind      TEXT    NOT NULL,
	target_id INTEGER NOT NULL,
	PRIMARY KEY (guild_id, kind, target_id)
);

CREATE TABLE IF NOT EXISTS guild_state (
	guild_id     INTEGER PRIMARY KEY,
	last_seen_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS member_xp (
	guild_id      INTEGER NOT NULL,
	user_id       INTEGER NOT NULL,
	xp            INTEGER NOT NULL DEFAULT 0,
	messages      INTEGER NOT NULL DEFAULT 0,
	voice_seconds INTEGER NOT NULL DEFAULT 0,
	updated_at    INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (guild_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_member_xp_rank ON member_xp (guild_id, xp DESC, user_id ASC);

CREATE TABLE IF NOT EXISTS member_reward_role (
	guild_id   INTEGER NOT NULL,
	user_id    INTEGER NOT NULL,
	role_id    INTEGER NOT NULL,
	granted_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (guild_id, user_id, role_id)
);

CREATE TABLE IF NOT EXISTS channel_checkpoint (
	guild_id        INTEGER NOT NULL,
	channel_id      INTEGER NOT NULL,
	last_message_id INTEGER NOT NULL DEFAULT 0,
	last_message_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (guild_id, channel_id)
);
`

// levelStore owns the module database. One SQLite connection serializes every
// statement, which removes SQLITE_BUSY entirely at this workload: reads are
// sub-millisecond and writes are per message.
type levelStore struct {
	db  *sql.DB
	log func(msg string, args ...any)

	mu      sync.RWMutex
	configs map[string]modules.LevelingConfig
}

// openStore opens (creating if needed) the module database at path.
func openStore(path string, log func(msg string, args ...any)) (*levelStore, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	u := url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(path),
		RawQuery: "_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&_foreign_keys=1",
	}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, fmt.Errorf("open leveling database: %w", err)
	}
	// SQLite allows a single writer; one pooled connection keeps writes
	// serialized and makes read-after-write ordering deterministic.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect leveling database: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create leveling schema: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("stamp leveling schema version: %w", err)
	}
	return &levelStore{db: db, log: log, configs: map[string]modules.LevelingConfig{}}, nil
}

func (s *levelStore) Close() error { return s.db.Close() }

// ── id / time helpers ────────────────────────────────────────────────────

// idInt converts a validated snowflake string to the INTEGER form stored in
// SQLite, so ID columns sort numerically.
func idInt(s string) (int64, error) {
	id, err := canonicalSnowflake(s, "snowflake")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(id, 10, 64)
}

func idString(id int64) string { return strconv.FormatInt(id, 10) }

func toMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func splitIDs(csv string) []string {
	if csv == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
