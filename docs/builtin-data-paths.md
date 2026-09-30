# Builtin data paths

The compiled-in feature modules were Go plugins in 0.1.0. As of 0.2.0 they are
built into the binary and their data home moved with them: each builtin's
`ctx.DataDir` is now **`modules/<name>/`**, not the plugin-era
`modules/Go/<name>/`. The first 0.2.0 start adopts the old folders forward (see
below), so nothing is lost on upgrade — but the paths in the table are the ones
the code writes from now on.

| Module | Data home | What lives there |
|---|---|---|
| `dashboard` | `modules/Go/dashboard/` | **Unchanged — the one deliberate exception** (not a module, pinned to the historical path in `DashboardModule`). `config.yml` (0600): `session_secret`, `client_id`, `allowed_guilds`, `exec_mode`, `exec_allowlist`, plus a legacy `client_secret` fallback. `listen`/`public_url` and `oauth.client_secret` live in the **core** `config.yml` and take priority when set. Sessions are in-memory only (no disk). Web assets are `go:embed` — not on disk. `exec_allowlist` is hand-edited only (no dashboard write path; empty blocks every web Run). |
| `cleanup` | *(none)* | Stateless — the builtin performs no `DataDir` writes at all (its `RequiredPerm` is `ManageMessages`; it only calls Discord's REST delete endpoints). |
| `tickets` | `modules/tickets/` | `config.yml` (bot-wide: `version`, `storage_retention_days`, `allow_dashboard_close`, `modals_enabled`), `guilds/<guildID>.yml` (per-guild `log_channel`, `types`, `panels`), and `tickets/<guildID>/` — one `<ticketID>.json` per ticket, an `index.json` open-ticket cache, `<ticketID>.html` transcripts, and `<ticketID>/files/` mirrored attachments. |
| `imagefilter` | `modules/imagefilter/` | `config.json` (`clip_variant` + per-guild `enabled`/`threshold`/`punishment`/`mute_duration`/`log_channel`/`delete_on_none`), `spam_images/<guildID>/` (that guild's reference-image gallery), and `models/clip-vision-<variant>.onnx` (gitignored; provisioned by `scripts/setup_imagefilter.sh`). |

## Notes

- **Adoption on upgrade.** Before the builtins' `OnLoad`,
  `cmd/bot/main.go`'s `run()` calls `modules.AdoptLegacyBuiltinData(baseDir,
  name, Log)` for `cleanup`, `tickets` and `imagefilter`; it moves
  `modules/Go/<name>/` → `modules/<name>/` — `os.Rename` when atomic, a
  recursive copy dropping `*.so` at every depth when the two paths are on
  different filesystems. It must run first: tickets' own v1/v2 → v3 migration
  (`internal/builtin/tickets/migrate.go` `migrateToV3`) only finds the legacy
  single-file config once it sits at the new data dir. When **both** directories
  hold state it only WARNs and moves nothing — the owner reconciles by hand. A
  legacy dir with nothing but `.so` files (or nothing at all) is a no-op, and
  the whole thing is idempotent.
- **Dashboard is the exception.** It is core infrastructure, not a module, and
  `run()` constructs it with `DataDir: filepath.Join(Dir, Cfg.Modules.Path,
  "Go", "dashboard")`. Its older pre-restructure migration still applies:
  `migrateLegacyConfig` copies `<config dir>/module_configs/dashboard/config.yml`
  to the pinned folder (only when the destination is absent, created `O_EXCL`
  0600, and the legacy file is never deleted).
- **`.gitignore` covers the runtime state, not the source.** The relevant
  rules are `modules/tickets/` and `modules/imagefilter/` (whole builtin data
  dirs), `modules/Go/*/{guilds,tickets,spam_images}/` and
  `modules/Go/*/config.json` (legacy plugin-era state), the generic
  `modules/{Go,Lua,Python}/*/{config*.yml,config.json,data,logs}` module-data
  rules, `*.so` and `modules/Go/*/*.so`, `*.tmp` (the temp file every atomic
  writer creates between write and rename), and `modules/imagefilter/models/`
  (regenerable via the setup script). Source for all of this lives in
  `internal/builtin/` and stays tracked.
- **Retired Python module.** `image_spam_filter`'s source was deleted in this
  release; a full copy of the backup lives in the gitignored
  `.hermes/backups/2026-09-09-pre-revamp/image_spam_filter`. Its data dir
  (`modules/Python/image_spam_filter/`, now explicitly gitignored) is orphaned
  on disk and is **not** adopted: its gallery was a flat `spam_images/` file
  dump whose guild mapping lived inside `config.json`'s `guilds` map, which does
  not map unambiguously onto the builtin's per-guild
  `spam_images/<guildID>/` layout. Re-add those references for a guild through
  the dashboard's image-filter page if you still want them.
- `setupDirs()` at startup still creates `modules/Go`, `modules/Lua` and
  `modules/Python`. `modules/Go` is now only the dashboard's data home (plus any
  legacy straggler the owner never reconciled); no `.so` is built or loaded from
  it — `migrateFromPluginEra()` removes stale plugin binaries, and
  `Manager.Load` rejects a Go plugin path outright.
