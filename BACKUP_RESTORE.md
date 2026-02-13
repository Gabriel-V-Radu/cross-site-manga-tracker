# Backup and Restore

This project uses SQLite as the single source of truth. Backup/restore is file-based.

## Scripts
- Backup: `scripts/backup.ps1`
- Restore: `scripts/restore.ps1`

## Local mode (single-PC)

### Backup
```powershell
./scripts/backup.ps1 -Mode local
```

Optional:
```powershell
./scripts/backup.ps1 -Mode local -LocalDbPath backend/data/app.sqlite -OutputDir .backups -KeepLast 20
```

### Restore
```powershell
./scripts/restore.ps1 -Mode local -BackupFile .backups/tracker-backup-local-YYYYMMDD-HHMMSS.sqlite
```

Behavior:
- Creates a `pre-restore-*.sqlite` snapshot in `backend/data/` unless `-SkipPreBackup` is used.

## Docker mode

### Backup
```powershell
./scripts/backup.ps1 -Mode docker
```

Optional:
```powershell
./scripts/backup.ps1 -Mode docker -ContainerName cross-site-tracker-api -DockerDbPath /app/data/app.sqlite
```

### Restore
```powershell
./scripts/restore.ps1 -Mode docker -BackupFile .backups/tracker-backup-docker-YYYYMMDD-HHMMSS.sqlite -RestartContainer
```

Behavior:
- Stops running container before restore.
- Creates a pre-restore snapshot in `%TEMP%/cross-site-tracker-restore/` unless `-SkipPreBackup` is used.
- Restarts container if it was running (or if `-RestartContainer` is passed).

## Suggested routine
- Run daily backup with Task Scheduler.
- Keep at least 10 backups (`-KeepLast 10` is default).
- Test restore monthly on a copy.

## Notes
- Keep backups outside the repo for long-term retention.
- If `docker` is unavailable, use local mode with a copied DB file.
