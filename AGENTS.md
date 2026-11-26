# tfsaz

Build a standalone Go CLI for macOS, Windows and Linux that lets an unmodified T3 Code use Azure DevOps Server. Ship it under its own name. Treat T3's invocation of `az` as the compatibility contract. The initial backend is the on-premises REST API directly. Azure cloud sign-in and extension installation are outside the product scope.

## Hard rules for credentials

- Never put a PAT in command arguments, logs, fixtures or configuration files. PATs live only in the OS keyring.
- Send a PAT only to its configured collection. Map an Azure-style alias to the real collection before sending credentials.
- Redirects on authenticated requests fail closed.
- A failed credential in one collection must never select another collection's identity.

Details and the remaining routing rules: [Architecture](docs/internals/architecture.md).

## Ways to hurt yourself

1. **Using the real configuration.** Without `TFSAZ_CONFIG`, tfsaz reads the developer's config in the OS user config directory, plus `<config>.repo-setups.json` and `<config>.browser-route` beside it. In tests and manual runs, set `TFSAZ_CONFIG` to an absolute path in a temp directory.
2. **Writing the real keyring.** `init`, `devops login` and `profile logout` write or delete OS keyring entries (`tfsaz` or `tfsaz/v2/<hash>`). Tests inject the in-memory store through `app{store: ...}`; never use `systemStore` in a test. Only `TestStockT3Adapter` touches the real keyring, with a random entry it removes. Trap: managed-SSH `repo setup` on an HTTPS origin without `--ssh-url` reads the system keyring (`internal/az/setup_route.go`).
3. **Running `route setup` or `route restore` on this machine.** They edit the system hosts file (no override exists), trust stores (login keychain, Windows user Root store, NSS databases), launchd LaunchAgents, systemd units with an nftables table via sudo, or a scheduled task, and write `~/.tfsaz/route-recovery`. Tests replace `runRouteSystemCommand` and set a temp `HOME`, `USERPROFILE` and `TFSAZ_CONFIG`. Keep it that way.
4. **Rewriting real checkouts.** `repo setup` and `repo restore` change `remote.origin.*` and `core.sshCommand` of the target clone and register it. Run them only on throwaway clones in temp directories, with `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_NOSYSTEM` isolating Git. Never point them at this worktree or the developer's clones.
5. **Live gates.** `TFSAZ_LIVE_LAUNCHD` (`1` or `443`), `TFSAZ_LIVE_TASK_SCHEDULER` and `TFSAZ_T3_ROOT` change real OS state. Set them only on a disposable test session and only when asked. The live testbed is shared: use it only on the maintainer's instruction.

## Tests and fixtures

- Strip identities, internal URLs and secrets from fixtures.
- Keep offline fixture results separate from live results.
- When a live test needs a PAT, request it through T3's secret input. Use a disposable credential for revocation tests.

## Verify

```sh
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...   # what CI runs; Windows x64 needs MinGW-w64
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

The last one is opt-in and needs a T3 checkout with dependencies installed. Details: [Development](docs/operations/development.md).

## Docs map

| Audience | Where |
| --- | --- |
| Everyone | [README.md](README.md): what tfsaz is, quickstart, docs index |
| Operators and script authors | `docs/user/`: install, configure, checkout setup, diagnose, routing, browser routing, desktop link handler, Azure CLI compatibility |
| Maintainers and agents | `docs/internals/`: [architecture](docs/internals/architecture.md), [T3 compatibility](docs/internals/t3-compatibility.md) (adapter files and feature coverage) |
| Maintainers | `docs/operations/development.md`: test runbook. `docs/verification/`: dated live records |

When implementing a command, read the T3 adapter and its tests first; [T3 compatibility](docs/internals/t3-compatibility.md) names the files.

## Doc rules

- `docs/user/` tells operators how to do a task. No dated test logs there; link to the record instead.
- Dated live results go to `docs/verification/`, kept in full.
- Design rules and their reasons go to `docs/internals/`; runbooks to `docs/operations/`.
- Code and docs in English. No em dashes in markdown.
- Some CLI help and error texts point at doc paths; when moving a doc, grep `*.go` for its path.
