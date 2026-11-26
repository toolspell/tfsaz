# Development

## Setup

- Go at the version in `go.mod` (1.26 or newer).
- Git and OpenSSH for the transport tests, Node for the TLS probe, and GIO on Linux for desktop dispatch.
- On Windows use Git for Windows with its bundled shell.

## Checks

| Command | What it runs | When |
| --- | --- | --- |
| `go run ./tools/dev check` | On the files changed since the merge base with `main`, uncommitted and untracked ones included: gofmt on changed Go files; `go vet` and golangci-lint (new issues only) for darwin, linux and windows; `go mod tidy -diff` for the touched modules; actionlint when a workflow changed; gitleaks on the branch's commits and uncommitted changes; `go test` on the root package and the changed packages. | Before every commit. A few seconds; about 35 s more when `internal/az` changed. |
| `go run ./tools/dev ready` | The whole tree: gofmt, `go vet` for all three systems, golangci-lint new issues, `go mod tidy -diff` for every module, actionlint, gitleaks over the full history and uncommitted changes, `go test -race -count=1 ./...`. Reports that never fail it: all golangci-lint findings as counts, govulncheck and deadcode, each for all three systems. | Before a push. About 40 s. |
| Pre-commit hook | Staged content only: gofmt, gitleaks `--staged`, and the repo checks when a `.go`, `.md` or `.github/` file is staged. | On `git commit`. About 1 s. |
| `go test -count=1 ./internal/az -run 'TestName'` | Focused tests. `-run` takes a regular expression, so `'TestA\|TestB'` runs both. | While iterating. |

- `check -base <ref>` compares against another ref. On `main` itself `check` sees only uncommitted changes; `-base HEAD~1` checks the last commit.
- Vet and lint type-check each system from any host, so a `_windows.go` change is checked on macOS too. Tests run only for the host; CI covers the other two.
- Without cgo or a C compiler, `ready` runs the tests without `-race` and says so in the step name.

Tests use temporary Git repositories, loopback HTTP/TLS servers and a test credential store. The rules for writing them are in [Testing](../internals/testing.md).

### Repo checks

Go tests in the root package that read the whole tree. Every `go test` of `.` runs them, so `check` and CI do too, and the hook runs them as `go test -count=1 -run 'TestDocLinks|TestNoEmDashInMarkdown|TestPinnedActions' .`. They always check everything: renaming a heading breaks links in other files.

| Test | What it checks |
| --- | --- |
| `TestDocLinks` (`docs_links_test.go`) | Relative links and heading anchors in README.md, AGENTS.md and `docs/`, plus every `https://github.com/toolspell/tfsaz` URL in Go code and docs, such as the links in CLI help and error texts. |
| `TestPinnedActions` (`repo_checks_test.go`) | Every `uses:` in `.github/workflows/` and `.github/actions/` names a full 40-hex commit SHA followed by a `# vX` version comment. Local `./` actions are exempt; `docker://` images need a `sha256` digest. |
| `TestNoEmDashInMarkdown` (`repo_checks_test.go`) | No em dash (U+2014) in any markdown file Git tracks or would track; reports `file:line`. |

### Lint and existing findings

`.golangci.yml` enables golangci-lint's standard linters plus `unused` and `nolintlint`. A `//nolint` must name a specific linter and give a reason.

The existing code has findings that the structure phase leaves unfixed; the [backlog](../backlog.md#code) summarizes them. The gates therefore run golangci-lint with `--new-from-merge-base=main`: an issue fails only on a line changed since the merge base. Unlike an exclusion list, this hides no new issue of an already-known kind, and a finding resurfaces as soon as its line is edited. `ready` prints the full counts as a report.

## Pre-commit hook

`.githooks/pre-commit` is versioned and runs `go run ./tools/dev staged`. It checks the staged content, not the working tree, so a partially staged file is checked as it will be committed. On Windows, Git for Windows runs it in its bundled shell.

Enable it once per clone with `git config core.hooksPath .githooks`; the setup command does this. The setting lives in the clone's shared Git config, so every worktree of the clone uses the hook its own checkout contains.

## Worktree setup

```sh
go run ./tools/dev setup -tools
```

T3 runs this as the project's "Setup Worktree" script. It enables the hook, runs `go mod download` for the root module and each tool module, and with `-tools` builds the pinned tools into Go's build cache, so the first check does not wait for them. It is safe to run again.

## Pinned tools

| Tool | Version | Module |
| --- | --- | --- |
| golangci-lint | v2.15.0 | `tools/golangci-lint` |
| govulncheck (`golang.org/x/vuln`) | v1.10.0 | `tools/govulncheck` |
| deadcode (`golang.org/x/tools`) | v0.53.0 | `tools/deadcode` |
| actionlint | v1.7.12 | `tools/actionlint` |
| gitleaks | v8.30.1 | `tools/gitleaks` |

- Each tool is a `tool` directive in its own module, run as `go tool -modfile=tools/<name>/go.mod <name>`. Nothing is installed globally, and the product's `go.mod` and `go.sum` hold no tool dependencies.
- One module per tool, because a shared module resolves every shared dependency to the highest version any tool needs: in one module, gitleaks lifted golangci-lint's viper from 1.12 to 1.19. The binaries would then differ from the tested releases. golangci-lint also advises against sharing its module.
- `tools/dev` and `tools/syncrepos` are packages of the root module and use only the standard library.
- To bump a tool: `go -C tools/<name> get <module>@<version>`, then `go -C tools/<name> mod tidy`, update the table above and run `go run ./tools/dev ready`. Dependabot proposes the bumps weekly, for direct dependencies only, so each tool keeps the indirect versions its release was built with.
- `.gitleaks.toml` extends the default rules with Azure DevOps PAT rules. A deliberate fake secret is allowlisted there by path or regex with a written reason; a real secret is removed and rotated, never allowlisted.

## Isolate manual runs

A manual run of the built binary uses the real configuration, keyring and, for some commands, system files. Before trying a command by hand:

- Set `TFSAZ_CONFIG` to an absolute path in a temporary directory.
- Run `repo setup` and `repo restore` only on throwaway clones, with `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_NOSYSTEM` set to isolate Git.
- Run `route setup` and `route restore` only on a disposable test machine or session; they edit the hosts file, trust stores and services.

[AGENTS.md](../../AGENTS.md#ways-to-hurt-yourself) lists every trap.

## Race detector and CI

The [native test workflow](../../.github/workflows/test.yml) runs `go vet` and this suite on macOS, Windows and Ubuntu, on pushes to `main`, on pull requests and on manual dispatch. All three jobs enable Go's race detector; locally, `ready` runs the tests with it. Windows x64 needs a compatible MinGW-w64 C compiler on PATH. [Go race detector requirements](https://go.dev/doc/articles/race_detector#Requirements)

## GitHub automation

These files in `.github/` take effect once they are pushed to GitHub; locally they do nothing.

| File | What it does |
| --- | --- |
| `dependabot.yml` | Weekly grouped updates for the Go module, weekly updates of the pinned tools (`build(deps): ...`) and workflow actions (`ci(deps): ...`), at most 3 open PRs each, no release younger than 7 days, labeled `dependencies`. |
| `labels.json`, `workflows/labels.yml` | The label set. The workflow creates and updates these labels on a push to `main` that changes them, or on manual dispatch. It never deletes a label. |
| `ISSUE_TEMPLATE/` | Bug report and feature request forms. Blank issues are off; security reports go to `SECURITY.md`. |
| `pull_request_template.md` | Problem, Change, Verification and a macOS, Windows and Linux checklist. |

Pin every workflow action to a full commit SHA with a `# vX.Y.Z` comment (`TestPinnedActions` enforces it); Dependabot keeps both current. Check a new pin with `git ls-remote https://github.com/OWNER/REPO refs/tags/vX.Y.Z*` (use the `^{}` line for annotated tags).

## Reference checkouts

Agents and maintainers read upstream reference code (T3 Code, Azure CLI, the Azure DevOps CLI extension, knack) only from the gitignored `.reference-apps/` folder. [T3 compatibility](../internals/t3-compatibility.md#references) explains what each one is for.

```sh
go run ./tools/syncrepos                 # all entries
go run ./tools/syncrepos --repo t3code   # one entry
```

- Run it from anywhere inside the repository; each worktree gets its own folder. It needs Git and network access, and about 11 MB in total.
- For each entry it clones or fetches only the pinned commit, applies the sparse paths, checks out that commit, resets local edits and stray files, and verifies the HEAD, the sparse patterns, a clean tree and that every listed path exists. A second run that finds everything in place downloads nothing.
- Tests and CI never need it; `go test ./...` skips the folder.

To bump a pin, edit the entry in `tools/syncrepos/main.go` (tag, full commit, sparse paths) and run the sync. It fails if a listed path no longer exists at the new commit. The `t3code` pin moves together with the T3 revision in [T3 compatibility](../internals/t3-compatibility.md#keeping-coverage-current); the steps per entry are in the skills `.agents/skills/reference-t3code` and `.agents/skills/reference-azure-cli`.

### Agent skills

Skills live in `.agents/skills/`, which Codex reads. Claude Code reads `.claude/skills`, a committed symlink to `../.agents/skills`. On Windows, Git checks out that symlink as a plain text file unless symlinks are enabled: turn on Developer Mode and clone with `git clone -c core.symlinks=true`. Otherwise Claude Code finds no project skills there; the skill files themselves stay readable in `.agents/skills/`.

## Stock T3 adapter test

The opt-in integration test builds the actual executable and runs T3's existing Azure adapter against it. It requires Git, Node with TypeScript support (tested with Node 26), a T3 source tree with its lockfile dependencies installed, and a usable OS keyring. It creates a random keyring entry for its loopback server and removes the entry at the end.

```sh
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

In PowerShell, set `$env:TFSAZ_T3_ROOT = 'C:\path\to\t3code'`, then run the same `go test` command. Use a logon with a loaded user profile and an accessible Credential Manager. For unattended Linux runs, start an unlocked Secret Service collection on the test session's D-Bus. The fixture uses its own random keyring entry.

Users do not need a T3 source checkout; only this test does. The sparse `.reference-apps/t3code` cannot serve it, because it holds only the adapter sources and no dependencies.

## Opt-in live system tests

These tests change real OS state and skip unless their variable is set. Run them only on a disposable test machine or session.

| Variable | What it does |
| --- | --- |
| `TFSAZ_LIVE_LAUNCHD=1` (macOS) | Loads the generated LaunchAgents into the real GUI session on a free high port, from a temporary directory, then unloads them. `443` uses the production port. It changes no trust, hosts or LaunchAgents directory. |
| `TFSAZ_LIVE_TASK_SCHEDULER=1` (Windows) | Registers a real scheduled task in an interactive test session and removes it. |

## Test data and records

- Rules for fixtures, live gates and test PATs: [Testing](../internals/testing.md#fixtures-and-live-tests).
- Record dated live results in [`docs/verification/`](../verification/), not in user guides. Current records: [offline fixtures and native suites](../verification/offline-fixtures.md), [live T3 results](../verification/live-t3-2026-11.md) and [browser routing](../verification/browser-routing.md).
