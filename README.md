# Simulation Platform

A multi-user discrete-event simulation platform: build or upload models, run
scenarios, watch them back in 3D and on a georeferenced 2D plan, compare runs,
and export PDF reports.

The simulation core is [godes](https://github.com/agoussia/godes), a Go
discrete-event simulation library. The server is a single Go binary; the
frontend is a React application it serves. It is built to deploy behind IIS on
Windows Server with MySQL 8.

## Status

The project is being built in phases. Phase 1 is complete.

| Phase | Scope | State |
|---|---|---|
| 1 | Accounts, projects, roles, asset storage, plan georeferencing | Done |
| 2 | godes engine, model spec, run pipeline, artifact format | Next |
| 3 | React frontend, 3D viewer, synchronized 2D plan view | |
| 4 | Heatmaps, resource Gantt, spaghetti paths, KPI dashboard | |
| 5 | Scenario comparison | |
| 6 | PDF report export | |
| 7 | IIS deployment kit, admin-gated Go model upload | |

## Requirements

- Go 1.24 or newer
- MySQL 8.0 or newer
- Node.js 20 or newer (for the frontend, from phase 3)

## Running locally

Start a disposable MySQL that installs nothing and registers no service:

```bash
pwsh ./scripts/dev-mysql.ps1 start
```

Copy the example configuration and point it at that instance:

```bash
cp config.example.yaml config.yaml
```

Set `database.port` to `3399` and `database.password` to `dev-only-password`,
then start the server:

```bash
go run ./cmd/simserver
```

It listens on <http://127.0.0.1:8080>. Migrations run automatically at startup.
Until the frontend exists the root URL shows a placeholder page; the API is
live at `/api/health`.

The first account created on an empty instance becomes an administrator.

## Layout

```
cmd/simserver/     HTTP server: API, auth, static frontend, artifact streaming
cmd/simrunner/     One-shot worker that executes a single simulation run
internal/auth/     Passwords, sessions, project permissions
internal/db/       MySQL pool and embedded migrations
internal/store/    Every SQL statement in the server
internal/api/      Routing, middleware, handlers
internal/engine/   godes integration: model spec, interpreter, templates
internal/blobstore/ Content-addressed file storage
web/               React frontend
deploy/iis/        Windows Server hosting
samples/           Example data, including the original demo animation
```

## Configuration

`config.example.yaml` documents every setting. Anything secret belongs in the
environment instead of the file:

| Variable | Purpose |
|---|---|
| `SIM_CONFIG` | Path to the configuration file |
| `SIM_DB_PASSWORD` | Database password |
| `SIM_COOKIE_KEY` | 64 hex characters used to sign cookies |
| `SIM_BOOTSTRAP_ADMIN_EMAIL` | First administrator, created on an empty instance |
| `SIM_BOOTSTRAP_ADMIN_PASSWORD` | That administrator's password |

## Tests

```bash
go test ./...
```

## Security notes

Uploading Go simulation models compiles and runs user-supplied code on the
server. It is disabled by default, requires a per-account permission on top of
the server setting, and should only be enabled after reading the deployment
security notes.

## License

MIT. See [LICENSE](LICENSE).
