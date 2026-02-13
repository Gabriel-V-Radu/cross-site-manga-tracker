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
        Write-Host "Pre-restore backup created: $preBackup"
    }

    Copy-Item -Path $BackupFile -Destination $LocalDbPath -Force
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
    Write-Host "Pre-restore backup created: $preBackupPath"
}

docker cp "$BackupFile" "$ContainerName`:$DockerDbPath" | Out-Null
Write-Host "Restore copied into container path: $DockerDbPath"

if ($RestartContainer -or -not [string]::IsNullOrWhiteSpace($isRunning)) {
    docker start $ContainerName | Out-Null
    Write-Host "Container restarted: $ContainerName"
}

Write-Host "Restore completed (docker)."
