# Cross-Site Tracker

Current setup is local-first (single PC) with future-ready architecture for remote access.

## Structure
- `backend/` Go + Fiber API
- Frontend HTMX + Tailwind
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
- What it does:
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
   - Hard refresh restart: `./scripts/restart.ps1 -NoCache`
- Manual fallback:
   - `docker compose down --remove-orphans`

## Access Mode (Current)
- The app is intentionally exposed only on `localhost` (`127.0.0.1:8080`).
- This keeps usage single-PC for now.

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
- A cookie stores the active profile in the browser for convenience.

## Notes
- Migrations are auto-applied from `backend/migrations/`.
- SQLite database file defaults to `backend/data/app.sqlite` locally.
- Seed data inserts default sources and base settings.

## Backup and Restore
- Quick backup (local): `./scripts/backup.ps1 -Mode local`
- Quick restore (local): `./scripts/restore.ps1 -Mode local -BackupFile <path-to-backup.sqlite>`
- Docker backup: `./scripts/backup.ps1 -Mode docker`
- Docker restore: `./scripts/restore.ps1 -Mode docker -BackupFile <path-to-backup.sqlite> -RestartContainer`
- Full runbook: [BACKUP_RESTORE.md](BACKUP_RESTORE.md)
