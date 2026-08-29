[CmdletBinding()]
param(
    [switch]$NoPull,
    [switch]$NoCache,
    [switch]$NoBrowser,
    [switch]$SkipTests
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Resolve-Path (Join-Path $scriptDir "..")
$stopScript = Join-Path $scriptDir "stop.ps1"
$deployScript = Join-Path $scriptDir "deploy.ps1"

# The test gate runs BEFORE the container is stopped. Stopping first and then
# failing the gate is what turns a restart into an outage that stays an outage:
# the app is already down when the throw happens, and re-running this script
# fails the same way. -SkipTests is the way back up when the gate is the problem.
if ($SkipTests) {
    Write-Host "Skipping the test gate (-SkipTests)."
}
else {
    if (-not (Get-Command "go" -ErrorAction SilentlyContinue)) {
        throw "Required command 'go' not found in PATH. Re-run with -SkipTests to restart without the test gate."
    }
    Write-Host "Running backend tests before restarting..."
    Push-Location (Join-Path $repoRoot "backend")
    try {
        go vet ./...
        if ($LASTEXITCODE -ne 0) { throw "go vet failed; the app was left running. Re-run with -SkipTests to restart anyway." }
        go test ./...
        if ($LASTEXITCODE -ne 0) { throw "Tests failed; the app was left running. Re-run with -SkipTests to restart anyway." }
    }
    finally {
        Pop-Location
    }
}

& $stopScript

# Already gated above, so the deploy must not run it a second time.
$deployParams = @{ SkipTests = $true }
if ($NoPull) {
    $deployParams["NoPull"] = $true
}
if ($NoCache) {
    $deployParams["NoCache"] = $true
}
if ($NoBrowser) {
    $deployParams["NoBrowser"] = $true
}

& $deployScript @deployParams
