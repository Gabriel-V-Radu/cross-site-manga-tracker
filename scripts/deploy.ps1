[CmdletBinding()]
param(
    [string]$HealthUrl = "http://localhost:8080/health",
    [string]$DashboardUrl = "http://localhost:8080/dashboard",
    [int]$HealthTimeoutSeconds = 120,
    [switch]$NoPull,
    [switch]$NoCache,
    [switch]$NoBrowser
)

$ErrorActionPreference = "Stop"

function Assert-CommandExists {
    param([string]$Name)
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Required command '$Name' not found in PATH."
    }
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Resolve-Path (Join-Path $scriptDir "..")
Push-Location $repoRoot

try {
    Assert-CommandExists "docker"

    docker info | Out-Null

    Write-Host "Building containers..."
    $buildArgs = @("compose", "build")
    if (-not $NoPull) {
        $buildArgs += "--pull"
    }
    if ($NoCache) {
        $buildArgs += "--no-cache"
    }
    & docker @buildArgs

    Write-Host "Starting containers..."
    docker compose up -d --force-recreate --remove-orphans

    $deadline = (Get-Date).AddSeconds($HealthTimeoutSeconds)
    $isHealthy = $false

    Write-Host "Waiting for app health check: $HealthUrl"
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -UseBasicParsing -Uri $HealthUrl -TimeoutSec 5
            if ($resp.StatusCode -eq 200) {
                $isHealthy = $true
                break
            }
        }
        catch {
            Start-Sleep -Seconds 2
        }
    }

    if (-not $isHealthy) {
        throw "App did not become healthy within $HealthTimeoutSeconds seconds. Run 'docker compose logs api' for details."
    }

    Write-Host "App is running."
    Write-Host "Dashboard: $DashboardUrl"
    docker compose ps

    if (-not $NoBrowser) {
        Start-Process $DashboardUrl | Out-Null
    }
}
finally {
    Pop-Location
}
