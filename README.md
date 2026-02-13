# Cross-Site Tracker

Current setup is local-first (single PC) with future-ready architecture for remote access.

## Structure
- `backend/` Go + Fiber API
- `web/` Frontend placeholder for HTMX + Tailwind
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
- `docker compose up --build`
- Health check at `http://localhost:8080/health`
- Dashboard at `http://localhost:8080/dashboard`

## Access Mode (Current)
- The app is intentionally exposed only on `localhost` (`127.0.0.1:8080`).
- This keeps usage single-PC for now.

## Notes
- Migrations are auto-applied from `backend/migrations/`.
- SQLite database file defaults to `backend/data/app.sqlite` locally.
- Seed data inserts default sources and base settings.
- Future remote-access/Tailscale plan: see [FUTURE_IMPROVEMENTS.md](FUTURE_IMPROVEMENTS.md).

## Backup and Restore
- Quick backup (local): `./scripts/backup.ps1 -Mode local`
- Quick restore (local): `./scripts/restore.ps1 -Mode local -BackupFile <path-to-backup.sqlite>`
- Docker backup: `./scripts/backup.ps1 -Mode docker`
- Docker restore: `./scripts/restore.ps1 -Mode docker -BackupFile <path-to-backup.sqlite> -RestartContainer`
- Full runbook: [BACKUP_RESTORE.md](BACKUP_RESTORE.md)

## Release Docs
- Release notes: [RELEASE_NOTES_v0.1.md](RELEASE_NOTES_v0.1.md)
