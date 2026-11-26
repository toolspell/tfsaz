# Development

## Setup

- Go at the version in `go.mod` (1.26 or newer).
- Git and OpenSSH for the transport tests, Node for the TLS probe, and GIO on Linux for desktop dispatch.
- On Windows use Git for Windows with its bundled shell.

## Native suite

Run the checks in this order:

1. `gofmt -l .` must print nothing. It lists files that need `gofmt -w`.
2. `go vet ./...`.
3. While iterating, focused tests: `go test -count=1 ./internal/az -run 'TestName'`. `-run` takes a regular expression, so `'TestA|TestB'` runs both.
4. Once at the end, the full suite: `go test -count=1 ./...`. The `internal/az` package takes about 35 s.

Tests use temporary Git repositories, loopback HTTP/TLS servers and a test credential store. The rules for writing them are in [Testing](../internals/testing.md).

The suite also checks links: `TestDocLinks` in `docs_links_test.go` verifies relative links and heading anchors in README.md, AGENTS.md and `docs/`, plus every `https://github.com/toolspell/tfsaz` URL in Go code and docs.

`TestPinnedActions` in `repo_checks_test.go` requires every `uses:` in `.github/workflows/` and `.github/actions/` to name a full 40-hex commit SHA followed by a `# vX` version comment. Local `./` actions are exempt; `docker://` images need a `sha256` digest.

`TestNoEmDashInMarkdown` in `repo_checks_test.go` rejects an em dash (U+2014) in any markdown file Git tracks or would track, and reports `file:line`.

## Isolate manual runs

A manual run of the built binary uses the real configuration, keyring and, for some commands, system files. Before trying a command by hand:

- Set `TFSAZ_CONFIG` to an absolute path in a temporary directory.
- Run `repo setup` and `repo restore` only on throwaway clones, with `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_NOSYSTEM` set to isolate Git.
- Run `route setup` and `route restore` only on a disposable test machine or session; they edit the hosts file, trust stores and services.

[AGENTS.md](../../AGENTS.md#ways-to-hurt-yourself) lists every trap.

## Race detector and CI

The [native test workflow](../../.github/workflows/test.yml) runs `go vet` and this suite on macOS, Windows and Ubuntu, on pushes to `main`, on pull requests and on manual dispatch. All three jobs enable Go's race detector. To run it locally, use `go test -race -count=1 ./...`. Windows x64 needs a compatible MinGW-w64 C compiler on PATH. [Go race detector requirements](https://go.dev/doc/articles/race_detector#Requirements)

## Stock T3 adapter test

The opt-in integration test builds the actual executable and runs T3's existing Azure adapter against it. It requires Git, Node with TypeScript support (tested with Node 26), a T3 source tree with its lockfile dependencies installed, and a usable OS keyring. It creates a random keyring entry for its loopback server and removes the entry at the end.

```sh
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

In PowerShell, set `$env:TFSAZ_T3_ROOT = 'C:\path\to\t3code'`, then run the same `go test` command. Use a logon with a loaded user profile and an accessible Credential Manager. For unattended Linux runs, start an unlocked Secret Service collection on the test session's D-Bus. The fixture uses its own random keyring entry.

Users do not need a T3 source checkout; only this test does.

## Opt-in live system tests

These tests change real OS state and skip unless their variable is set. Run them only on a disposable test machine or session.

| Variable | What it does |
| --- | --- |
| `TFSAZ_LIVE_LAUNCHD=1` (macOS) | Loads the generated LaunchAgents into the real GUI session on a free high port, from a temporary directory, then unloads them. `443` uses the production port. It changes no trust, hosts or LaunchAgents directory. |
| `TFSAZ_LIVE_TASK_SCHEDULER=1` (Windows) | Registers a real scheduled task in an interactive test session and removes it. |

## Test data and records

- Rules for fixtures, live gates and test PATs: [Testing](../internals/testing.md#fixtures-and-live-tests).
- Record dated live results in [`docs/verification/`](../verification/), not in user guides. Current records: [offline fixtures and native suites](../verification/offline-fixtures.md), [live T3 results](../verification/live-t3-2026-11.md) and [browser routing](../verification/browser-routing.md).
