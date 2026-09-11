<#
.SYNOPSIS
    Puts IIS in front of the simserver service as a reverse proxy.

.DESCRIPTION
    IIS terminates TLS on 443 and forwards every request to simserver on
    loopback. This script enables the IIS features and the ARR proxy the
    web.config depends on, creates the site with an HTTPS binding, and allows
    the one server variable the reverse-proxy rule sets.

    The URL Rewrite and Application Request Routing modules are downloads, not
    Windows features. The script checks for them and stops with the download
    links if they are missing, unless you pass -DownloadModules to fetch and
    install them.

    Run from an elevated PowerShell prompt.

.PARAMETER SiteName
    The IIS site name. Defaults to "SimulationPlatform".

.PARAMETER InstallDir
    The install directory. A "site" subfolder holding only web.config is created
    here and used as the IIS physical path, so IIS never serves the binaries.

.PARAMETER Hostname
    The public host name, e.g. sim.example.com. Used for the HTTPS binding.

.PARAMETER CertThumbprint
    The thumbprint of an installed TLS certificate to bind. If omitted, a
    self-signed certificate for -Hostname is created, which browsers will warn
    on; replace it with a real one for anything but a trial.

.PARAMETER HttpPort
    Optional extra plain-HTTP binding that redirects to HTTPS is not created;
    this script binds HTTPS only. Terminate any HTTP-to-HTTPS redirect upstream
    or add a binding by hand if you need one.

.PARAMETER DownloadModules
    Download and install URL Rewrite and ARR if they are not present.
#>
[CmdletBinding()]
param(
    [string]$SiteName = 'SimulationPlatform',
    [string]$InstallDir = (Split-Path -Parent $PSScriptRoot),
    [Parameter(Mandatory = $true)][string]$Hostname,
    [string]$CertThumbprint,
    [switch]$DownloadModules
)

$ErrorActionPreference = 'Stop'
Import-Module WebAdministration -ErrorAction Stop

# --- IIS features -----------------------------------------------------------

Write-Host 'Ensuring the required IIS features are installed.'
$features = @('Web-Server', 'Web-WebSockets', 'Web-Http-Redirect', 'Web-Mgmt-Console')
foreach ($f in $features) {
    $state = Get-WindowsFeature -Name $f -ErrorAction SilentlyContinue
    if ($state -and -not $state.Installed) {
        Install-WindowsFeature -Name $f | Out-Null
    }
}

# --- URL Rewrite and ARR ----------------------------------------------------

function Test-Module($dllName) {
    Test-Path (Join-Path $env:SystemRoot "System32\inetsrv\$dllName")
}

$rewriteInstalled = Test-Module 'rewrite.dll'
$arrInstalled = Test-Module 'requestRouter.dll'

if (-not ($rewriteInstalled -and $arrInstalled)) {
    if (-not $DownloadModules) {
        Write-Warning 'URL Rewrite and/or Application Request Routing are missing.'
        Write-Host 'Install them, then re-run this script:'
        Write-Host '  URL Rewrite: https://www.iis.net/downloads/microsoft/url-rewrite'
        Write-Host '  ARR 3.0:     https://www.iis.net/downloads/microsoft/application-request-routing'
        Write-Host 'Or re-run with -DownloadModules to fetch and install them automatically.'
        throw 'Required IIS modules are not installed.'
    }

    $temp = Join-Path $env:TEMP 'sim-iis-modules'
    New-Item -ItemType Directory -Force -Path $temp | Out-Null

    if (-not $rewriteInstalled) {
        Write-Host 'Downloading and installing URL Rewrite.'
        $msi = Join-Path $temp 'rewrite.msi'
        Invoke-WebRequest -Uri 'https://download.microsoft.com/download/1/2/8/128E2E22-C1B9-44A4-BE2A-5859ED1D4592/rewrite_amd64_en-US.msi' -OutFile $msi
        Start-Process msiexec.exe -ArgumentList "/i `"$msi`" /quiet /norestart" -Wait
    }
    if (-not $arrInstalled) {
        Write-Host 'Downloading and installing Application Request Routing.'
        $msi = Join-Path $temp 'arr.msi'
        Invoke-WebRequest -Uri 'https://download.microsoft.com/download/E/9/8/E9849D6A-020E-47E4-9FD0-A023E99B54EB/requestRouter_amd64.msi' -OutFile $msi
        Start-Process msiexec.exe -ArgumentList "/i `"$msi`" /quiet /norestart" -Wait
    }
    Write-Host 'Modules installed. You may need to re-open PowerShell if the next step fails.'
}

# --- ARR proxy settings -----------------------------------------------------

Write-Host 'Enabling the ARR reverse proxy at the server level.'
Set-WebConfigurationProperty -PSPath 'MACHINE/WEBROOT/APPHOST' `
    -Filter 'system.webServer/proxy' -Name 'enabled' -Value 'True'

# Streaming: the run-progress endpoint is Server-Sent Events. Turning off the
# proxy's response buffering is what lets those events reach the browser as they
# happen rather than being held until the response ends.
Set-WebConfigurationProperty -PSPath 'MACHINE/WEBROOT/APPHOST' `
    -Filter 'system.webServer/proxy' -Name 'responseBufferLimit' -Value 0

# The reverse-proxy rule sets HTTP_X_FORWARDED_PROTO; a server variable must be
# on the allow-list before a rule may set it.
$allowed = Get-WebConfiguration -PSPath 'MACHINE/WEBROOT/APPHOST' `
    -Filter 'system.webServer/rewrite/allowedServerVariables/add' -ErrorAction SilentlyContinue
if (-not ($allowed | Where-Object { $_.name -eq 'HTTP_X_FORWARDED_PROTO' })) {
    Add-WebConfiguration -PSPath 'MACHINE/WEBROOT/APPHOST' `
        -Filter 'system.webServer/rewrite/allowedServerVariables' `
        -Value @{ name = 'HTTP_X_FORWARDED_PROTO' }
}

# --- The site ---------------------------------------------------------------

# The IIS physical path holds only web.config: every request is proxied, so IIS
# serves no files of its own, and keeping the binaries out of the web root means
# a misconfiguration cannot expose them.
$sitePath = Join-Path $InstallDir 'site'
New-Item -ItemType Directory -Force -Path $sitePath | Out-Null
Copy-Item -Path (Join-Path $PSScriptRoot 'web.config') -Destination $sitePath -Force

if (Get-Website -Name $SiteName -ErrorAction SilentlyContinue) {
    Write-Host "Removing the existing $SiteName site to recreate it."
    Remove-Website -Name $SiteName
}

# --- TLS certificate --------------------------------------------------------

if (-not $CertThumbprint) {
    Write-Warning "No -CertThumbprint given; creating a self-signed certificate for $Hostname."
    Write-Warning 'Browsers will warn on it. Replace it with a real certificate for production.'
    $cert = New-SelfSignedCertificate -DnsName $Hostname -CertStoreLocation 'Cert:\LocalMachine\My'
    $CertThumbprint = $cert.Thumbprint
}

Write-Host "Creating the $SiteName site bound to https://${Hostname}."
New-Website -Name $SiteName -PhysicalPath $sitePath -Port 443 -HostHeader $Hostname -Ssl | Out-Null

# Bind the certificate to the 443 endpoint.
$binding = Get-WebBinding -Name $SiteName -Protocol 'https'
$binding.AddSslCertificate($CertThumbprint, 'My')

Write-Host ''
Write-Host "IIS is set up. The site is at https://${Hostname}."
Write-Host 'Confirm the simserver service is running (install-service.ps1) and browse to the site.'
