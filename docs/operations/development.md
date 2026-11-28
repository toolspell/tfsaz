# Development

## Setup

- Go at the version in `go.mod` (1.26.11 or newer). The minimum is a patch release because Go ships standard library security fixes in patch releases. Dependabot does not bump the `go` line; the [update-references](../../.agents/skills/update-references/SKILL.md) run checks it.
- Git and OpenSSH for the transport tests, Node for the TLS probe, and GIO on Linux for desktop dispatch.
- On Windows use Git for Windows with its bundled shell.

## Checks

| Command | What it runs | When |
| --- | --- | --- |
| `go run ./tools/dev check` | On the files changed since the merge base with `main`, uncommitted and untracked ones included: gofmt on changed Go files; `go vet` and golangci-lint on the whole tree for darwin, linux and windows; `go mod tidy -diff` for the touched modules; actionlint when a workflow changed; gitleaks on the branch's commits and uncommitted changes; `go test` on the root package and the changed packages, plus the mock module's tests when a file under it changed. | Before every commit. A few seconds; about 35 s more when `internal/az` changed. |
| `go run ./tools/dev ready` | The whole tree: gofmt, `go vet`, golangci-lint and govulncheck for all three systems, `go mod tidy -diff` for every module, actionlint, gitleaks over the full history and uncommitted changes, `go test -race -count=1 ./...` in each code module. A report that never fails it: deadcode for all three systems. | Before a push. About 40 s. |
| `go run ./tools/dev ci-local` | The [CI workflow](#ci) with act in Docker: the Linux jobs only. | Before a push that changes CI, or to reproduce a CI failure. A few minutes. |
| Pre-commit hook | Staged content only: gofmt, gitleaks `--staged`, and the repo checks when any file is staged. | On `git commit`. About 1 s. |
| `go test -count=1 ./internal/az -run 'TestName'` | Focused tests. `-run` takes a regular expression, so `'TestA\|TestB'` runs both. | While iterating. |

- The code modules are the root module and the live-test mock in `live-tests/devops-server-mock`, which has a `go.mod` of its own. "The whole tree" covers both: vet, lint, govulncheck, deadcode and the tests run in each. The tool modules under `tools/` get only `go mod tidy -diff`. The list is `codeModules` in `tools/dev/run.go`.
- `check -base <ref>` compares against another ref. On `main` itself `check` sees only uncommitted changes; `-base HEAD~1` checks the last commit.
- Vet, lint and govulncheck type-check each system from any host, so a `_windows.go` change is checked on macOS too. Tests run only for the host; CI covers the other two.
- govulncheck needs network access and judges the standard library of the Go that runs it: an outdated local Go fails `ready` until you update it.
- Without cgo or a C compiler, `ready` runs the tests without `-race` and says so in the step name.

Tests use temporary Git repositories, loopback HTTP/TLS servers and a test credential store. The rules for writing them are in [Testing](../internals/testing.md).

For VM verification of changes that depend on the client OS or T3, use the [live-tests skill](../../.agents/skills/live-tests/SKILL.md) on a live-test host.

### Repo checks

Go tests in the root package that read the whole tree. Every `go test` of `.` runs them, so `check` and CI do too, and the hook runs them as `go test -count=1 -run '<repoChecksRun>' .` (the list is in `tools/dev/run.go`). They always check everything: renaming a heading breaks links in other files.

Pick a new mechanical guard by what it reads: a pattern inside one Go file belongs to golangci-lint ([Lint](#lint)); a check across files, on Markdown or on whether files exist is a repo check. A deliberate exception carries a written reason.

| Test | What it checks |
| --- | --- |
| `TestDocLinks` (`docs_links_test.go`) | Relative links and heading anchors in README.md, AGENTS.md and `docs/`, plus every `https://github.com/toolspell/tfsaz` URL in Go code and docs, such as the links in CLI help and error texts. |
| `TestPinnedActions` (`repo_checks_test.go`) | Every `uses:` in `.github/workflows/` and `.github/actions/` names a full 40-hex commit SHA followed by a `# vX` version comment. Local `./` actions are exempt; `docker://` images need a `sha256` digest. |
| `TestNoEmDashInMarkdown` (`repo_checks_test.go`) | No em dash (U+2014) in any markdown file Git tracks or would track; reports `file:line`. |
| `TestAgentContextBudget` (`repo_checks_test.go`) | `.agents/context-budget.json` equals the measured bytes of AGENTS.md, CLAUDE.md and the skills' frontmatter, which load in every agent session. CRLF counts as LF. |
| `TestNaming` (`naming_test.go`) | The names in [Naming](../internals/naming.md#the-naming-check) in every text file Git tracks or would track, except `docs/verification/`. A deliberate exception carries `naming-allow: <reason>`. |
| `TestT3PinInDocs` (`repo_checks_test.go`) | The `t3code` tag in `tools/syncrepos/main.go` appears in README.md, `docs/internals/t3-compatibility.md`, `docs/user/t3-features.md` and `docs/user/az-compatibility.md`, so a pin bump cannot leave a page naming the old one. |
| `TestAgentFileLines` (`repo_checks_test.go`) | AGENTS.md, CLAUDE.md and every markdown file under `.agents/` stay at 300 lines or fewer. |

### Lint

`.golangci.yml` enables golangci-lint's standard linters plus `unused` and `nolintlint`. Every finding fails `check`, `ready` and CI. A deliberate exception names its reason: a `//nolint` names a specific linter and says why, and an exclusion rule in `.golangci.yml` carries a comment. The `ST1005` rule, for example, exempts error strings that start with a proper noun.

deadcode only reports. It lists, per system, functions that `main` cannot reach, including code that only tests call on that system. Delete such code or move it to the file of the system that uses it; a function kept on purpose says why in its comment, as `editRouteHosts` does for Linux.

## Pre-commit hook

`.githooks/pre-commit` is versioned and runs `go run ./tools/dev staged`. It checks the staged content, not the working tree, so a partially staged file is checked as it will be committed. On Windows, Git for Windows runs it in its bundled shell.

Enable it once per clone with `git config core.hooksPath .githooks`; the setup command does this. The setting lives in the clone's shared Git config, so every worktree of the clone uses the hook its own checkout contains. Check it with `git config --show-origin core.hooksPath`; turn the hook off again with `go run ./tools/dev setup -undo`. It unsets `core.hooksPath` only when the value is `.githooks`, the one setup writes, and leaves the downloaded modules and built tools in Go's caches.

## Worktree setup

```sh
go run ./tools/dev setup -tools
```

T3 runs this as the project's "Setup Worktree" script, declared with "Check" and "Ready" in [`t3.json`](../../t3.json) (import the actions in T3's project settings once). It enables the hook, runs `go mod download` for each code module and tool module, and with `-tools` builds the pinned tools into Go's build cache, so the first check does not wait for them. It is safe to run again.

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
| Browser routing (`route setup`) | `tfsaz route restore` with the same `TFSAZ_CONFIG` path as setup; rerun it if it stops partway. | `tfsaz route verify` reports managed browser routing as absent; the hosts file has no `# tfsaz-route` line; `~/.tfsaz/route-recovery` is gone unless another configuration still uses routing. |
| Managed test checkouts | `tfsaz repo restore PATH` for each entry in `tfsaz repo list`, then delete the throwaway clones. For a clone already deleted, `tfsaz repo forget PATH`. | `tfsaz repo list` is empty; `git -C PATH config --get-regexp '^remote\.origin\.'` shows the native URL. |
| Desktop link handler, Chromium extension | Select the usual default browser, run the Windows `Uninstall.ps1`, delete the exported app, directory or desktop file, and remove the extension from the browser profile. | The steps in [Desktop link handler](../user/desktop-link-handler.md) and [Chromium extension](../user/routing.md#chromium-extension). |
| PATs of test profiles | `tfsaz profile remove --profile ID` for each test profile, before you delete the configuration that names it, then remove what `tfsaz profile list` still shows under "Keyring entries that no profile in this configuration uses" and belongs to the run. | `tfsaz profile list` shows no entry of the run; `~/.tfsaz/keyring-entries.json` is gone unless other entries remain. |
| Shims | `tfsaz shim uninstall --dir DIR` for the test shim directory, and remove it from PATH. | `command -v az` (PowerShell: `Get-Command az`) no longer finds them. |
| Temporary configuration | Delete the temp `TFSAZ_CONFIG` file and the [files beside it](../user/configure.md#files-beside-the-configuration), and unset the variable. A killed `route run` leaves `git-*` entries in `.run` until the next `route run` with that configuration. | The temp directory is gone. |

- Live gates (`TFSAZ_LIVE_LAUNCHD`, `TFSAZ_LIVE_TASK_SCHEDULER`, `TFSAZ_T3_ROOT`) remove what they create when they finish. After an aborted run, check with `launchctl print gui/$(id -u)` (macOS) or `Get-ScheduledTask` (Windows) for leftover TFSaz entries.
- Deleting a configuration first orphans its checkout registrations, so `repo restore` can no longer find them. Its PATs stay in the keyring; `profile list` with any configuration shows the recorded ones.

## CI

[`.github/workflows/test.yml`](../../.github/workflows/test.yml) (workflow "CI") runs on pushes to `main`, on pull requests, on [merge queue](#merge-queue) groups and on manual dispatch.

| Job | Runner | What it does |
| --- | --- | --- |
| Changes | ubuntu-24.04 | `go run ./tools/dev ci-changes`: whether the test matrix must run ([path filter](#path-filter)). |
| Checks | ubuntu-24.04 | `go run ./tools/dev ci-checks`: the whole-tree checks of `ready` without the tests, plus the [repo checks](#repo-checks). |
| test | macOS, Windows (three runners), Ubuntu | `go run ./tools/dev ci-test`: `go test -race -count=1 ./...` in each [code module](#checks) with the Go version in `go.mod`, the oldest supported. Skipped when only non-code files changed. Windows splits the tests into [shards](#test-shards). |
| Check | ubuntu-24.04 | Roll-up: passes when every job passed, or `test` was skipped because Changes found no code change. Fails on anything else. |

### Test shards

The tests of a package run one after another, and Windows runs the Git-heavy setup and preserve tests about five times slower than Ubuntu: about 157 s for `internal/az`, against 78 s on macOS and 28 s on Ubuntu. Windows therefore runs on three runners, `ci-test -shard 1/3` to `3/3`. Each lists the top-level tests of every package with `go test -race -list`, sorts the names and takes every third one, so each test runs in exactly one shard. The mock module's tests take seconds and run whole in the first shard. A new test needs no entry anywhere.

Three shards keep the Windows runners at about macOS's duration. More would shorten nothing, and a CI run already uses up to seven runners at once against the free plan's 20.

### Gates and reports

- **Gates** (Checks): gofmt; `go vet`, golangci-lint and govulncheck for darwin, linux and windows in each code module; `go mod tidy -diff` for every module; actionlint; gitleaks over the full history; the repo checks. The test matrix gates too.
- **Report** that never fails a run: deadcode for each system.
- TFSaz has no cgo code, so vet, lint and govulncheck with `GOOS` set on Linux see what native runs would see. Only the tests need the three runners.
- Checks and the test matrix run on the Go version in `go.mod`: govulncheck judges the standard library of the Go it runs on, and that version is the minimum every build uses.
- All three systems need the race detector; Windows x64 needs a compatible MinGW-w64 C compiler on PATH, which GitHub's runner has. [Go race detector requirements](https://go.dev/doc/articles/race_detector#Requirements)

### Change base

The path filter compares against a base:

| Event | Base | Why |
| --- | --- | --- |
| Pull request | The PR base: the first parent of GitHub's merge commit | Exactly the files the PR changes. |
| Merge queue group | `merge_group.base_sha`, the base the group was built on | Every file of every PR in the group. |
| Push to `main` | `github.event.before`, the previous head of `main` | Covers every commit of the push, including direct pushes that had no PR run. |
| Manual run, first push, or a base the clone lacks (force push) | None: the matrix runs | Nothing to compare against. |

The workflow also passes the base to `ci-checks`, which ignores it: every gate there covers the whole tree.

### Path filter

`ci-changes` lists the files changed since the [change base](#change-base). The test matrix is skipped only when every file is outside the code: markdown (except under `testdata/`), `docs/`, `.agents/`, `.claude`, `.githooks/`, `.github/scripts/`, the review bot files `.macroscope/` and `.coderabbit.config.ts`, the issue and PR templates, `labels.json`, `dependabot.yml`, `.gitleaks.toml`, `.golangci.yml` and the tool modules `tools/<tool>/`. Any other file runs the matrix, including every workflow and file types nobody listed yet. Without a base, or when the diff fails, the matrix runs and the log says why. The list is `testIrrelevant` in `tools/dev/ci.go`.

Checks always runs, so a docs-only change still gets the doc link, em dash and pinned-action checks. Skipping the matrix saves the slowest jobs, the macOS and Windows runs, on changes that cannot affect them.

The filter is a job, not `paths-ignore` on the trigger: a workflow that does not start reports no status, and a required check would wait forever. With the job, every run reports `Check`.

### Required check

`Check` is the one CI status the ruleset on `main` requires, next to `PR title` and `Agent context budget` from [GitHub automation](#github-automation). It stays stable when jobs are renamed, added or skipped; requiring the individual matrix jobs would not.

### Caching

`actions/setup-go` saves the module and build caches. Checks keys them on `go.sum`, the mock's `go.sum` and `tools/*/go.sum`, so the compiled tools, most of a cold run, come from the build cache. The test jobs key on `go.sum` per runner. Changes builds only the standard-library `tools/dev` and caches nothing.

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
| `workflows/pr-title.yml`, `scripts/pr-title.cjs` | Check `PR title`: the title is a Conventional Commit subject (`type(scope): what changed`), because the squash merge uses it as the commit subject. A title edit reruns only this check. In the merge queue it passes without checking: a group has no title, and each PR passed on its own run. |
| `workflows/pr-size.yml`, `scripts/pr-size.cjs` | Sets one `size:XS` to `size:XXL` label by changed lines, ignoring whitespace. `go.sum` files never count; `_test.go` files and `testdata/` count only in a PR that touches nothing else. Comments when a PR adds 10 or more test functions net. Runs on `pull_request_target` from the default branch and only diffs the PR's commits. |
| `workflows/agent-context-budget.yml` | Check `Agent context budget`: fails when a value in `.agents/context-budget.json` is higher than on the PR's base branch. In the merge queue it passes without checking: each PR passed against its own base, and two PRs that change the same value conflict in the file. |
| `workflows/claude.yml` | Runs Claude Code when the account in the repository variable `CLAUDE_ALLOWED_ACTOR` mentions `@claude` in an issue, a PR or a review; unset, nobody can start it. It reads code and CI results and writes comments, never pushes. Needs the secret `CLAUDE_CODE_OAUTH_TOKEN` (from `claude setup-token`). |

Pin every workflow action to a full commit SHA with a `# vX.Y.Z` comment (`TestPinnedActions` enforces it); Dependabot keeps both current. Check a new pin with `git ls-remote https://github.com/OWNER/REPO refs/tags/vX.Y.Z*` (use the `^{}` line for annotated tags).

### Agent context budget

AGENTS.md, CLAUDE.md and the skills' frontmatter load in every agent session; every other doc loads only when an agent follows a link to it. `.agents/context-budget.json` records their bytes, `TestAgentContextBudget` keeps it equal to the measured size, and the check `Agent context budget` fails a PR that raises a value above its base branch's.

Keep that text to what an agent needs before it would know to look, or whose absence does damage first: the hard rules, the traps, the glossary, the check commands, the PR basics. Move detail that matters only in one kind of task to the doc that owns it, and leave a one-line pointer in AGENTS.md that says when to read it, like the one to [Issues](#issues): "Before you file, start or close one, read Issues." A skill's frontmatter says when to use the skill; its body holds the rest. Codex reads Code review rules, and the [review bots](#review-bots) name AGENTS.md sections: keep those, or change the bot files in the same commit.

To grow the agent context on purpose anyway, update `.agents/context-budget.json` to the values `TestAgentContextBudget` prints, and ask a maintainer to add the `agent-context-increase` label. The check reruns on the label event. Growth of 200 bytes or more in one value, about two lines of prose, makes the test and the check point here; smaller growth, such as a reworded rule, fails without the advice.

### Review bots

CodeRabbit and Codex review PRs. CodeRabbit reads [`.coderabbit.config.ts`](../../.coderabbit.config.ts), which it evaluates on its side, so the repository needs no Node toolchain for it. Codex reads [Code review rules](../../AGENTS.md#code-review-rules) in AGENTS.md. The Macroscope config in [`.macroscope/`](../../.macroscope) (`approvability.md`, `ignore.md` and the check-run agents in `check-run-agents/`) is kept, but the Macroscope app is not enabled for TFSaz. All three bot files point at the rules in AGENTS.md, `docs/internals/` and `SECURITY.md`; change a rule and the bot file that repeats it together.

## GitHub workflows

These are the rules for `main`, enforced by a ruleset. `main` takes only squash-merged PRs that pass `Check`, `PR title` and `Agent context budget`, on the PR and again in the [merge queue](#merge-queue). Resolve a Dependabot PR's conflicts with a `@dependabot rebase` comment, not by pushing to it: Dependabot stops rebasing a PR once someone else pushed to it.

### Co-author attribution

Keep the human author and add a `Co-authored-by` trailer after a blank line for each agent that contributed changes. Keep any existing co-author trailers. A review alone calls for no co-author trailer.

Use the contributing agent's GitHub-linked identity. TFSaz uses these trailers for Codex and Claude Opus 5.5.

```text
Co-authored-by: Codex <267193182+codex@users.noreply.github.com>
Co-authored-by: Claude Opus 5.5 <noreply@anthropic.com>
```

For another harness or model, use the identity its integration provides if the email is linked to its GitHub account. Several models may share one agent identity. Use one trailer per identity and record the specific harness and model in the PR description when that detail matters. If the agent has no verified GitHub identity, credit its harness and model by name in the PR description. [GitHub co-author requirements](https://docs.github.com/en/pull-requests/how-tos/commit-changes/creating-a-commit-with-multiple-authors)

Keep the trailers on commits in the PR for the merge queue. GitHub can carry co-authors into the squash commit even with a blank default squash body. Whoever merges the PR checks the squash commit on `main` for every credited co-author and flags any missing credit.

### Merge queue

A PR merges through the merge queue, not directly. Add it with "Merge when ready" or `gh pr merge <number> --squash`; with auto-merge (`gh pr merge <number> --squash --auto`) GitHub adds it once its own checks pass. The queue tests each PR on top of `main` and the PRs ahead of it, so nobody updates a branch to bring it up to date. Each PR still gets its own CI run, but up to three run at the same time instead of one after another.

| Setting | Value | Why |
| --- | --- | --- |
| Merge method | Squash | The PR title becomes the commit on `main`. |
| Build concurrency | 3 | Groups whose CI runs at the same time. The free plan runs 20 jobs at once, 5 of them on macOS, and pull request runs need room too. |
| PRs per merge | 1 to 5 | A lone PR does not wait for company. |
| Only merge non-failing PRs | On | A PR whose group fails leaves the queue; the PRs behind it are tested again without it. |
| Status check timeout | 30 minutes | Longer than the slowest job's 20-minute timeout. |

The queue resolves no conflicts: a PR that conflicts with `main` or with a PR ahead of it leaves the queue, and its author merges `main` in and adds it again. Every workflow that a required check comes from runs on `merge_group` and reports that check there too; without it the queue waits for the timeout.

Every workflow that runs on PRs must also work for [stacked pull requests](https://docs.github.com/en/pull-requests/get-started/about-stacked-prs):

- A `pull_request` workflow runs for every layer of a stack, as if the layer targeted `main`.
- A layer's base is the branch below it, not `main`. Compare with the merge commit's first parent (`HEAD^1`) or `pull_request.base.sha`, never a freshly fetched `main`, so each layer is judged on its own change. The CI [change base](#change-base) does this.
- `github.event.pull_request.stack` exists only for stacked PRs; guard it with `!= null`.
- Merging a stack through the API needs the asynchronous merge endpoint.
- Stacks cannot span forks, so only people with write access stack.

Push only `main` and PR branches by name. Never `git push --mirror` or `--all`: T3 Code keeps local checkpoint refs under `refs/t3/` that must not reach GitHub.

### Stacked pull requests

Drive stacks with `gh stack`; the skill `.agents/skills/gh-stack` covers its commands, exit codes and recovery. It does not cover this repository's rules:

- At most 7 PRs per stack.
- Never the interactive commands: `gh stack modify`, `switch`, bare `view` or bare `submit`. Use `view --json`, `submit --auto --open` and explicit arguments, then give every PR its Conventional Commit title with `gh pr edit --title`.
- Every layer runs CI against its own base, the branch below it, so each layer must pass on its own.
- Rebase the stack onto current `main` (`gh stack sync`) before queueing it.
- The merge queue builds at most 3 and merges at most 5 entries per group. A larger stack can split across groups, and with squash merges and automatic branch deletion the PR at the split can be closed instead of continuing ([gh-stack#444](https://github.com/github/gh-stack/issues/444)). So whoever merges a stack raises `max_entries_to_build` and `max_entries_to_merge` of the `main` ruleset (`gh api repos/toolspell/tfsaz/rulesets/28177410`) to at least the stack size, queues the whole stack with `gh stack merge <top PR> --yes --squash`, and restores 3 and 5 once it landed or failed. If it splits anyway, queue the rest again from the next open layer.

## Issues

Deferred work lives in [GitHub issues](https://github.com/toolspell/tfsaz/issues), never in a file in the repository. One issue is one piece of work someone can pick up; split an entry when its parts can land independently.

- **Body:** what is wrong or missing, where to start (files and functions, not only line numbers), and how to verify it. End with a note to cross-check the issue against the current code before starting, because paths and line numbers age.
- **Type and labels:** an issue type (Bug, Feature or Task, set with `gh issue create --type`), plus every label that applies: the area labels `documentation`, `testing` and `tooling`, `platform:<os>`, `routing`, `t3-compatibility`, `reversibility` for a gap against [Reversible and verifiable system changes](../../AGENTS.md#reversible-and-verifiable-system-changes), and `needs-live` when only a live machine, the testbed or another OS can verify it.
- **Size:** one `size:XS` to `size:XXL` label as an estimate of the PR's changed lines, on the scale `pr-size.yml` applies to PRs. Issues labeled `later` (deferred until something else happens) or `idea` (value and feasibility unchecked) get no size.
- **Open questions:** when the work needs a maintainer's answer first, label it `needs-decision` and list the questions under "Open questions". `needs-more-info` is for outside reporters.
- **Dependencies:** when one issue cannot sensibly start before another lands, add GitHub's "blocked by" relation and say why under "Depends on" in the blocked issue. Mention a looser relation in the text only.
- **Starting work:** assign whoever drives it (a maintainer, or the person an agent works for), then cross-check the issue against the current code before you rely on its paths or claims.
- **Closing:** the PR closes its issues with a closing keyword (`Closes #<n>`). GitHub applies it only when the PR merges into `main`, so a stacked layer's keyword takes effect when its stack lands. Use `Refs #<n>` instead when work remains, and note in the issue what remains.

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

To bump a pin, follow the skill `.agents/skills/update-references`: it finds the latest releases with `-latest`, diffs the old and new pin in the paths TFSaz depends on, classifies the impact, re-pins and opens an issue for each required TFSaz change. The sync fails if a listed path no longer exists at the new commit. The `t3code` pin moves together with the T3 revision in [T3 compatibility](../internals/t3-compatibility.md#keeping-coverage-current); what each entry is for is in the skills `.agents/skills/reference-t3code` and `.agents/skills/reference-azure-cli`.

### Agent skills

Skills live in `.agents/skills/`, which Codex reads. Claude Code reads `.claude/skills`, a committed symlink to `../.agents/skills`. On Windows, Git checks out that symlink as a plain text file unless symlinks are enabled: turn on Developer Mode and clone with `git clone -c core.symlinks=true`. Otherwise Claude Code finds no project skills there; the skill files themselves stay readable in `.agents/skills/`.

`.agents/skills/gh-stack` is GitHub's official skill, vendored verbatim and pinned to a release (`gh skill install github/gh-stack gh-stack --pin <tag> --agent codex --scope project`), so `gh skill update` keeps working. Do not edit it; the em dash check skips it. Before a bump, read the new version for prompt injection.

## Stock T3 adapter test

The opt-in integration test builds the actual executable and runs T3's existing Azure adapter against it. It requires Git, Node with TypeScript support (tested with Node 26), a T3 source tree with its lockfile dependencies installed, and a usable OS keyring. It creates a random keyring entry for its loopback server, with its name in `~/.tfsaz/keyring-entries.json`, and removes both at the end.

```sh
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

In PowerShell, set `$env:TFSAZ_T3_ROOT = 'C:\path\to\t3code'`, then run the same `go test` command. Use a logon with a loaded user profile and an accessible Credential Manager. For unattended Linux runs, start an unlocked Secret Service collection on the test session's D-Bus. The fixture uses its own random keyring entry.

Users do not need a T3 source checkout; only this test does. The sparse `.reference-apps/t3code` cannot serve it, because it holds only the adapter sources and no dependencies.

## Opt-in live system tests

These tests change real OS state and skip unless their variable is set. Run them only on a disposable test machine or session.

| Variable | What it does |
| --- | --- |
| `TFSAZ_LIVE=1` (live-test host) | Runs the [Go harness](live-tests.md#write-a-live-test) in disposable VM clones against the mock. |
| `TFSAZ_LIVE_LAUNCHD=1` (macOS) | Loads the generated LaunchAgents into the real GUI session on a free high port, from a temporary directory, then unloads them. `443` uses the production port. It changes no trust, hosts or LaunchAgents directory. |
| `TFSAZ_LIVE_TASK_SCHEDULER=1` (Windows) | Registers a real scheduled task in an interactive test session and removes it. |
| `TFSAZ_T3_ROOT=PATH` | Runs the [stock T3 adapter test](#stock-t3-adapter-test) against a full T3 source tree, with a random entry in the real keyring that it removes. |

Tests that need a whole disposable machine run in the live-test VMs against the Azure DevOps Server mock: [Live tests](live-tests.md).

## Test data and records

- Rules for fixtures, live gates and test PATs: [Testing](../internals/testing.md#fixtures-and-live-tests).
- Record dated live results in [`docs/verification/`](../verification/), not in user guides. Current records: [offline fixtures and native suites](../verification/offline-fixtures.md), [live T3 results](../verification/live-t3-2026-11.md), [browser routing](../verification/browser-routing.md), [live VM browsers](../verification/live-vm-browsers-2026-11.md) and [live VM smoke](../verification/live-vm-smoke-2026-11.md).
