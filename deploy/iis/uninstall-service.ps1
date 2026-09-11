<#
.SYNOPSIS
    Stops and removes the simserver Windows service.

.DESCRIPTION
    Removes the service and its stored environment. It does not touch the
    install directory, the database, or the data directory, so a reinstall
    keeps every run and every account.

    Run from an elevated PowerShell prompt.
#>
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
$ServiceName = 'SimulationPlatform'

$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if (-not $svc) {
    Write-Host "The $ServiceName service is not installed."
    return
}

if ($svc.Status -ne 'Stopped') {
    Write-Host "Stopping $ServiceName."
    Stop-Service -Name $ServiceName -Force
}

Write-Host "Removing $ServiceName."
& sc.exe delete $ServiceName | Out-Null

Write-Host "Removed. The install directory, database and data directory are untouched."
