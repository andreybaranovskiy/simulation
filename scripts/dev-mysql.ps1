<#
.SYNOPSIS
    Runs a throwaway MySQL 8.4 for local development.

.DESCRIPTION
    Downloads the MySQL 8.4 LTS ZIP archive, initializes a data directory and
    starts mysqld as a plain process on a non-standard port. Nothing is
    installed and no Windows service is registered, so -Reset deletes the whole
    instance and leaves the machine exactly as it was.

    This is for development only. Production installs a real MySQL service:
    see deploy/iis/INSTALL.md.

.PARAMETER Action
    start   Download if needed, initialize if needed, then run mysqld.
    stop    Stop the running mysqld.
    reset   Stop and delete the data directory.
    status  Report whether the server is reachable.

.EXAMPLE
    .\scripts\dev-mysql.ps1 start
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('start', 'stop', 'reset', 'status')]
    [string]$Action = 'start',

    [int]$Port = 3399,
    [string]$Root = (Join-Path $env:LOCALAPPDATA 'simulation-devdb')
)

$ErrorActionPreference = 'Stop'

$Version  = '8.4.11'
$ZipUrl   = "https://cdn.mysql.com/Downloads/MySQL-8.4/mysql-$Version-winx64.zip"
$BaseDir  = Join-Path $Root "mysql-$Version-winx64"
$DataDir  = Join-Path $Root 'data'
$IniPath  = Join-Path $Root 'my.ini'
$LogPath  = Join-Path $Root 'mysqld.log'
$Mysqld   = Join-Path $BaseDir 'bin\mysqld.exe'
$MysqlCli = Join-Path $BaseDir 'bin\mysql.exe'

# Development credentials. These are deliberately not secret; the server only
# listens on loopback and the instance is disposable.
$DbName = 'simulation'
$DbUser = 'simulation'
$DbPass = 'dev-only-password'

function Test-ServerUp {
    try {
        $c = New-Object System.Net.Sockets.TcpClient
        $c.Connect('127.0.0.1', $Port)
        $c.Close()
        return $true
    } catch {
        return $false
    }
}

function Install-Archive {
    if (Test-Path $Mysqld) { return }

    New-Item -ItemType Directory -Force -Path $Root | Out-Null
    $zip = Join-Path $Root "mysql-$Version.zip"

    if (-not (Test-Path $zip)) {
        Write-Host "Downloading MySQL $Version (about 270 MB)..."
        # Progress rendering makes Invoke-WebRequest an order of magnitude
        # slower on a download this size.
        $previous = $ProgressPreference
        $ProgressPreference = 'SilentlyContinue'
        try { Invoke-WebRequest -Uri $ZipUrl -OutFile $zip }
        finally { $ProgressPreference = $previous }
    }

    Write-Host "Extracting..."
    Expand-Archive -Path $zip -DestinationPath $Root -Force
    Remove-Item $zip -ErrorAction SilentlyContinue
}

function Write-Ini {
    $basedirFwd = $BaseDir -replace '\\', '/'
    $datadirFwd = $DataDir -replace '\\', '/'

    # Forward slashes throughout: my.ini treats a backslash as an escape.
    @"
[mysqld]
basedir=$basedirFwd
datadir=$datadirFwd
port=$Port
bind-address=127.0.0.1
mysqlx=OFF
"@ | Set-Content -Path $IniPath -Encoding ascii
}

function Initialize-Data {
    if (Test-Path (Join-Path $DataDir 'mysql')) { return }

    Write-Host "Initializing the data directory..."
    if (Test-Path $DataDir) { Remove-Item -Recurse -Force $DataDir }
    & $Mysqld --defaults-file=$IniPath --initialize-insecure --console
    if ($LASTEXITCODE -ne 0) { throw "mysqld --initialize-insecure failed with exit code $LASTEXITCODE" }
}

function Start-Server {
    if (Test-ServerUp) {
        Write-Host "MySQL is already listening on port $Port."
        return
    }

    Install-Archive
    Write-Ini
    Initialize-Data

    Write-Host "Starting mysqld on port $Port..."
    Start-Process -FilePath $Mysqld `
        -ArgumentList "--defaults-file=$IniPath" `
        -RedirectStandardOutput $LogPath `
        -RedirectStandardError "$LogPath.err" `
        -WindowStyle Hidden

    for ($i = 0; $i -lt 40; $i++) {
        if (Test-ServerUp) { break }
        Start-Sleep -Milliseconds 500
    }
    if (-not (Test-ServerUp)) {
        Write-Host (Get-Content "$LogPath.err" -Tail 20 -ErrorAction SilentlyContinue)
        throw "MySQL did not start. See $LogPath.err"
    }

    Write-Host "Creating the $DbName database and user..."
    $sql = @"
CREATE DATABASE IF NOT EXISTS $DbName CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
CREATE USER IF NOT EXISTS '$DbUser'@'localhost' IDENTIFIED BY '$DbPass';
CREATE USER IF NOT EXISTS '$DbUser'@'127.0.0.1' IDENTIFIED BY '$DbPass';
GRANT ALL PRIVILEGES ON $DbName.* TO '$DbUser'@'localhost';
GRANT ALL PRIVILEGES ON $DbName.* TO '$DbUser'@'127.0.0.1';
FLUSH PRIVILEGES;
"@
    $sql | & $MysqlCli -h 127.0.0.1 -P $Port --protocol=tcp -u root --skip-password
    if ($LASTEXITCODE -ne 0) { throw "Could not create the development database." }

    Write-Host ""
    Write-Host "MySQL $Version is ready." -ForegroundColor Green
    Write-Host "  host 127.0.0.1   port $Port   database $DbName"
    Write-Host "  user $DbUser   password $DbPass"
    Write-Host ""
    Write-Host "Put this in config.yaml, then run: go run ./cmd/simserver"
}

function Stop-Server {
    Get-Process mysqld -ErrorAction SilentlyContinue |
        Where-Object { $_.Path -eq $Mysqld } |
        Stop-Process -Force
    Write-Host "Stopped."
}

switch ($Action) {
    'start'  { Start-Server }
    'stop'   { Stop-Server }
    'status' { if (Test-ServerUp) { Write-Host "Listening on port $Port." } else { Write-Host "Not running." } }
    'reset'  {
        Stop-Server
        Start-Sleep -Seconds 1
        if (Test-Path $DataDir) { Remove-Item -Recurse -Force $DataDir }
        Write-Host "Data directory removed. The next start will initialize a fresh instance."
    }
}
