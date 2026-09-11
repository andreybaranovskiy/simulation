<#
.SYNOPSIS
    Installs simserver as a Windows service.

.DESCRIPTION
    Registers the simulation platform server as an automatically started
    service, plants its secrets in the service's own environment so they never
    sit in a file, and configures it to restart if it ever exits unexpectedly.

    Secrets are passed as parameters and written to the service's registry
    environment (REG_MULTI_SZ), which the service control manager hands to the
    process at start. They are never written to config.yaml.

    Run from an elevated PowerShell prompt.

.PARAMETER InstallDir
    The directory holding simserver.exe, simrunner.exe, config.yaml and the web
    folder. Defaults to the parent of this script's own folder.

.PARAMETER DbPassword
    The MySQL password for the application account. Required.

.PARAMETER CookieKey
    64 hex characters (32 bytes) used to sign session cookies. Generate one with
    the -GenerateCookieKey switch and keep it stable: changing it signs every
    existing session out.

.PARAMETER BootstrapAdminPassword
    Optional. The first administrator's password, used only while the users
    table is empty. At least 12 characters.

.PARAMETER ServiceAccount
    Optional. A Windows account to run the service as, e.g. a low-privilege
    "sim-svc" account. Defaults to LocalSystem; a dedicated account is better.

.PARAMETER GenerateCookieKey
    Print a fresh cookie key and exit, so you can capture it before installing.

.EXAMPLE
    .\install-service.ps1 -GenerateCookieKey

.EXAMPLE
    .\install-service.ps1 -InstallDir C:\simulation -DbPassword 'secret' `
        -CookieKey 'ab12...(64 hex)...' -BootstrapAdminPassword 'change-me-now'
#>
[CmdletBinding()]
param(
    [string]$InstallDir = (Split-Path -Parent $PSScriptRoot),
    [string]$DbPassword,
    [string]$CookieKey,
    [string]$BootstrapAdminPassword,
    [string]$ServiceAccount,
    [switch]$GenerateCookieKey
)

$ErrorActionPreference = 'Stop'
$ServiceName = 'SimulationPlatform'
$DisplayName = 'Simulation Platform'

if ($GenerateCookieKey) {
    $bytes = New-Object 'System.Byte[]' 32
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    ($bytes | ForEach-Object { $_.ToString('x2') }) -join ''
    return
}

if (-not $DbPassword) { throw 'DbPassword is required. Pass the MySQL application password.' }
if (-not $CookieKey)  { throw 'CookieKey is required. Generate one with -GenerateCookieKey.' }
if ($CookieKey -notmatch '^[0-9a-fA-F]{64}$') {
    throw 'CookieKey must be exactly 64 hexadecimal characters (32 bytes).'
}

$exe = Join-Path $InstallDir 'simserver.exe'
$config = Join-Path $InstallDir 'config.yaml'
if (-not (Test-Path $exe))    { throw "simserver.exe not found in $InstallDir." }
if (-not (Test-Path $config)) { throw "config.yaml not found in $InstallDir. Copy config.production.yaml there and edit it first." }

# Stop and remove any previous install so this is repeatable.
$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing) {
    Write-Host "Removing the existing $ServiceName service."
    if ($existing.Status -ne 'Stopped') { Stop-Service -Name $ServiceName -Force }
    # sc.exe delete is the reliable removal on Windows PowerShell 5.1.
    & sc.exe delete $ServiceName | Out-Null
    Start-Sleep -Seconds 2
}

$binPath = '"{0}" -config "{1}"' -f $exe, $config

$newServiceArgs = @{
    Name           = $ServiceName
    BinaryPathName = $binPath
    DisplayName    = $DisplayName
    Description    = 'Discrete-event simulation platform server (reverse-proxied by IIS).'
    StartupType    = 'Automatic'
}
if ($ServiceAccount) {
    $cred = Get-Credential -UserName $ServiceAccount -Message "Password for the service account $ServiceAccount"
    $newServiceArgs['Credential'] = $cred
}

Write-Host "Registering the $ServiceName service."
New-Service @newServiceArgs | Out-Null

# The service reads its secrets from its own environment. Write them to the
# service key as a multi-string; the SCM passes them to the process.
$envEntries = @(
    "SIM_DB_PASSWORD=$DbPassword",
    "SIM_COOKIE_KEY=$CookieKey"
)
if ($BootstrapAdminPassword) {
    $envEntries += "SIM_BOOTSTRAP_ADMIN_PASSWORD=$BootstrapAdminPassword"
}
$serviceKey = "HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName"
New-ItemProperty -Path $serviceKey -Name 'Environment' -PropertyType MultiString `
    -Value $envEntries -Force | Out-Null

# Restart automatically after a crash: two quick retries, then every minute,
# and reset the counter after a day of health.
& sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/10000/restart/60000 | Out-Null

Write-Host "Starting the service."
Start-Service -Name $ServiceName

$svc = Get-Service -Name $ServiceName
Write-Host ""
Write-Host "Installed. $DisplayName is $($svc.Status)."
Write-Host "Logs go where log.file points in config.yaml."
Write-Host "Next: run setup-iis.ps1 to put IIS in front of it."
