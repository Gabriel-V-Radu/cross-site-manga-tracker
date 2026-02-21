[CmdletBinding()]
param(
    [int64]$ProfileID = 0,
    [int]$Limit = 0,
    [int]$ResolveTimeoutSeconds = 12,
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Resolve-Path (Join-Path $scriptDir "..")
$backendDir = Join-Path $repoRoot "backend"

if (-not (Test-Path $backendDir)) {
    throw "Backend folder not found at '$backendDir'."
}

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go executable not found in PATH."
}

$goArgs = @("run", "./cmd/backfill-related-titles")

if ($ProfileID -gt 0) {
    $goArgs += @("--profile-id", "$ProfileID")
}
if ($Limit -gt 0) {
    $goArgs += @("--limit", "$Limit")
}
if ($ResolveTimeoutSeconds -gt 0) {
    $goArgs += @("--resolve-timeout", "$($ResolveTimeoutSeconds)s")
}
if ($DryRun) {
    $goArgs += "--dry-run"
}

Push-Location $backendDir
try {
    & go @goArgs
}
finally {
    Pop-Location
}
