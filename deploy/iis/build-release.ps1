<#
.SYNOPSIS
    Builds a release of the simulation platform ready to copy to a server.

.DESCRIPTION
    Compiles both binaries with the version stamped in, builds the web app, and
    assembles an install directory with everything the server needs and nothing
    it does not: the two executables, the built web files under "web", and a
    copy of the deploy scripts and templates.

    Run on a build machine with Go 1.26+ and Node 20+. The server itself needs
    neither.

.PARAMETER Version
    The version string, stamped into the binary and reported by /api/health and
    the -version flag. Defaults to "dev".

.PARAMETER Output
    The install directory to assemble. It is created if missing. Existing
    binaries and web files in it are overwritten; a config.yaml already there is
    left alone.

.PARAMETER SkipWeb
    Skip building the web app, for a binary-only rebuild during development.
#>
[CmdletBinding()]
param(
    [string]$Version = 'dev',
    [Parameter(Mandatory = $true)][string]$Output,
    [switch]$SkipWeb
)

$ErrorActionPreference = 'Stop'

# The repository root is two levels up from this script (deploy/iis).
$root = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
Push-Location $root
try {
    Write-Host "Building simserver and simrunner (version $Version)."
    $ldflags = "-X main.version=$Version"
    $env:CGO_ENABLED = '0'

    New-Item -ItemType Directory -Force -Path $Output | Out-Null

    & go build -trimpath -ldflags $ldflags -o (Join-Path $Output 'simserver.exe') ./cmd/simserver
    if ($LASTEXITCODE -ne 0) { throw 'simserver build failed.' }
    & go build -trimpath -ldflags $ldflags -o (Join-Path $Output 'simrunner.exe') ./cmd/simrunner
    if ($LASTEXITCODE -ne 0) { throw 'simrunner build failed.' }

    if (-not $SkipWeb) {
        Write-Host 'Building the web app.'
        Push-Location (Join-Path $root 'web')
        try {
            & npm ci
            if ($LASTEXITCODE -ne 0) { throw 'npm ci failed.' }
            & npm run build
            if ($LASTEXITCODE -ne 0) { throw 'web build failed.' }
        } finally {
            Pop-Location
        }

        $webOut = Join-Path $Output 'web'
        if (Test-Path $webOut) { Remove-Item $webOut -Recurse -Force }
        Copy-Item -Path (Join-Path $root 'web\dist') -Destination $webOut -Recurse
    }

    Write-Host 'Copying deploy scripts and the config template.'
    $deployOut = Join-Path $Output 'deploy\iis'
    New-Item -ItemType Directory -Force -Path $deployOut | Out-Null
    Copy-Item -Path (Join-Path $PSScriptRoot '*') -Destination $deployOut -Recurse -Force

    Write-Host ''
    Write-Host "Release assembled in $Output."
    Write-Host 'Copy that directory to the server and follow deploy/iis/README.md.'
} finally {
    Pop-Location
}
