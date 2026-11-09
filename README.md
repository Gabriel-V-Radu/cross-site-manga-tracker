# Cross-Site Tracker

A self-hosted manga and novel tracker: one Go binary polls a set of reading
sites for new chapters and serves an HTMX dashboard. It runs on a Raspberry Pi
in Docker and is used from any browser on the same network.

## Structure
- `backend/` Go + Fiber API
- Frontend HTMX
- `docker-compose.yml` containerized API with persistent SQLite volume

## Local Run
1. Open terminal in `backend/`
2. Copy env file:
   - Windows PowerShell: `Copy-Item .env.example .env`
3. Run API:
   - `go run ./cmd/api`
4. Check health:
   - `http://localhost:8080/health`

## Docker Run
- `docker compose build --pull`
- `docker compose up -d --force-recreate --remove-orphans`
- Health check at `http://localhost:8080/health`
- Dashboard at `http://localhost:8080/dashboard`
- Docker uses `backend/data/app.sqlite` via bind mount (`./backend/data:/app/data`) so local and Docker runs share the same DB file.

## One-Click Deploy (Windows)
- Double-click `deploy.cmd` from the repo root, or run:
   - `./scripts/deploy.ps1`
- Requires Go on PATH: the deploy runs `go vet` and `go test` first and aborts
  if either fails, so a broken build never reaches the container.
- What it does:
   - Runs the backend test gate (skip with `-SkipTests`)
   - Builds containers with `docker compose build --pull`
   - Starts with `docker compose up -d --force-recreate --remove-orphans`
   - Waits for health check (`/health`)
   - Opens dashboard automatically (`/dashboard`)
- Optional hard refresh build (bypass build cache):
   - `./scripts/deploy.ps1 -NoCache`
- Stop the app:
   - Double-click `stop.cmd`, or run `./scripts/stop.ps1`
- Restart the app:
   - Double-click `restart.cmd`, or run `./scripts/restart.ps1`
   - Runs the test gate, then the deploy above. The running container is
     replaced only after the new image builds, so a failed build leaves the
     app up.
   - Hard refresh restart: `./scripts/restart.ps1 -NoCache`
- Manual fallback:
   - `docker compose down --remove-orphans`

## Access Mode (Current)
- `docker-compose.yml` publishes `8080:8080`, which binds every interface: the
  dashboard is reachable from any machine on the same network, not just this
  PC (that is how the Raspberry Pi deployment is used).
- There is no login — see "Profiles (No Login)" below. Anyone who can reach the
  port has full read and write access to the library.
- To make it single-PC, change the mapping to `"127.0.0.1:8080:8080"`.

## Profiles (No Login)
- The app now supports two local profiles with separate tracker libraries:
   - `profile1`
   - `profile2`
- Dashboard switching:
   - Open `http://localhost:8080/dashboard` and use the **Profile** dropdown.
   - Or open directly with query string: `http://localhost:8080/dashboard?profile=profile1` (or `profile2`).
- API usage (profile-aware):
   - Query parameter: `/v1/trackers?profile=profile1`
   - Header: `X-Profile-Key: profile1` or `X-Profile-ID: 1`
   - Writes (POST, PUT, DELETE) that a browser marks as coming from another
     site — including an `Origin: null` from a sandboxed frame or a `file:`
     page — are refused with 403, on `/v1` as on the dashboard. curl and
     scripts send no such marker and are unaffected.
   - POST and PUT bodies are JSON and must say so with
     `Content-Type: application/json` (curl: add
     `-H "Content-Type: application/json"`, since `-d` alone sends a form
     type); any other body is refused with 415. Every `/v1` error, unknown
     routes included, answers `{"message": "..."}`.
   - `lastReadChapter` and `latestKnownChapter` must be between 0 and 100000
     (decimals allowed); a PUT may send back a stored value outside that range
     unchanged. The site `sourceUrl` belongs to decides the stored `sourceId`.
- A cookie stores the active profile in the browser for convenience.

## Notes
- The schema is compiled into the binary (`backend/internal/database/migrations/`) and applied at startup; nothing to copy or configure.
- SQLite database file defaults to `backend/data/app.sqlite` locally.
- Seed data inserts the default sources and the two profiles.
- Site logos are uploaded once, from either profile's menu, and shown to both.

## Backup and Restore
- Quick backup (local): `./scripts/backup.ps1 -Mode local`
- Quick restore (local): `./scripts/restore.ps1 -Mode local -BackupFile <path-to-backup.sqlite>`
- Docker backup: `./scripts/backup.ps1 -Mode docker`
- Docker restore: `./scripts/restore.ps1 -Mode docker -BackupFile <path-to-backup.sqlite> -RestartContainer`
- Full runbook: [BACKUP_RESTORE.md](BACKUP_RESTORE.md)

## Link Review
- The dashboard's **Link review** page scans a source for series it can serve
  as a fallback for, and lets you accept, reject or paste the right URL for
  each match. Accepted links merge the source's alternate titles into the
  tracker, which is what makes later scans match better.

## Source Health
- The toolbar pill next to **Link review** says whether any site is failing
  ("Sources OK", "1 source failing", "2 sources with problems"); click it for
  each site's verdict, the reason (blocked, bot challenge, timed out, page
  lists no chapters…), how long it has been failing, any cooldown, the last raw
  error, and which of your trackers could not be checked.
- It is built from the poller's real resolves, not a probe, and lives in
  memory: after a restart it fills in again during the first poll cycle. With
  `POLLING_ENABLED=false` (the local run config) it says "Polling off".
- A site is degraded after one cycle where nothing got through (or a share of
  its series failed) and failing when that repeats in the next cycle, or when
  one cycle failed several series. `poll-once` prints the same verdicts.

## Operational Tools
Three commands ship in the image next to the server (`/usr/local/bin` in the
container) and can also be run from `backend/` with `go run ./cmd/<name>`:
- `cleanup-stale-sources`: see below.
- `repair-latest-chapter --assign "28=271,167=199"`: sets a tracker's latest
  chapter to a hand-verified value after a source poisoned it with a junk
  number. `--dry-run` previews without writing or migrating.
- `poll-once --db <copy-of-app.sqlite>` (not in the image): runs one full poll
  cycle against a database copy with the real connectors, for rehearsing
  poller changes. It writes to the copy, so never aim it at the live file.
  `--feeds 3 --feed-gap 4m` runs feed ticks instead of cycles.

## Logs
- The server logs to stdout (`docker logs`), which Docker keeps only as long
  as the container: a redeploy recreates it and the log is gone.
- With `LOG_FILE` set (`./data/logs/app.log` in `.env.example`) it also
  appends the same lines to that file in the data volume, which survives
  redeploys and reboots. The file is rotated at 5 MB and three old files are
  kept (`app.log.1` … `app.log.3`), so it never takes more than 20 MB.

## Polling
- Every `POLLING_MINUTES` (30) of rest the poller runs a cycle: each tracker
  that is due is asked about on one of its linked sites, in turn.
- Between cycles, every `FEED_MINUTES` (5; 0 turns it off), it reads each
  site's "latest updates" listing — one request per site — and polls at once
  the trackers a listing shows a new chapter for. The log line
  `poll found a new chapter … via=feed:<site>` (or `via=cycle`) says which of
  the two found each chapter.

## Cleanup Stale Sources (Removed Connectors / Old Custom Sites)
- Removes source records that no longer exist in the current connector registry.
- For trackers whose primary source is stale:
  - If an active linked source exists, it is promoted to primary.
  - Otherwise the tracker is deleted during cleanup.
- Run from `backend/`:
  - Preview only (default): `go run ./cmd/cleanup-stale-sources`
  - Apply cleanup: `go run ./cmd/cleanup-stale-sources --apply`
- Windows helper script from repo root:
  - Preview only: `./scripts/cleanup-stale-sources.ps1`
  - Apply cleanup: `./scripts/cleanup-stale-sources.ps1 -Apply`
