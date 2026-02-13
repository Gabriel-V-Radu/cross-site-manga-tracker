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

if ($Mode -eq "local") {
    if (-not (Test-Path $LocalDbPath)) {
        throw "Local database not found at '$LocalDbPath'."
    }

    Copy-Item -Path $LocalDbPath -Destination $backupFile -Force
}
else {
    Assert-CommandExists "docker"

    $containerId = docker ps --filter "name=$ContainerName" --format "{{.ID}}"
    if ([string]::IsNullOrWhiteSpace($containerId)) {
        throw "Docker container '$ContainerName' is not running."
    }

    docker cp "$ContainerName`:$DockerDbPath" "$backupFile" | Out-Null
}

if ($KeepLast -gt 0) {
    Get-ChildItem -Path $OutputDir -Filter "tracker-backup-*.sqlite" |
        Sort-Object LastWriteTime -Descending |
        Select-Object -Skip $KeepLast |
        Remove-Item -Force -ErrorAction SilentlyContinue
}

Write-Host "Backup created: $backupFile"
