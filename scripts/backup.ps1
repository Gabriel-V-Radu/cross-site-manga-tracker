[CmdletBinding()]
param(
    [ValidateSet("local", "docker")]
    [string]$Mode = "local",

    [string]$OutputDir = ".backups",

    [string]$LocalDbPath = "backend/data/app.sqlite",

    [string]$ContainerName = "cross-site-tracker-api",

    [string]$DockerDbPath = "/app/data/app.sqlite",

    [int]$KeepLast = 10
)

$ErrorActionPreference = "Stop"

function Assert-CommandExists {
    param([string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command '$Name' not found in PATH."
    }
}

if (-not (Test-Path $OutputDir)) {
    New-Item -ItemType Directory -Path $OutputDir -Force | Out-Null
}

$timestamp = Get-Date -Format "yyyyMMdd-HHmmss"
$backupFile = Join-Path $OutputDir "tracker-backup-$Mode-$timestamp.sqlite"

# The database runs in WAL mode, so the newest committed data lives in the
# -wal sidecar until a checkpoint folds it back — it is routinely larger than
# the main file. Copying app.sqlite alone therefore produces a backup that is
# silently missing the most recent writes, which is the worst kind of backup:
# one that succeeds. There is no sqlite3 binary on the host or in the image to
# checkpoint with, so the whole file set travels together and restore puts it
# back together.
if ($Mode -eq "local") {
    if (-not (Test-Path $LocalDbPath)) {
        throw "Local database not found at '$LocalDbPath'."
    }

    Copy-Item -Path $LocalDbPath -Destination $backupFile -Force
    foreach ($suffix in @("-wal", "-shm")) {
        $sidecar = "$LocalDbPath$suffix"
        if (Test-Path $sidecar) {
            Copy-Item -Path $sidecar -Destination "$backupFile$suffix" -Force
        }
    }
}
else {
    Assert-CommandExists "docker"

    $containerId = docker ps --filter "name=$ContainerName" --format "{{.ID}}"
    if ([string]::IsNullOrWhiteSpace($containerId)) {
        throw "Docker container '$ContainerName' is not running."
    }

    docker cp "$ContainerName`:$DockerDbPath" "$backupFile" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "docker cp of the database failed; no backup was written." }
    # The sidecars are absent when the database was closed cleanly, so a
    # failure here is expected rather than fatal.
    foreach ($suffix in @("-wal", "-shm")) {
        docker cp "$ContainerName`:$DockerDbPath$suffix" "$backupFile$suffix" 2>$null | Out-Null
    }
    Write-Host "Note: taken from a running container, so the file set is a moment-in-time copy rather than a quiesced one. Stop the container first if you need a guaranteed-consistent backup."
}

if ($KeepLast -gt 0) {
    # Retire each backup with its sidecars: a main file left without its -wal,
    # or a -wal left without its main file, is worse than no backup at all.
    Get-ChildItem -Path $OutputDir -Filter "tracker-backup-*.sqlite" |
        Sort-Object LastWriteTime -Descending |
        Select-Object -Skip $KeepLast |
        ForEach-Object {
            Remove-Item -Path "$($_.FullName)-wal" -Force -ErrorAction SilentlyContinue
            Remove-Item -Path "$($_.FullName)-shm" -Force -ErrorAction SilentlyContinue
            Remove-Item -Path $_.FullName -Force -ErrorAction SilentlyContinue
        }
}

Write-Host "Backup created: $backupFile"
