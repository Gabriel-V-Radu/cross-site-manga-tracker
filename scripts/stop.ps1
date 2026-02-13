[CmdletBinding()]
param()

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

    Write-Host "Stopping containers..."
    docker compose down --remove-orphans
    Write-Host "App stopped."
}
finally {
    Pop-Location
}
