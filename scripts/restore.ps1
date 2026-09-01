[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BackupFile,

    [ValidateSet("local", "docker")]
    [string]$Mode = "local",

    [string]$LocalDbPath = "backend/data/app.sqlite",

    [string]$ContainerName = "cross-site-tracker-api",

    [string]$DockerDbPath = "/app/data/app.sqlite",

    [switch]$SkipPreBackup,

    [switch]$RestartContainer
)

$ErrorActionPreference = "Stop"

function Assert-CommandExists {
    param([string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command '$Name' not found in PATH."
    }
}

if (-not (Test-Path $BackupFile)) {
    throw "Backup file '$BackupFile' not found."
}

if ($Mode -eq "local") {
    $dbDir = Split-Path -Path $LocalDbPath -Parent
    if (-not (Test-Path $dbDir)) {
        New-Item -ItemType Directory -Path $dbDir -Force | Out-Null
    }

    if ((Test-Path $LocalDbPath) -and (-not $SkipPreBackup)) {
        $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
        $preBackup = Join-Path $dbDir "pre-restore-$stamp.sqlite"
        Copy-Item -Path $LocalDbPath -Destination $preBackup -Force
        # The sidecars carry whatever has not been checkpointed yet; without
        # them the safety copy is missing the newest writes.
        foreach ($suffix in @("-wal", "-shm")) {
            if (Test-Path "$LocalDbPath$suffix") {
                Copy-Item -Path "$LocalDbPath$suffix" -Destination "$preBackup$suffix" -Force
            }
        }
        Write-Host "Pre-restore backup created: $preBackup"
    }

    Copy-Item -Path $BackupFile -Destination $LocalDbPath -Force
    # Same rule as the docker branch below: the backup's own sidecars travel
    # with it, and a -wal left over from the database being replaced must not
    # survive, or SQLite replays it on top of the restored file and mixes two
    # databases. Locally the leftover can simply be deleted.
    foreach ($suffix in @("-wal", "-shm")) {
        if (Test-Path "$BackupFile$suffix") {
            Copy-Item -Path "$BackupFile$suffix" -Destination "$LocalDbPath$suffix" -Force
        }
        else {
            Remove-Item -Path "$LocalDbPath$suffix" -Force -ErrorAction SilentlyContinue
        }
    }
    Write-Host "Restore completed (local): $LocalDbPath"
    exit 0
}

Assert-CommandExists "docker"

$containerId = docker ps -a --filter "name=$ContainerName" --format "{{.ID}}"
if ([string]::IsNullOrWhiteSpace($containerId)) {
    throw "Docker container '$ContainerName' was not found. Start the compose stack at least once before restore."
}

$isRunning = docker ps --filter "name=$ContainerName" --format "{{.ID}}"
if (-not [string]::IsNullOrWhiteSpace($isRunning)) {
    docker stop $ContainerName | Out-Null
}

if (-not $SkipPreBackup) {
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $tmpDir = Join-Path $env:TEMP "cross-site-tracker-restore"
    if (-not (Test-Path $tmpDir)) {
        New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
    }
    $preBackupPath = Join-Path $tmpDir "pre-restore-$stamp.sqlite"
    docker cp "$ContainerName`:$DockerDbPath" "$preBackupPath" | Out-Null
    # The sidecars carry whatever has not been checkpointed yet, so the safety
    # copy is only a safety copy with them.
    foreach ($suffix in @("-wal", "-shm")) {
        docker cp "$ContainerName`:$DockerDbPath$suffix" "$preBackupPath$suffix" 2>$null | Out-Null
    }
    Write-Host "Pre-restore backup created: $preBackupPath"
}

docker cp "$BackupFile" "$ContainerName`:$DockerDbPath" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "docker cp of the backup failed; the database was left as it was." }

# A -wal left over from the database being replaced would be replayed on top of
# the one just restored, mixing two databases together. A clean shutdown
# checkpoints and removes it, so this normally finds nothing — but a container
# that was killed rather than stopped leaves one behind. The container is
# stopped here, so it cannot be deleted through exec; a zero-length file is the
# equivalent, since SQLite finds no valid WAL header and starts a fresh one.
$emptyFile = Join-Path $env:TEMP "cross-site-tracker-empty.tmp"
Set-Content -Path $emptyFile -Value $null -NoNewline
foreach ($suffix in @("-wal", "-shm")) {
    if (Test-Path "$BackupFile$suffix") {
        docker cp "$BackupFile$suffix" "$ContainerName`:$DockerDbPath$suffix" | Out-Null
    }
    else {
        docker cp "$emptyFile" "$ContainerName`:$DockerDbPath$suffix" 2>$null | Out-Null
    }
}
Remove-Item -Path $emptyFile -Force -ErrorAction SilentlyContinue
Write-Host "Restore copied into container path: $DockerDbPath"

if ($RestartContainer -or -not [string]::IsNullOrWhiteSpace($isRunning)) {
    docker start $ContainerName | Out-Null
    Write-Host "Container restarted: $ContainerName"
}

Write-Host "Restore completed (docker)."
