# Builtin data paths (pinned)

After the Go-modules → compiled-in-core migration, the three former Go
plugins keep their **data folders on disk** exactly where they are today.
The code moves to `internal/`, but the folders remain the data home — no
file migration is needed on upgrade.

| Module | Data home (unchanged) | What lives there |
|---|---|---|
| `dashboard` | `modules/Go/dashboard/` | `config.yml` (0600): `session_secret`, `client_id`, `allowed_guilds`, plus `listen`/`public_url` fallback. Sessions are in-memory only (no disk). Web assets are `go:embed` — not on disk. |
| `cleanup` | *(none)* | Stateless — no `DataDir` writes at all. |
| `tickets` | `modules/Go/tickets/` | `config.yml` (0600, atomic tmp+rename) + `tickets/<guild>/…` (transcripts, attachments). |

## Notes

- The `modules/Go/<name>/` folders stay on disk even though no `.so` is
  built there anymore. They are **data home, not code home** — the code
  lives in `internal/`. `.gitignore` already covers the runtime files
  (`modules/*/{config*.yml,data/,logs/}`); the folders themselves are
  gitignored as build artifacts.
- Dashboard's `listen`/`public_url`/OAuth `client_secret` already live in
  the **core** `config.yml` (`dashboard:` / `oauth:` sections) — the module
  config file only holds the per-installation secrets. On migration the
  dashboard reads the core sections directly (same pattern as the updater)
  and keeps the module-local file only for `session_secret`/`client_id`/
  `allowed_guilds` until those are folded into core config.
- `cleanup` needs no data-dir handling anywhere.
