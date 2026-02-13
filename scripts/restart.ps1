[CmdletBinding()]
param(
    [switch]$NoBrowser
)

$ErrorActionPreference = "Stop"

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$stopScript = Join-Path $scriptDir "stop.ps1"
$deployScript = Join-Path $scriptDir "deploy.ps1"

& $stopScript
$deployParams = @{}
if ($NoBrowser) {
    $deployParams["NoBrowser"] = $true
}

& $deployScript @deployParams
