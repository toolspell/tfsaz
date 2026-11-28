# TFSaz

TFSaz is a standalone Go CLI for macOS, Windows and Linux that lets an unmodified T3 Code use on-premises Azure DevOps Server. It ships under its own name and runs as `az` on the T3 backend's PATH. T3's invocation of `az` is the compatibility contract. The backend is the server's REST API, called directly. Azure cloud sign-in and extension installation are out of scope.

Stock T3 recognizes only Azure DevOps Services remotes and scopes every `az` call to a cloud organization. TFSaz therefore gives checkouts Azure-style aliases and maps them back to the real collection; do not "simplify" that to native URLs. Details: [why stock T3 cannot reach Azure DevOps Server](docs/internals/t3-compatibility.md#why-stock-t3-cannot-reach-azure-devops-server).

## Hard rules

### Compatibility contract

TFSaz works only while stock T3 Code cannot tell it apart from Microsoft's Azure CLI with the `azure-devops` extension. Any difference shows up as a broken T3 feature, not as a TFSaz error. Before you change a command, check all three:

- T3's call site and its tests: the arguments, JSON shapes, stderr and exit codes T3 reads. Skill `reference-t3code`; [T3 compatibility](docs/internals/t3-compatibility.md) names the files.
- The hosts and URL shapes T3 recognizes as Azure DevOps. The Azure-style aliases and synthetic URLs must keep matching them.
- How Microsoft's Azure CLI, the extension and knack parse, format output and fail. Skill `reference-azure-cli`. Where they differ from what T3 actually calls, T3 wins.

Resolve REST routes through the server's resource-location catalog, not hard-coded `_apis` paths, and target API 7.1 (and its previews), compatible down to Azure DevOps Server 2022.2. Details: [REST API](docs/internals/rest-api.md).

### Outside in only

TFSaz solves the problem from outside, against the real installed T3 Code app or server. Never suggest or make a change, fork or upstream PR to T3 Code, Microsoft's Azure CLI or the `azure-devops` extension, and never patch an installed copy. If a fix seems to need one, report it to the maintainer as a gap.

### Simple first

Do not overengineer, on the system or in commands. Start with the simplest approach built on common existing tools before reinventing one. Build a custom solution only when it makes our case meaningfully easier, or is meaningfully more efficient or effective.

### Reversible and verifiable system changes

- Everything TFSaz or its setup changes outside its own configuration (hosts file, trust stores, services, firewall rules, Git config and remotes, link handlers, keyring entries, shims, browser and proxy settings) must be cleanly undoable with a TFSaz command or a documented step, leaving no residue, and checkable by TFSaz itself, so users can see the current state. A feature that lacks either is a gap: report it and open an issue labeled `reversibility`.
- Clean up after yourself: revert test environments (temp configs, test checkouts, routing on test machines, test keyring entries) to a clean state as soon as you are sure the state is no longer needed.

### Security-affecting features are intended

Some features deliberately change a machine's security state: local trust for a leaf certificate, hosts entries, a loopback listener on port 443, firewall rules, link handlers. TFSaz users are developers who accept that knowingly. Do not remove or weaken such a feature as "insecure", and do not reject an idea or a possible solution for that reason: TFSaz only works through creative approaches. Instead, make sure the user docs and [SECURITY.md](SECURITY.md) say exactly what it changes, why, and how to undo it. Using TFSaz requires trust; transparency earns it.

### Credentials

- Every credential (PATs, tokens) lives only in the OS secret store through go-keyring: macOS Keychain, Windows Credential Manager, Linux Secret Service. Never in files, configuration, command arguments, logs or fixtures. Add no new on-disk secrets.
- One exception exists today: the browser-redirect TLS leaf private key `alias-key.pem` in `<config>.browser-route` (owner-only, mode 600). It is tracked in [#9](https://github.com/toolspell/tfsaz/issues/9).
- Send a PAT only to its configured collection. Map an Azure-style alias to the real collection before sending credentials.
- Redirects on authenticated requests fail closed.
- A failed credential in one collection must never select another collection's identity.

Details and the remaining routing rules: [Architecture](docs/internals/architecture.md).

### Platform parity

macOS, Linux and Windows are fully supported at feature parity. Platform-specific implementations are fine (launchd, systemd, Task Scheduler). Consider all three platforms in every change, even when a first shot or an experiment is only tried on one.

- Parity is required for a release, not for every PR.
- A PR may land a feature on fewer platforms, but every other platform must get explicit "not supported on this platform" handling: a clear error and a nonzero exit, never a silent no-op or a crash. The same PR opens a `platform:<os>` issue for each missing platform.
- A feature or doc step that misses a platform without that handling and entry is a gap to report, not a design choice.

## Glossary

- **collection**: one Azure DevOps Server project collection, addressed by its real HTTPS URL including port and virtual directory.
- **profile**: a configured collection with a stable ID, its Azure-style alias, optional extra aliases and CA, and a PAT in the keyring. Profiles are ordered; discovery uses that order.
- **Azure-style alias**: a `*.visualstudio.com` organization URL that T3 recognizes and TFSaz maps to a profile's real collection.
- **synthetic URL**: a URL on a generated alias host `tfsaz-<24 hex>.visualstudio.com`, one per project and repository. T3 sees it; nothing on the network serves it unless routing is set up.
- **native URL**: the real HTTPS or SSH URL of a repository on the server.
- **shim**: the `az` and `git-remote-tfsaz` names that `shim install` creates for the TFSaz binary. `main.go` picks the behavior by program name.
- **managed checkout**: a clone after `repo setup`: the synthetic origin for T3, the native target in its common Git config, registered in `<config>.repo-setups.json`.
- **routing**: the ways a synthetic URL reaches the real collection: the loopback redirector (`route setup`), the desktop link handler, the Chromium extension, `route run` for Git, and PAC/hosts refusal.

## Ways to hurt yourself

1. **Using the real configuration.** Without `TFSAZ_CONFIG`, TFSaz reads the developer's config in the OS user config directory, plus `<config>.repo-setups.json`, `<config>.browser-route` and `route run`'s `<config>.run` beside it. In tests and manual runs, set `TFSAZ_CONFIG` to an absolute path in a temp directory.
2. **Writing the real keyring.** `init`, `devops login` and `profile logout` write or delete OS keyring entries (`tfsaz` or `tfsaz/v2/<hash>`). Tests inject the in-memory store through `app{store: ...}`; never use `systemStore` in a test. Only `TestStockT3Adapter` touches the real keyring, with a random entry it removes. Trap: a test that runs a built TFSaz binary cannot inject a store; build it with `-tags tfsaztest` (as `managerTestRepo` in `setup_manager_test.go` does), or it uses the system keyring.
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
- `.github/workflows/test.yml`: [CI](docs/operations/development.md#ci).
- `tools/dev`: the `check`, `ready`, `staged`, `setup` and `ci-local` (act) commands, plus `ci-changes` and `ci-checks` for CI. `tools/<tool>/go.mod`: one module per pinned dev tool. `.githooks/pre-commit`: the commit hook. `tools/syncrepos`: syncs the reference checkouts.
- `live-tests/`, `tools/live`: live-test VMs; before you use or change them, read [Live tests](docs/operations/live-tests.md).
- `.agents/skills/`: agent skills, linked as `.claude/skills`.
- `.reference-apps/<name>`: gitignored, pinned sparse checkouts of T3 Code, Azure CLI, the Azure DevOps CLI extension and knack, synced with `go run ./tools/syncrepos [--repo <name>]`. Read reference code only there, never from another checkout or installation on this machine, which may be a fork or at another revision. Never edit or import from it.

## Verify

```sh
go run ./tools/dev check   # before every commit: the branch's changes, a few seconds
go run ./tools/dev ready   # before a push: everything, with -race, about 40 s
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

- While iterating, run focused tests (`go test -count=1 ./internal/az -run 'TestName'`). The pre-commit hook is a quick check of the staged content; it does not replace `check`. What each command runs, and where a new lint rule or repo check goes: [Development](docs/operations/development.md#checks).
- When a change touches a command T3 calls, run `TestStockT3Adapter` (the last line; needs a full T3 source tree with dependencies installed, which the sparse `.reference-apps/t3code` is not). Without one, say in the handoff that it did not run.
- Before you fix a bug, or write or review a test, read [Testing](docs/internals/testing.md), the one home of the test rules.

## Pull requests

- Open a PR only when asked. One request is one PR.
- Title: a conventional commit in plain language, such as `fix(setup): restore no longer leaves a lock file`. PRs are squash merged, so it becomes the commit on `main`.
- Body: fill in the [PR template](.github/pull_request_template.md).
- Credit the agents that contributed changes. Whoever merges the PR verifies that all [co-author trailers reach main](docs/operations/development.md#co-author-attribution).
- Babysit a PR with `watch_pull_request`; never poll checks or reviews.
- Workflows that run on PRs must work for stacked PRs: [GitHub workflows](docs/operations/development.md#github-workflows).
- Outside contributions follow [CONTRIBUTING.md](CONTRIBUTING.md).
- Ask Codex for a second review (`@codex review`) only after a significant change since its last review: new behavior, a redesign of reviewed logic, or a new change to credentials, system changes or a command T3 calls. Not for fixes of review findings, rebases, docs, tests or formatting.

## Code review rules

Review each changed file against the doc that owns its rules, and read that doc first:

- Commands T3 calls: [Compatibility contract](#compatibility-contract) and [T3 compatibility](docs/internals/t3-compatibility.md).
- REST calls (`invoke.go`, `catalog.go`, `rest_url.go`, `generic_http.go`, `api_*.go`): [REST API](docs/internals/rest-api.md).
- PATs, collection selection and authenticated HTTP: [Credentials](#credentials), [Architecture](docs/internals/architecture.md#authentication-and-routing-rules) and the trust model in [SECURITY.md](SECURITY.md).
- Changes outside TFSaz's configuration (`setup_*`, `preserve_*`, `route_*`, `https_*`, `link_*`, `shim*`): [Reversible and verifiable system changes](#reversible-and-verifiable-system-changes), [Security-affecting features are intended](#security-affecting-features-are-intended) and [What TFSaz changes on the machine](SECURITY.md#what-tfsaz-changes-on-the-machine).
- OS-specific code and user doc steps: [Platform parity](#platform-parity).
- Tests: [Testing](docs/internals/testing.md); ask for a test only as it allows.
- Docs, help and error texts: [Documentation rules](#documentation-rules) and [Naming](docs/internals/naming.md).

Review bots repeat these rules in their own files ([Development](docs/operations/development.md#review-bots)); change a rule and those files together.

## Code comments

Default to no comment. Existing comments record what the code can't show: a server quirk ("Server 2022.2 accepts adding an empty tag string but keeps existing tags"), a T3 expectation (time budgets, path encoding) or a security reason. Keep new ones to that, in a line or two. Don't narrate steps or record history.

## Documentation rules

- Most code changes need no doc change. Check whether a change makes existing guidance wrong.
- When a documented fact changes, rewrite or remove the old text. Do not append a second account.
- Code and docs in English. No em dashes in markdown.
- Before you write or review docs (AGENTS.md and skills included), help or error texts, read [Documentation](docs/internals/documentation.md) for what goes where and [Naming](docs/internals/naming.md) for how to write names.

## Plans and work artifacts

Do not commit plans, research notes or scratch files; keep them outside the worktree. Deferred work goes to a GitHub issue. Before you file, start or close one, read [Issues](docs/operations/development.md#issues).
