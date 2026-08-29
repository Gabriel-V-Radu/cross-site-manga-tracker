[CmdletBinding()]
param(
    [string]$HealthUrl = "http://localhost:8080/health",
    [string]$DashboardUrl = "http://localhost:8080/dashboard",
    [int]$HealthTimeoutSeconds = 120,
    [switch]$NoPull,
    [switch]$NoCache,
    [switch]$NoBrowser,
    # Escape hatch for recovery: bringing the app back up must never be blocked
    # by a red or flaky test, or by a machine without Go on PATH.
    [switch]$SkipTests
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

    if ($SkipTests) {
        Write-Host "Skipping the test gate (-SkipTests)."
    }
    else {
        Assert-CommandExists "go"
        Write-Host "Running backend tests before deploy..."
        Push-Location (Join-Path $repoRoot "backend")
        try {
            go vet ./...
            if ($LASTEXITCODE -ne 0) { throw "go vet failed; aborting deploy. Re-run with -SkipTests to deploy anyway." }
            go test ./...
            if ($LASTEXITCODE -ne 0) { throw "Tests failed; aborting deploy. Re-run with -SkipTests to deploy anyway." }
        }
        finally {
            Pop-Location
        }
    }

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
    # ErrorActionPreference does not stop on a native command's exit code, so
    # without this a failed build carries on and recreates the container from
    # the previous image — which answers /health and looks like a good deploy.
    if ($LASTEXITCODE -ne 0) { throw "docker compose build failed; aborting deploy (the running container was left untouched)." }

    Write-Host "Starting containers..."
    docker compose up -d --force-recreate --remove-orphans
    if ($LASTEXITCODE -ne 0) { throw "docker compose up failed; the app may be down. Check 'docker compose ps' and 'docker compose logs api'." }

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
