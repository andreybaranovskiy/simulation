# Simulation Platform

A multi-user discrete-event simulation platform: build or upload models, run
scenarios, watch them back in 3D and on a georeferenced 2D plan, compare runs,
and export PDF reports.

The simulation core is [godes](https://github.com/agoussia/godes), a Go
discrete-event simulation library. The server is a single Go binary; the
frontend is a React application it serves. It is built to deploy behind IIS on
Windows Server with MySQL 8.

## Status

The project is being built in phases. Phases 1 to 4 are complete.

| Phase | Scope | State |
|---|---|---|
| 1 | Accounts, projects, roles, asset storage, plan georeferencing | Done |
| 2 | godes engine, model spec, run pipeline, artifact format | Done |
| 3 | React frontend, 3D viewer, synchronized 2D plan view | Done |
| 4 | Heatmaps, resource Gantt, spaghetti paths, KPI dashboard | Done |
| 5 | Scenario comparison | Next |
| 6 | PDF report export | |
| 7 | IIS deployment kit, admin-gated Go model upload | |

## Requirements

- Go 1.24 or newer
- MySQL 8.0 or newer
- Node.js 20 or newer (for the frontend, from phase 3)

## Building the web interface

```bash
cd web
npm install
npm run build
```

The Go server serves the result from `web/dist`. For frontend work,
`npm run dev` runs Vite on port 5173 and proxies the API to the Go server, so
the session cookie stays same-origin exactly as it is in production.

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

## Running a simulation without the server

The engine is usable on its own, which is the quickest way to try a model:

```bash
go build -o bin/simrunner ./cmd/simrunner
./bin/simrunner -list
./bin/simrunner -describe container_terminal
./bin/simrunner -template container_terminal -set gateLanes=6 -seed 42 -out ./run1
```

A run directory holds the event trace, the playback chunks the viewer streams,
and the aggregate files behind the dashboards. The same seed always produces a
byte-identical trace.

## What a run produces

```
manifest.json          index: time range, chunk levels, what else exists
frames/lod0/00000.bin  playback chunks; higher levels thin entities out
agg/kpi.json           headline numbers, also mirrored into MySQL
agg/series.json        time series, integrated rather than sampled
agg/gantt.json         per-resource busy, queued and down intervals
agg/paths.json         sampled journey traces
agg/heatmaps.json      traffic, dwell, occupancy and congestion grids
run.trace              raw events; an intermediate, never served to a browser
```

Playback chunks are self-contained windows of simulated time. Each opens with
every live entity's motion in progress, so seeking to hour six is one fetch
rather than replaying the first six hours. Motion is stored as spans the viewer
interpolates, which keeps size tied to how often entities change direction
rather than to a frame rate.

## Chart colours

Every chart colour came out of the palette validator, not out of taste. The
tokens live in `web/src/charts/tokens.ts` with the run on record. Re-run it
before changing any of them:

```bash
node scripts/validate_palette.js "#3987e5,#d95926,#199e70" --mode dark --surface "#0f1430" --pairs all
```

Colour encodes magnitude wherever it can: heatmaps, the Gantt and the meters
all share one sequential ramp, so "busy" looks the same everywhere. Categorical
hues are reserved for series identity and capped at three, which is the number
that validates when any two marks can end up side by side.

## Layout

```
cmd/simserver/     HTTP server: API, auth, static frontend, artifact streaming
cmd/simrunner/     One-shot worker that executes a single simulation run
internal/auth/     Passwords, sessions, project permissions
internal/db/       MySQL pool and embedded migrations
internal/store/    Every SQL statement in the server
internal/api/      Routing, middleware, handlers
internal/engine/   godes integration: model spec, interpreter, templates
internal/runstore/ Run artifacts: playback chunks, manifest, build pass
internal/analytics/ KPIs, heatmaps, Gantt, journey traces
internal/runner/   Run queue and process supervision
internal/importer/ Converts legacy animation files into run artifacts
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
