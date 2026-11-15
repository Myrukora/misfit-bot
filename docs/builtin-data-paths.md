# Builtin data paths

The compiled-in feature modules were Go plugins in 0.1.0. As of 0.2.0 they are
built into the binary and their data home moved with them: each builtin's
`ctx.DataDir` is now **`modules/<name>/`**, not the plugin-era
`modules/Go/<name>/`. The paths in the table are the ones the code writes.

| Module | Data home | What lives there |
|---|---|---|
| `dashboard` | `modules/Go/dashboard/` | **Unchanged — the one deliberate exception** (not a module, pinned to the historical path in `DashboardModule`). `config.yml` (0600): `session_secret`, `client_id`, `allowed_guilds`, `exec_allowlist`, plus a legacy `client_secret` fallback. `listen`/`public_url` and `oauth.client_secret` live in the **core** `config.yml` and take priority when set. Sessions are in-memory only (no disk). Web assets are `go:embed` — not on disk. `exec_allowlist` is hand-edited only (no dashboard write path; empty blocks every web Run). |
| `cleanup` | *(none)* | Stateless — the builtin performs no `DataDir` writes at all (its `RequiredPerm` is `ManageMessages`; it only calls Discord's REST delete endpoints). |
| `tickets` | `modules/tickets/` | `config.yml` (bot-wide: `version`, `storage_retention_days`, `allow_dashboard_close`, `modals_enabled`), `guilds/<guildID>.yml` (per-guild `log_channel`, `types`, `panels`), and `tickets/<guildID>/` — one `<ticketID>.json` per ticket, an `index.json` open-ticket cache, `<ticketID>.html` transcripts, and `<ticketID>/files/` mirrored attachments. Every file under `tickets/` is **AES-256-GCM encrypted** (see `internal/securefile`); `data.key` (0600) is the key. |
| `imagefilter` | `modules/imagefilter/` | `config.json` (`clip_variant` + per-guild `enabled`/`threshold`/`punishment`/`mute_duration`/`log_channel`/`delete_on_none`), `spam_images/<guildID>/` (that guild's reference-image gallery), and `models/clip-vision-<variant>.onnx` (gitignored; provisioned by `scripts/setup_imagefilter.sh`). |
| `welcome` | `modules/welcome/` | `guilds/<guildID>.yaml` (0600, atomic): per-guild join/leave message, channel, auto-delete and embed builder config. No other state; delivery errors for the dashboard are in-memory only. |
| `moderation` | `modules/moderation/` | `guilds/<guildID>.yml` (0600, atomic): enabled, audit/warn channels, warn expiry, ban delete seconds and escalation rules. `cases/<guildID>.json` (0600, atomic, **AES-256-GCM encrypted**): the per-guild case audit (`next_id` + newest 20,000 cases, including removed warnings and automated actions); `data.key` (0600) is the key. |
| `reactionroles` | `modules/reactionroles/` | `guilds/<guildID>.yml` (0600, atomic): panel list with name/enabled/channel/embed/mappings and module-owned posted state (message ID, posted channel, synced emoji list). |
| `leveling` | `modules/leveling/` | `leveling.db` (SQLite, WAL, 0700 dir): `guild_config` (per-guild settings), `guild_reward` and `guild_ignored` (reward roles, ignored channels/roles), `member_xp` and `member_reward_role` (XP, message/voice counters, granted reward roles), `channel_checkpoint` (per-channel catch-up cursors) and `guild_state` (last-seen watermark). No YAML/JSON files. |

## Encryption at rest

Ticket and moderation state holds API Data (message content, user records), so
it is encrypted on disk rather than relying on filesystem permissions alone.
`internal/securefile` implements AES-256-GCM with a random 96-bit nonce per
file and a 5-byte magic header (`MFAT` + version).

| Path | Key |
|---|---|
| `modules/tickets/data.key` | encrypts everything under `modules/tickets/tickets/` (ticket JSON, `index.json`, transcripts, mirrored attachments) |
| `modules/moderation/data.key` | encrypts `modules/moderation/cases/*.json` |

- Keys are generated on first load at 0600 and are **never** in `config.yml`,
  `.env` or the repo. Back them up with the data: without the key the files are
  unrecoverable, and `LoadOrCreateKey` deliberately refuses to overwrite a
  malformed key rather than silently orphaning the archive.
- Migration is automatic and needs no offline step. Files without the header
  load as plaintext (older builds) and are sealed on their next save, and a
  startup sweep encrypts files that are written once and never re-saved (closed
  tickets, archived transcripts). The sweep is idempotent, atomic per file,
  symlink-safe, and logs a one-line count.
- `leveling.db` is intentionally **not** encrypted: it stores XP counters and
  role IDs only, never message content.
- The dashboard and the Discord transcript upload receive decrypted bytes
  through `modules.TicketFileReader` / `TicketTranscript`; they never read the
  sealed files directly.

## Notes

- **Dashboard is the exception.** It is core infrastructure, not a module, and
  `run()` constructs it with `DataDir: filepath.Join(Dir, Cfg.Modules.Path,
  "Go", "dashboard")`.
- **`.gitignore` covers the runtime state, not the source.** The relevant
  rules are `modules/tickets/`, `modules/imagefilter/`, `modules/welcome/`,
  `modules/moderation/`, `modules/reactionroles/` and `modules/leveling/`
  (whole builtin data dirs),
  `modules/Go/` (the dashboard data home plus any legacy plugin-era state), the
  generic `modules/{Lua,Python}/*/{config*.yml,config.json,data,logs,models}`
  module-data rules, `__pycache__/`/`*.pyc`, `*.so`, and `*.tmp` (the temp file
  every atomic writer creates between write and rename). Source for all of this
  lives in `internal/builtin/` and stays tracked.
- **Retired Python module.** `image_spam_filter`'s source was deleted in this
  release; a full copy of the backup lives in the gitignored
  `.hermes/backups/2026-09-09-pre-revamp/image_spam_filter`. Its old data dir
  (`modules/Python/image_spam_filter/`) is orphaned on disk and is read by
  nothing; delete it if present. The old gallery was a flat `spam_images/` file
  dump whose guild mapping lived inside `config.json`'s `guilds` map, which does
  not map unambiguously onto the builtin's per-guild
  `spam_images/<guildID>/` layout. Re-add those references for a guild through
  the dashboard's image-filter page if you still want them.
- `setupDirs()` at startup still creates `modules/Go`, `modules/Lua` and
  `modules/Python`. `modules/Go` is now only the dashboard's data home (plus any
  legacy straggler the owner never reconciled); no `.so` is built or loaded from
  it — `Manager.Load` rejects a Go plugin path outright.
