# tfsaz

tfsaz is a standalone Go CLI for macOS, Windows and Linux that lets an unmodified T3 Code use on-premises Azure DevOps Server. It ships under its own name and runs as `az` on the T3 backend's PATH. T3's invocation of `az` is the compatibility contract. The backend is the server's REST API, called directly. Azure cloud sign-in and extension installation are out of scope.

Stock T3 recognizes only Azure DevOps Services remotes and scopes every `az` call to a cloud organization. tfsaz therefore gives checkouts Azure-style aliases and maps them back to the real collection; do not "simplify" that to native URLs. Details: [why stock T3 cannot reach Azure DevOps Server](docs/internals/t3-compatibility.md#why-stock-t3-cannot-reach-azure-devops-server).

## Current phase: no code changes

While the repo structure work is ongoing, change no code, tests, workflows or tooling, and reorganize nothing unless the maintainer explicitly asks for it. This holds even when existing code or docs break the rules below (for example the on-disk `alias-key.pem`). Report such findings to the maintainer instead of fixing them. This rule is removed when the repo setup is done ([backlog](docs/backlog.md)).

## Hard rules

### Credentials

- Every credential (PATs, tokens) lives only in the OS secret store through go-keyring: macOS Keychain, Windows Credential Manager, Linux Secret Service. Never in files, configuration, command arguments, logs or fixtures. Add no new on-disk secrets.
- One exception exists today: the browser-redirect TLS leaf private key `alias-key.pem` in `<config>.browser-route` (owner-only, mode 600). It is tracked in the [backlog](docs/backlog.md).
- Send a PAT only to its configured collection. Map an Azure-style alias to the real collection before sending credentials.
- Redirects on authenticated requests fail closed.
- A failed credential in one collection must never select another collection's identity.

Details and the remaining routing rules: [Architecture](docs/internals/architecture.md).

### Platform parity

macOS, Linux and Windows are fully supported at feature parity. Platform-specific implementations are fine (launchd, systemd, Task Scheduler). A feature or doc step that misses a platform is a gap to report, not a design choice.

## Glossary

- **collection**: one Azure DevOps Server project collection, addressed by its real HTTPS URL including port and virtual directory.
- **profile**: a configured collection with a stable ID, its Azure-style alias, optional extra aliases and CA, and a PAT in the keyring. Profiles are ordered; discovery uses that order.
- **Azure-style alias**: a `*.visualstudio.com` organization URL that T3 recognizes and tfsaz maps to a profile's real collection.
- **synthetic URL**: an Azure-style URL on a generated alias host `tfsaz-<24 hex>.visualstudio.com`, one per project and repository. T3 sees it; nothing on the network serves it unless routing is set up.
- **native URL**: the real HTTPS or SSH URL of a repository on the server.
- **shim**: the `az` and `git-remote-tfsaz` names that `shim install` creates for the tfsaz binary. `main.go` picks the behavior by program name.
- **managed checkout**: a clone after `repo setup`: the synthetic origin for T3, the native target in its common Git config, registered in `<config>.repo-setups.json`.
- **routing**: the ways a synthetic URL reaches the real collection: the loopback redirector (`route setup`), the desktop link handler, the Chromium extension, `route run` for Git, and PAC/hosts refusal.

## Ways to hurt yourself

1. **Using the real configuration.** Without `TFSAZ_CONFIG`, tfsaz reads the developer's config in the OS user config directory, plus `<config>.repo-setups.json` and `<config>.browser-route` beside it. In tests and manual runs, set `TFSAZ_CONFIG` to an absolute path in a temp directory.
2. **Writing the real keyring.** `init`, `devops login` and `profile logout` write or delete OS keyring entries (`tfsaz` or `tfsaz/v2/<hash>`). Tests inject the in-memory store through `app{store: ...}`; never use `systemStore` in a test. Only `TestStockT3Adapter` touches the real keyring, with a random entry it removes. Trap: managed-SSH `repo setup` on an HTTPS origin without `--ssh-url` reads the system keyring (`internal/az/setup_route.go`, in the [backlog](docs/backlog.md)).
3. **Running `route setup` or `route restore` on this machine.** They edit the system hosts file (no override exists), trust stores (login keychain, Windows user Root store, NSS databases), launchd LaunchAgents, systemd units with an nftables table via sudo, or a scheduled task, and write `~/.tfsaz/route-recovery`. Tests replace `runRouteSystemCommand` and set a temp `HOME`, `USERPROFILE` and `TFSAZ_CONFIG`. Keep it that way.
4. **Rewriting real checkouts.** `repo setup` and `repo restore` change `remote.origin.*` and `core.sshCommand` of the target clone and register it. Run them only on throwaway clones in temp directories, with `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_NOSYSTEM` isolating Git. Never point them at this worktree or the developer's clones.
5. **Live gates.** `TFSAZ_LIVE_LAUNCHD` (`1` or `443`), `TFSAZ_LIVE_TASK_SCHEDULER` and `TFSAZ_T3_ROOT` change real OS state. Set them only on a disposable test session and only when asked. The live testbed is shared: use it only on the maintainer's instruction.

## Where code lives

- `main.go`: picks `az`, `git-remote-tfsaz` or the `route run` HTTPS helper by program name.
- `internal/az`: everything else, one package. File prefixes group it:
  - `cli.go`, `help.go`: dispatcher and offline help.
  - `config.go`, `profiles.go`, `credentials.go`, `client.go`, `scope.go`: profiles, keyring, collection selection, authenticated HTTP.
  - `repos.go`, `pr_writes.go`, `workitems.go`, `boards_write.go`, `teams.go`: the commands T3 calls.
  - `invoke.go`, `catalog.go`, `rest_url.go`, `generic_http.go`, `api_*.go`: `devops invoke` and `rest`.
  - `setup_*.go`, `preserve_*.go`, `repo_commands.go`: checkout setup, restore and the Git remote helper.
  - `route_*.go`, `https_*.go`, `link_*.go`, `browser_target.go`: routing. `*_darwin.go`, `*_linux.go`, `*_windows.go` hold the OS integration.
  - `t3_test.go`: the stock T3 adapter test. `testdata/`: fixtures.
- `.github/workflows/test.yml`: CI on macOS, Windows and Ubuntu.

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

## Code comments

Default to no comment. Existing comments record what the code can't show: a server quirk ("Server 2022.2 accepts adding an empty tag string but keeps existing tags"), a T3 expectation (time budgets, path encoding) or a security reason. Keep new ones to that, in a line or two. Don't narrate steps or record history.

## Docs map

| Audience | Where |
| --- | --- |
| Everyone | [README.md](README.md): what tfsaz is, how it works, features, status, constraints, docs index |
| Operators and script authors | `docs/user/`: getting started, install, configure, checkout setup, what works in T3, diagnose, routing, browser routing (managed and manual), desktop link handler, Azure CLI compatibility |
| Maintainers and agents | `docs/internals/`: [architecture](docs/internals/architecture.md), [T3 compatibility](docs/internals/t3-compatibility.md) (why tfsaz is needed, adapter files, keeping coverage current) |
| Maintainers | [`docs/operations/development.md`](docs/operations/development.md): dev setup, tests, CI. `docs/verification/`: dated live records. [`docs/backlog.md`](docs/backlog.md): deferred work, including the code-reference scan |

When implementing a command, read the T3 adapter and its tests first; [T3 compatibility](docs/internals/t3-compatibility.md) names the files.

## Documentation rules

- Most code changes need no doc change. Check whether a change makes existing guidance wrong.
- `docs/user/`: how an operator does a task, in the product's voice. What a feature does, how to start, what is unexpected. No implementation details, no contributor tooling, no dated test logs (link to the record). Treat macOS, Windows and Linux equally.
- `docs/internals/`: design decisions and their reasons, constraints across components, traps hard to see in the code. Before adding a paragraph, ask what a maintainer would get wrong without it. No feature lists, field lists or control-flow narration.
- `docs/operations/`: maintainer runbooks. `docs/verification/`: dated live results, kept in full.
- When a documented fact changes, rewrite or remove the old text. Do not append a second account.
- Code and docs in English. No em dashes in markdown.
- CLI help and error texts link to docs by GitHub URL. `TestDocLinks` checks those URLs and all relative links and anchors in the docs; run `go test ./...` after moving a doc or renaming a heading.

## Plans and work artifacts

Do not commit plans, research notes or scratch files; keep them outside the worktree. Deferred work goes to [docs/backlog.md](docs/backlog.md), short and without task status. Remove an entry in the commit that lands it.
