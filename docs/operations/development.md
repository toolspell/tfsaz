# Development

## Setup

- Go at the version in `go.mod` (1.26.11 or newer). The minimum is a patch release because Go ships standard library security fixes in patch releases. Dependabot does not bump the `go` line; the [update-references](../../.agents/skills/update-references/SKILL.md) run checks it.
- Git and OpenSSH for the transport tests, Node for the TLS probe, and GIO on Linux for desktop dispatch.
- On Windows use Git for Windows with its bundled shell.

## Checks

| Command | What it runs | When |
| --- | --- | --- |
| `go run ./tools/dev check` | On the files changed since the merge base with `main`, uncommitted and untracked ones included: gofmt on changed Go files; `go vet` and golangci-lint (new issues only) for darwin, linux and windows; `go mod tidy -diff` for the touched modules; actionlint when a workflow changed; gitleaks on the branch's commits and uncommitted changes; `go test` on the root package and the changed packages. | Before every commit. A few seconds; about 35 s more when `internal/az` changed. |
| `go run ./tools/dev ready` | The whole tree: gofmt, `go vet`, golangci-lint new issues and govulncheck for all three systems, `go mod tidy -diff` for every module, actionlint, gitleaks over the full history and uncommitted changes, `go test -race -count=1 ./...`. Reports that never fail it: all golangci-lint findings as counts and deadcode, each for all three systems. | Before a push. About 40 s. |
| `go run ./tools/dev ci-local` | The [CI workflow](#ci) with act in Docker: the Linux jobs only. | Before a push that changes CI, or to reproduce a CI failure. A few minutes. |
| Pre-commit hook | Staged content only: gofmt, gitleaks `--staged`, and the repo checks when a `.go`, `.md` or `.github/` file is staged. | On `git commit`. About 1 s. |
| `go test -count=1 ./internal/az -run 'TestName'` | Focused tests. `-run` takes a regular expression, so `'TestA\|TestB'` runs both. | While iterating. |

- `check -base <ref>` compares against another ref. On `main` itself `check` sees only uncommitted changes; `-base HEAD~1` checks the last commit.
- Vet, lint and govulncheck type-check each system from any host, so a `_windows.go` change is checked on macOS too. Tests run only for the host; CI covers the other two.
- govulncheck needs network access and judges the standard library of the Go that runs it: an outdated local Go fails `ready` until you update it.
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

The existing code has findings that the structure phase leaves unfixed; the [backlog](../backlog.md#code) summarizes them. The gates therefore run golangci-lint with `--new-from-merge-base=main`: an issue fails only on a line changed since the merge base. Unlike an exclusion list, this hides no new issue of an already-known kind, and a finding resurfaces as soon as its line is edited. `ready` and CI print the full counts as a report. CI compares against its own base ([lint base](#lint-base)).

## Pre-commit hook

`.githooks/pre-commit` is versioned and runs `go run ./tools/dev staged`. It checks the staged content, not the working tree, so a partially staged file is checked as it will be committed. On Windows, Git for Windows runs it in its bundled shell.

Enable it once per clone with `git config core.hooksPath .githooks`; the setup command does this. The setting lives in the clone's shared Git config, so every worktree of the clone uses the hook its own checkout contains. Check it with `git config --show-origin core.hooksPath`; turn the hook off again with `git config --unset core.hooksPath`.

## Worktree setup

```sh
go run ./tools/dev setup -tools
```

T3 runs this as the project's "Setup Worktree" script, declared with "Check" and "Ready" in [`t3.json`](../../t3.json) (import the actions in T3's project settings once). It enables the hook, runs `go mod download` for the root module and each tool module, and with `-tools` builds the pinned tools into Go's build cache, so the first check does not wait for them. It is safe to run again.

## Pinned tools

| Tool | Version | Module |
| --- | --- | --- |
| golangci-lint | v2.15.0 | `tools/golangci-lint` |
| govulncheck (`golang.org/x/vuln`) | v1.10.0 | `tools/govulncheck` |
| deadcode (`golang.org/x/tools`) | v0.53.0 | `tools/deadcode` |
| actionlint | v1.7.12 | `tools/actionlint` |
| gitleaks | v8.30.1 | `tools/gitleaks` |
| act | v0.2.98 | `tools/act` |

- Each tool is a `tool` directive in its own module, run as `go tool -modfile=tools/<name>/go.mod <name>`. Nothing is installed globally, and the product's `go.mod` and `go.sum` hold no tool dependencies.
- One module per tool, because a shared module resolves every shared dependency to the highest version any tool needs: in one module, gitleaks lifted golangci-lint's viper from 1.12 to 1.19. The binaries would then differ from the tested releases. golangci-lint also advises against sharing its module.
- `tools/dev` and `tools/syncrepos` are packages of the root module and use only the standard library.
- act is a tool module too, not a downloaded release binary: the same `go.sum` verification and Dependabot bumps as the others, and no download code. Its Docker client dependencies are large, so `setup` neither downloads nor builds it; the first `ci-local` builds it (about 1 minute).
- To bump a tool: `go -C tools/<name> get <module>@<version>`, then `go -C tools/<name> mod tidy`, update the table above and run `go run ./tools/dev ready`. Dependabot proposes the bumps weekly, for direct dependencies only, so each tool keeps the indirect versions its release was built with.
- `.gitleaks.toml` extends the default rules with Azure DevOps PAT rules. A deliberate fake secret is allowlisted there by path or regex with a written reason; a real secret is removed and rotated, never allowlisted.

## Isolate manual runs

A manual run of the built binary uses the real configuration, keyring and, for some commands, system files. Before trying a command by hand:

- Set `TFSAZ_CONFIG` to an absolute path in a temporary directory.
- Run `repo setup` and `repo restore` only on throwaway clones, with `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_NOSYSTEM` set to isolate Git.
- Run `route setup` and `route restore` only on a disposable test machine or session; they edit the hosts file, trust stores and services.

[AGENTS.md](../../AGENTS.md#ways-to-hurt-yourself) lists every trap.

## Return to a clean state

After a manual or live run, undo what it changed as soon as you no longer need the state ([AGENTS.md rule](../../AGENTS.md#reversible-and-verifiable-system-changes)). Run each command with the same `TFSAZ_CONFIG` as the run itself, in this order, and check the result before you delete anything:

| What the run left | Undo | Check |
| --- | --- | --- |
| Browser routing (`route setup`) | `tfsaz route restore` with the same `TFSAZ_CONFIG` path as setup; rerun it if it stops partway. | `tfsaz route verify` reports managed browser routing as absent; the hosts file has no `# tfsaz-route` line; `~/.tfsaz` is gone unless another configuration still uses routing. |
| Managed test checkouts | `tfsaz repo restore PATH` for each entry in `tfsaz repo list`, then delete the throwaway clones. | `tfsaz repo list` is empty; `git -C PATH config --get-regexp '^remote\.origin\.'` shows the native URL. |
| Desktop link handler, Chromium extension | Select the usual default browser, run the Windows `Uninstall.ps1`, delete the exported app, directory or desktop file, and remove the extension from the browser profile. | The steps in [Desktop link handler](../user/desktop-link-handler.md) and [Chromium extension](../user/routing.md#chromium-extension). |
| PATs of test profiles | `tfsaz profile logout --profile ID` for each test profile, before you delete the configuration that names it. | Your OS keyring shows no `tfsaz` entry for that profile ID. |
| Shims | Delete `az` and `git-remote-tfsaz` from the test shim directory, and remove it from PATH. | `command -v az` (PowerShell: `Get-Command az`) no longer finds them. |
| Temporary configuration | Delete the temp `TFSAZ_CONFIG` file with any `.repo-setups.json`, `.browser-route`, `.run` and `.lock` siblings, and unset the variable. A killed `route run` leaves `git-*` directories in `.run`. | The temp directory is gone. |

- Live gates (`TFSAZ_LIVE_LAUNCHD`, `TFSAZ_LIVE_TASK_SCHEDULER`, `TFSAZ_T3_ROOT`) remove what they create when they finish. After an aborted run, check with `launchctl print gui/$(id -u)` (macOS) or `Get-ScheduledTask` (Windows) for leftover tfsaz entries.
- Deleting a configuration first orphans its PATs and checkout registrations, so `profile logout` and `repo restore` can no longer find them.

## CI

[`.github/workflows/test.yml`](../../.github/workflows/test.yml) (workflow "CI") runs on pushes to `main`, on pull requests and on manual dispatch.

| Job | Runner | What it does |
| --- | --- | --- |
| Changes | ubuntu-24.04 | `go run ./tools/dev ci-changes`: whether the test matrix must run ([path filter](#path-filter)). |
| Checks | ubuntu-24.04 | `go run ./tools/dev ci-checks`: the whole-tree checks of `ready` without the tests, plus the [repo checks](#repo-checks). |
| test | macOS, Windows, Ubuntu | `go test -race -count=1 ./...` with the Go version in `go.mod`, the oldest supported. Skipped when only non-code files changed. |
| Check | ubuntu-24.04 | Roll-up: passes when every job passed, or `test` was skipped because Changes found no code change. Fails on anything else. |

### Gates and reports

- **Gates** (Checks): gofmt; `go vet`, golangci-lint new issues and govulncheck for darwin, linux and windows; `go mod tidy -diff` for the root and every tool module; actionlint; gitleaks over the full history; the repo checks. The test matrix gates too.
- **Reports** that never fail a run: all golangci-lint findings as counts, and deadcode, for each system.
- tfsaz has no cgo code, so vet, lint and govulncheck with `GOOS` set on Linux see what native runs would see. Only the tests need the three runners.
- Checks and the test matrix run on the Go version in `go.mod`: govulncheck judges the standard library of the Go it runs on, and that version is the minimum every build uses.
- All three systems need the race detector; Windows x64 needs a compatible MinGW-w64 C compiler on PATH, which GitHub's runner has. [Go race detector requirements](https://go.dev/doc/articles/race_detector#Requirements)

### Lint base

CI gates golangci-lint on lines changed since a base, like `check` and `ready` do against `main`:

| Event | Base | Why |
| --- | --- | --- |
| Pull request | The PR base: the first parent of GitHub's merge commit | Exactly the lines the PR changes. |
| Push to `main` | `github.event.before`, the previous head of `main` | Covers every commit of the push, including direct pushes that had no PR run. |
| Manual run, first push, or a base the clone lacks (force push) | None: counts only | Nothing to compare against. |

The existing findings sit on unchanged lines, so they fail no run. Editing such a line brings its finding back into the gate, as it does locally.

### Path filter

`ci-changes` lists the files changed since the same base. The test matrix is skipped only when every file is outside the code: markdown (except under `testdata/`), `docs/`, `.agents/`, `.claude`, `.githooks/`, the issue and PR templates, `labels.json`, `dependabot.yml`, `.gitleaks.toml`, `.golangci.yml` and the tool modules `tools/<tool>/`. Any other file runs the matrix, including every workflow and file types nobody listed yet. Without a base, or when the diff fails, the matrix runs and the log says why. The list is `testIrrelevant` in `tools/dev/ci.go`.

Checks always runs, so a docs-only change still gets the doc link, em dash and pinned-action checks. Skipping the matrix matters in a private repository, where a macOS minute counts ten times and a Windows minute twice.

The filter is a job, not `paths-ignore` on the trigger: a workflow that does not start reports no status, and a required check would wait forever. With the job, every run reports `Check`.

### Required check

`Check` is the one status a ruleset should require (planned for the public switch, see the [backlog](../backlog.md#at-the-public--pr-switch)). It stays stable when jobs are renamed, added or skipped; requiring the individual matrix jobs would not.

### Caching

`actions/setup-go` saves the module and build caches. Checks keys them on `go.sum` and `tools/*/go.sum`, so the compiled tools, most of a cold run, come from the build cache. The test jobs key on `go.sum` per runner. Changes builds only the standard-library `tools/dev` and caches nothing.

### Secrets found in CI

A secret that gitleaks finds in CI has already been pushed to GitHub: treat it as exposed. **Rotate it first** (revoke the PAT on the server and create a new one), then clean up the commit or history. CI prints only the count; `go run ./tools/dev ready` shows the findings locally with file and commit, values redacted. Allowlist only deliberate fake secrets in `.gitleaks.toml`.

### Local run with act

```sh
go run ./tools/dev ci-local                      # the Linux jobs
go run ./tools/dev ci-local -- -j checks         # one job; arguments after -- go to act
go run ./tools/dev ci-local -context <name>      # another Docker context, such as a remote amd64 engine
```

- It runs `.github/workflows/test.yml` with [act](https://github.com/nektos/act) as a push to `main` whose previous head is the merge base with `main` (`-base` picks another ref), so the gates see the branch's changes as `check` does.
- It runs from a temporary clone, where the uncommitted and untracked changes become one commit; the worktree stays untouched and gitignored files stay out.
- It runs no other workflow: `labels.yml` writes to GitHub. act gets no token: an empty `GITHUB_TOKEN` secret (otherwise act asks the `gh` CLI for yours), no `GITHUB_TOKEN` or `GH_TOKEN` in its environment, and empty env, secret and var files. A user-level `~/.actrc` still applies.
- The Linux jobs run in `catthehacker/ubuntu:act-24.04` (multi-arch, about 1.7 GB on first pull) on the engine's own architecture, arm64 on Apple silicon. act would run the macOS and Windows matrix entries in that Linux image too, which proves nothing about those systems, so `ci-local` runs only the Ubuntu entry; verify the others on test machines.
- A green run is evidence, not proof: the image, architecture and caching differ from GitHub's runners.

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
go run ./tools/syncrepos -latest         # pins next to the latest release tags, changes nothing
```

- Run it from anywhere inside the repository; each worktree gets its own folder. It needs Git and network access, and about 11 MB in total.
- For each entry it clones or fetches only the pinned commit, applies the sparse paths, checks out that commit, resets local edits and stray files, and verifies the HEAD, the sparse patterns, a clean tree and that every listed path exists. A second run that finds everything in place downloads nothing.
- Tests and CI never need it; `go test ./...` skips the folder.

To bump a pin, follow the skill `.agents/skills/update-references`: it finds the latest releases with `-latest`, diffs the old and new pin in the paths tfsaz depends on, classifies the impact, re-pins and puts required tfsaz changes in the [backlog](../backlog.md#code). The sync fails if a listed path no longer exists at the new commit. The `t3code` pin moves together with the T3 revision in [T3 compatibility](../internals/t3-compatibility.md#keeping-coverage-current); what each entry is for is in the skills `.agents/skills/reference-t3code` and `.agents/skills/reference-azure-cli`.

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
