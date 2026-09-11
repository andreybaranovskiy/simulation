<#
.SYNOPSIS
    Creates the database and application account for the simulation platform.

.DESCRIPTION
    Connects to MySQL as an administrator, creates the database and a dedicated
    least-privilege application account, and grants that account only the rights
    it needs on that one database. It then applies the schema by running
    simserver with -migrate, so the database is ready before the service starts.

    The application account is granted data and DDL rights on its own database
    only. It is never given server-wide privileges: the server needs to run
    migrations, which alter its own tables, but nothing outside them.

.PARAMETER MysqlExe
    Path to mysql.exe. Defaults to "mysql" on PATH.

.PARAMETER AdminUser
    A MySQL administrator, e.g. root. Prompted for a password.

.PARAMETER Database
    The database name. Defaults to "simulation".

.PARAMETER AppUser
    The application account name. Defaults to "simulation".

.PARAMETER AppPassword
    The application account password. This must match SIM_DB_PASSWORD given to
    install-service.ps1.

.PARAMETER MysqlHost
    MySQL host. Defaults to 127.0.0.1.

.PARAMETER Port
    MySQL port. Defaults to 3306.

.PARAMETER InstallDir
    The install directory, used to find simserver.exe for the migration step.
    Pass -SkipMigrate to only create the database and account.

.PARAMETER SkipMigrate
    Create the database and account but do not apply the schema.
#>
[CmdletBinding()]
param(
    [string]$MysqlExe = 'mysql',
    [string]$AdminUser = 'root',
    [string]$Database = 'simulation',
    [string]$AppUser = 'simulation',
    [Parameter(Mandatory = $true)][string]$AppPassword,
    [string]$MysqlHost = '127.0.0.1',
    [int]$Port = 3306,
    [string]$InstallDir = (Split-Path -Parent $PSScriptRoot),
    [switch]$SkipMigrate
)

$ErrorActionPreference = 'Stop'

$adminCred = Get-Credential -UserName $AdminUser -Message "MySQL password for $AdminUser"
$adminPassword = $adminCred.GetNetworkCredential().Password

# Escape single quotes so a password with one in it cannot break out of the
# statement or, worse, alter it.
$escapedAppPassword = $AppPassword -replace "'", "''"

# Built from a literal template so nothing here is subject to PowerShell's
# backtick escaping: the MySQL identifier quotes around the database name are
# real backticks, and the values are substituted in afterwards. utf8mb4 so the
# schema's text columns hold any character; both loopback and localhost accounts
# because MySQL treats them as different hosts.
$sqlTemplate = @'
CREATE DATABASE IF NOT EXISTS `__DB__` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER IF NOT EXISTS '__USER__'@'127.0.0.1' IDENTIFIED BY '__PWD__';
CREATE USER IF NOT EXISTS '__USER__'@'localhost' IDENTIFIED BY '__PWD__';
ALTER USER '__USER__'@'127.0.0.1' IDENTIFIED BY '__PWD__';
ALTER USER '__USER__'@'localhost' IDENTIFIED BY '__PWD__';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX, DROP, REFERENCES ON `__DB__`.* TO '__USER__'@'127.0.0.1';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE, ALTER, INDEX, DROP, REFERENCES ON `__DB__`.* TO '__USER__'@'localhost';
FLUSH PRIVILEGES;
'@
$sql = $sqlTemplate.Replace('__DB__', $Database).Replace('__USER__', $AppUser).Replace('__PWD__', $escapedAppPassword)

Write-Host "Creating database '$Database' and account '$AppUser'."
$sql | & $MysqlExe --host=$MysqlHost --port=$Port --user=$AdminUser "--password=$adminPassword" --protocol=tcp
if ($LASTEXITCODE -ne 0) { throw "MySQL returned exit code $LASTEXITCODE." }

if ($SkipMigrate) {
    Write-Host 'Database and account ready. Schema not applied (-SkipMigrate).'
    return
}

$exe = Join-Path $InstallDir 'simserver.exe'
$config = Join-Path $InstallDir 'config.yaml'
if (-not (Test-Path $exe))    { throw "simserver.exe not found in $InstallDir; pass -SkipMigrate or fix -InstallDir." }
if (-not (Test-Path $config)) { throw "config.yaml not found in $InstallDir." }

Write-Host 'Applying the schema.'
# The migration runs as the application account, using the same config the
# service will, so a permission gap shows up here rather than at first start.
$env:SIM_DB_PASSWORD = $AppPassword
& $exe -config $config -migrate
if ($LASTEXITCODE -ne 0) { throw "Migration failed with exit code $LASTEXITCODE." }
Remove-Item Env:\SIM_DB_PASSWORD

Write-Host ''
Write-Host 'Database provisioned and schema applied.'
Write-Host "Use this same password as SIM_DB_PASSWORD when you install the service."
