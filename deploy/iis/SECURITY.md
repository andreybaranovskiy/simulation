# Security notes for the uploaded-Go-models feature

Read this before turning on `plugins.enabled`. The feature lets a user upload a
Go program that the server compiles and runs. That is arbitrary code execution
on the server, by design, and no sandbox on Windows reduces the risk to zero.
This document says what the feature does to contain it, what it does not, and
how to decide whether to enable it.

## What the feature is

An uploaded model is a plain Go program that reads the scenario's parameters on
standard input and writes a model definition — the same declarative model the
built-in templates produce — on standard output. The server:

1. compiles the program, offline and stdlib-only;
2. runs it to produce a model definition;
3. parses and validates that definition and runs it with the ordinary engine.

The important property is that **the untrusted program only ever produces data.**
Nothing it writes reaches the trace writer, the artifact pipeline, or the
database except as a model definition the trusted side has parsed and checked.
The uploaded code never runs inside the simulation.

## What contains it

- **It is off by default.** `plugins.enabled` is false, and the server will not
  compile or run an uploaded model until an operator turns it on.
- **It is administrators only, and then only by grant.** Even with the feature
  on, a user can upload a Go model only if they are an administrator or an
  administrator has given their account the permission. A normal member cannot.
- **Compilation is offline and stdlib-only.** The build runs with the module
  proxy off, so an uploaded program cannot pull a dependency, and with cgo
  disabled, so it cannot invoke a C compiler. An import outside the standard
  library fails to compile rather than fetching code.
- **Memory and time are capped.** Both the compile and the run happen under a
  memory limit (a Windows job object) and a timeout, in their own process group.
  A runaway or hostile program is bounded and killed rather than taking the
  server down with it. The job object is kill-on-close, so if the server dies
  the operating system tears the plugin down with it.
- **The program is handed no environment.** The generate step runs with an empty
  environment, so a secret the service holds in an environment variable — the
  database password, the cookie key — is not visible to uploaded code.
- **Output is size-capped.** A program that streams endless output is cut off
  rather than exhausting the server's memory.

## What it does not contain

- **Network access is not blocked.** Windows does not offer a simple per-process
  network jail. A compiled plugin can open outbound connections while it runs.
  If that matters, run the service as a dedicated account and add an outbound
  firewall rule that blocks it:

  ```powershell
  New-NetFirewallRule -DisplayName 'Block sim-svc outbound' -Direction Outbound `
      -Action Block -Owner (Get-Process ...) # or scope by the service account's SID
  ```

  Scoping a rule to the service account is the practical approach; test it
  against a real run before relying on it.
- **The filesystem is not jailed.** The plugin runs with the service account's
  rights. This is the main reason to run the service as a dedicated
  low-privilege account rather than LocalSystem: a plugin can do whatever that
  account can do.
- **CPU is not capped.** The timeout bounds wall-clock time, not the cores a
  program can spin. `engine.max_concurrent_runs` bounds how many run at once.

## How to decide

Enable it only when **all** of these hold:

- You trust the administrators who can upload, and the accounts you grant the
  permission to, roughly as much as you trust the operator.
- The server runs as a dedicated low-privilege account, not LocalSystem.
- You have added an outbound firewall rule for that account, or you accept that
  a plugin can reach the network.
- The machine is one you can treat as compromised if an upload turns out to be
  hostile: nothing else of value shares it, and it can be rebuilt.

If you cannot meet those, leave the feature off. The built-in templates and the
declarative model cover the same ground for models that do not need to be
computed in code, and they run no uploaded code at all.

## Turning it on

In `config.yaml`:

```yaml
plugins:
  enabled: true
  go_toolchain: "go"        # must be on the service account's PATH
  build_timeout: 2m
  build_user: ""            # advisory; see above about a dedicated account
```

The Go toolchain must be installed on the server and on the service account's
PATH. Restart the service. The startup log records `go_upload=true` when the
feature came up. Grant upload rights to specific accounts from the admin area.
