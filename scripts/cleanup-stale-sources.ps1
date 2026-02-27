[CmdletBinding()]
param(
    [switch]$Apply
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

$goArgs = @("run", "./cmd/cleanup-stale-sources")
if ($Apply) {
    $goArgs += "--apply"
}

Push-Location $backendDir
try {
    & go @goArgs
}
finally {
    Pop-Location
}
