# Deploying on Windows Server with IIS and MySQL

This is the operator's guide to installing the simulation platform on a Windows
Server behind IIS, with MySQL for storage. The server is a single Go binary
that runs as a Windows service on loopback; IIS terminates TLS and reverse
-proxies to it.

```
Browser ──HTTPS──▶ IIS (443) ──HTTP──▶ simserver service (127.0.0.1:8080) ──▶ MySQL
                     reverse proxy         API · artifacts · SPA · PDF export
```

## What you need

- Windows Server 2019 or newer. The PDF export uses the Edge that ships with it,
  so nothing extra is needed for reports.
- MySQL 8.0 or newer, reachable from the server (localhost is fine).
- IIS with the **URL Rewrite** and **Application Request Routing** modules. The
  setup script installs them for you with `-DownloadModules`, or links you to
  the downloads.
- A TLS certificate for the site's host name. The setup script can make a
  self-signed one to get started, which browsers will warn on.

The Go toolchain is needed only to **build** the release, not to run it, and
only on the machine where you build. The one exception is the optional uploaded
-Go-models feature, which needs the toolchain on the server; see SECURITY.md.

## Build the release

On a machine with Go 1.26+ and Node 20+:

```powershell
.\deploy\iis\build-release.ps1 -Version 1.0.0 -Output C:\simulation
```

That builds `simserver.exe` and `simrunner.exe`, builds the web app, and
assembles an install directory with the binaries, the `web` folder, and a copy
of the deploy scripts. Copy that directory to the server (for example to
`C:\simulation`).

## Install on the server

Run each step from an **elevated** PowerShell prompt, in the install directory.

**1. Configuration.** Copy the template and edit the values marked `EDIT`:

```powershell
Copy-Item deploy\iis\config.production.yaml config.yaml
notepad config.yaml
```

Set `base_url`, `data_dir`, `log.file`, and the bootstrap admin email. Leave the
passwords blank: they come from the environment.

**2. Database.** Create the database and a least-privilege application account,
and apply the schema:

```powershell
.\deploy\iis\provision-mysql.ps1 -AppPassword '<db-password>'
```

It prompts for your MySQL administrator password, creates the `simulation`
database and account, grants that account rights on that database only, and runs
the migrations.

**3. The service.** Generate a cookie key, then install the service with the
secrets. They are stored in the service's own environment, never in a file:

```powershell
$key = .\deploy\iis\install-service.ps1 -GenerateCookieKey
.\deploy\iis\install-service.ps1 -DbPassword '<db-password>' -CookieKey $key `
    -BootstrapAdminPassword '<first-admin-password>'
```

The service starts automatically and restarts itself if it ever crashes. Its
logs go where `log.file` points.

**4. IIS.** Put IIS in front of it:

```powershell
.\deploy\iis\setup-iis.ps1 -Hostname sim.example.com -DownloadModules
```

Pass `-CertThumbprint` to bind a real certificate; without it the script makes a
self-signed one. Browse to `https://sim.example.com` and sign in as the
bootstrap admin.

## Running as a dedicated account

By default the service runs as LocalSystem. A dedicated low-privilege account is
better: create one, give it write access to the `data_dir` and the log folder,
and pass it to the installer:

```powershell
.\deploy\iis\install-service.ps1 -ServiceAccount .\sim-svc -DbPassword ... -CookieKey ...
```

## Upgrading

1. Build the new release and copy `simserver.exe`, `simrunner.exe` and the `web`
   folder over the old ones.
2. `Restart-Service SimulationPlatform`.

Migrations run at startup when `auto_migrate` is on, so a schema change is
applied on the first restart. Migrations are additive and checksummed; the
server refuses to start if a previously applied migration has changed.

## Backups

Two things hold state:

- **MySQL** — accounts, projects, scenarios, run metadata, KPIs, reports. Back
  it up with `mysqldump` or your usual database backup.
- **The data directory** — uploaded assets, run artifacts and cached compiled
  plugins. Back up the folder `data_dir` points at.

A run's bulk data lives on disk in the data directory, not in MySQL, so both
need to be captured for a complete backup.

## Operating the service

```powershell
Get-Service SimulationPlatform             # status
Restart-Service SimulationPlatform         # restart
Get-Content C:\simulation\logs\simserver.log -Tail 50 -Wait   # follow the log
```

To remove the service (leaving the database and data intact):

```powershell
.\deploy\iis\uninstall-service.ps1
```

## Troubleshooting

- **502 from IIS.** The service is not running or not listening on the address
  in `server.addr`. Check `Get-Service` and the log.
- **The run-progress bar never moves.** ARR is buffering the event stream. Re
  -run `setup-iis.ps1`; it sets the proxy's `responseBufferLimit` to 0, which is
  what lets the stream through.
- **Links come back as http on an https site.** `server.trust_proxy_headers`
  must be true and IIS must forward `X-Forwarded-Proto`; the setup script allows
  that server variable and the web.config sets it.
- **PDF export says no browser was found.** Install Microsoft Edge, or set
  `report.browser_path` to a Chromium-family executable.

## The uploaded-Go-models feature

It is off by default and should stay off unless you have read **SECURITY.md** and
accepted what it describes. It compiles and runs code that users upload.
