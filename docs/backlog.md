# Backlog

Deferred work, so any session or worktree can pick it up.

- Only deferred work. No task status and no plans: work in progress lives in its thread.
- Add an entry when something is deliberately deferred; remove it in the commit that lands it.
- Keep entries short: the decision and where to start.

## Before going public

- **Browser-redirect TLS key on disk:** move the leaf private key `alias-key.pem` in `<config>.browser-route`, written by `internal/az/route_managed.go`, into the OS secret store, or decide otherwise.
- **Pre-public git history audit** for personal details, secrets and other things that deserve cleanup. Cover every commit (not just the current tree), commit messages and `docs/verification/`. Look for:
  - personal names and machine paths;
  - real hostnames, IP addresses and collection URLs;
  - testbed identifiers;
  - PATs or other secrets;
  - leftovers such as large or stray files.

  If something turns up, the history has to be rewritten. That is easiest before anyone else clones the repo.
- **Decide the product name:** tfsaz is a working name. A rename also changes persisted names (keyring services, `TFSAZ_` variables, `~/.tfsaz`, `tfsaz-<24 hex>` alias hosts, `remote.origin.vcs=tfsaz`, link handler IDs) and needs a migration. [Naming](internals/naming.md#tfsaz-is-a-working-name) lists them.
- **License decision and CONTRIBUTING.md:** the README has only placeholders, and GitHub shows no license.
- **CI path filter:** skip the 3-OS test matrix (about 5 minutes) on docs-only changes, using affected detection. Pointer: Toolspell `scripts/ci-changes.ts`.
- **Secrets scan in CI:** run gitleaks over the pushed commits in CI, as `go run ./tools/dev ready` does locally (pinned in `tools/gitleaks`, config `.gitleaks.toml`). PATs are the core risk. Pointer: Toolspell `scripts/check-secrets.ts`.
- **Local checks in CI:** CI runs only `go vet` and `go test -race`. Add golangci-lint new issues, `go mod tidy -diff`, actionlint and govulncheck, using the pinned tool modules (`tools/<name>/go.mod`).
- **Low-value comment analyzer** (optional): a custom analyzer as the Go equivalent of Toolspell's `no-low-value-comments`. Pointer: Toolspell `oxlint-plugin-toolspell/rules/`.
- **Local workflow run:** run the real workflow locally with nektos/act, optionally on a remote Docker context (Toolspell `vp run ci:local`, `scripts/ci-local.ts`). A green run is evidence, not proof. act runs only Linux containers, so the macOS and Windows jobs of `.github/workflows/test.yml` need another route, for example the test machines or VMs from the test-environment runbook item.
- **Agent skills:** a testbed/VM verification skill in `.agents/skills/` (pairs with the test-environment runbook item).

## At the public / PR switch

- **PR checks:** PR title in conventional-commit form, PR size labels, PR limits and the stale bot. Pointers: Toolspell `.github/workflows/pr-title.yml`, `pr-size.yml`, `pr-limits.yml`, `stale-needs-more-info.yml`.
- **Review bots and review rules:** CodeRabbit config with path instructions, Macroscope approvability, Codex review, and a `claude.yml` workflow (actor-restricted, comment-only). Add a "Code review rules" section to AGENTS.md that maps areas to the docs owning their rules, and Macroscope check-run agents such as test-signal. Pointers: Toolspell `.coderabbit.config.ts`, `.macroscope/approvability.md`, `.macroscope/check-run-agents/`, `.github/workflows/claude.yml`.
- **Repo settings and rulesets** (Toolspell's phase-1 setup):
  - squash-only merges and auto-delete of merged branches;
  - a ruleset on `main` with required checks, the strict up-to-date rule and no force push;
  - secret scanning with push protection, private vulnerability reporting and the org security configuration.
- **Security contact link:** `.github/ISSUE_TEMPLATE/config.yml` links `SECURITY.md` as the place to report vulnerabilities, and [SECURITY.md](../SECURITY.md#report-a-vulnerability) sends reporters to GitHub's private vulnerability reporting once the repository is public. Enable private vulnerability reporting at the switch, or both lead to a policy with no private channel.
- **CodeQL default setup for Go plus Copilot Autofix**, both free for public repositories.
- **Vouch and contribution triage** for outside contributors. Pointers: Toolspell `.github/workflows/vouch.yml`, `.github/VOUCHED.td`, `.agents/skills/contribution-triage/`.
- **Agent context budget** (decided): measured AGENTS.md bytes in a JSON file, a Go test that keeps it exact, an AGENTS.md rule, and a label-gated CI check. Pointers: Toolspell `scripts/agent-context-budget.json`, `scripts/agent-docs.test.ts`, `.github/workflows/agent-context-budget.yml`.

## Later

- **Release pipeline:** signed or checksummed binaries for macOS, Windows and Linux (for example with goreleaser). The README states there is no release yet.
- **Skills link on Windows:** `.claude/skills` is a committed symlink, which Git checks out as a text file on Windows without `core.symlinks`, so Claude Code sees no project skills there. Let the worktree setup command (`go run ./tools/dev setup`) create a directory junction instead, and verify it on Windows.

## Code

- **Existing golangci-lint findings** (v2.15.0, `.golangci.yml`; the gates see only new ones): 222 on darwin, 206 on linux, 214 on windows. Decide per linter: fix, or disable with a reason in `.golangci.yml`. List them with `go tool -modfile=tools/golangci-lint/go.mod golangci-lint run --max-issues-per-linter=0 --max-same-issues=0 ./...` (set `GOOS` for another system).
  - `errcheck`, 146 on darwin, 79 of them in tests (unchecked `fmt.Fprintf`/`io.WriteString` in test handlers). Production code: mostly deferred `Close` and best-effort `os.Remove`/`os.RemoveAll`; most in `route_proxy.go` (10), `route_system.go` (7), `setup_manager.go` and `invoke.go` (5 each).
  - `staticcheck`, 69: 67 `ST1005` (capitalized or punctuated error strings; check whether some mirror Azure CLI texts on purpose), one `QF1001`, one `QF1003`.
  - `unused`, 7 on darwin: the same symbols as the dead-code entry below.
- **Dead code** (deadcode v0.53.0 and `unused`; both depend on `GOOS`): unreachable on every system unless noted. Delete, or move to the file of the system that uses it.
  - `routeSystemLeafNames` (`route_system.go:218`, darwin and linux), `routeSystemSystemTrust` (const in `route_system_darwin.go`, `_linux.go`, `_windows.go`).
  - darwin only: `linkHandlerPaths` (`link_open.go:50`); `writeRootFile`, `reserveRoutePort`, `ownsRoutePort`, `releaseRoutePort` and the const `routePortOwner` in `route_system_unix.go`, which only linux uses.
  - linux only: `editRouteHosts` (`route_system_hosts.go:24`). windows only: `routeHostsShell` and `shellQuote` (`route_system_hosts.go:91`, `:134`), and the test helpers `routeSystemCall` and `fakeRouteSystemCommands` (`route_system_test.go:254`, `:261`).
- **Real keyring in managed-SSH setup:** `internal/az/setup_route.go:217` reads the system keyring when managed-SSH `repo setup` runs on an HTTPS origin without `--ssh-url`. Tests avoid it only because they use `ssh://` origins.
- **Texts that break the [naming rules](internals/naming.md)**, in help and error strings and agent files:
  - `internal/az/help.go:235` (`doctor`): "Check the CLI" should say tfsaz.
  - `internal/az/help.go:131` (`rest --url`): "organization alias" should be "Azure-style alias".
  - `internal/az/help.go:379` (`route`): "Azure-style URLs" should be "synthetic URLs".
  - `internal/az/help.go:380`, `internal/az/shim.go:69`, `internal/az/shim.go:106`: "launchers", "CLI and Git helper launchers", "tfsaz launcher" should be "shims" (`az` and `git-remote-tfsaz`).
  - "Microsoft's `az`" should be "Microsoft's Azure CLI" in AGENTS.md (the line after the docs map), `.agents/skills/reference-azure-cli/SKILL.md:8` and `.agents/skills/reference-t3code/SKILL.md:35`.
  - `internal/az/preserve_git.go:27` ("the tfsaz remote helper") and `internal/az/setup_transport.go:76` ("Git transport helper") should name `git-remote-tfsaz`.
- **Tests that poll with `time.Sleep`:** candidates for waiting on an event instead ([Testing](internals/testing.md#waiting)). Four calls in three files:
  - `internal/az/route_system_darwin_test.go:286` (`TestInheritedRedirectServes`): retries an HTTPS GET every 100 ms until the child serves. The child could signal readiness instead.
  - `internal/az/link_export_linux_test.go:101` (`TestDesktopLinkHandlerGIOArguments`): polls a trace file every 10 ms after `gio launch`. GIO starts the receiver detached, so there may be no event to wait on; if so, keep it and add the reason as a comment.
  - `internal/az/route_system_windows_test.go:475` and `:482` (`TestWindowsScheduledServiceRestartsFailedAction`, live gate): polls the task's retries every second, then sleeps 12 s to prove no restart after stop. The 12 s sleep is on purpose: it outlasts the supervisor's 10 s retry delay (`route_system_windows.go:146`), and a restart that did not happen has no event; add that reason as a comment. The 1 s poll could stay for the same reason.

- **Windows live test skip message:** `internal/az/route_system_windows_test.go:446` (`TestWindowsScheduledServiceRestartsFailedAction`) skips with "requires an interactive Windows test session" and does not name `TFSAZ_LIVE_TASK_SCHEDULER`, unlike the launchd live test (`set TFSAZ_LIVE_LAUNCHD=1 ...`).

## Platforms

- **Check commands on Windows and Linux:** `tools/dev` (`check`, `ready`, `staged`, `setup`) and `.githooks/pre-commit` were verified only on macOS. Verify on Windows (the hook in Git for Windows' shell, CRLF checkouts, `-race` with MinGW-w64, `go tool` paths) and on Linux.
- **WSL 2 and WSL 3:** check for a platform gap. The code has no WSL-specific handling. Check:
  - T3 backend inside WSL with the browser or desktop app on Windows: which side needs `route setup`, the hosts file, the trust stores and the desktop link handler.
  - Secret Service availability for go-keyring inside WSL.
  - Git and SSH transport across the WSL/Windows boundary.
  - T3's own adapter already mentions "Windows az running in WSL" (`AzureDevOpsCli.ts`, scope detection and `@-` stdin text): check which side's `az` T3 runs in that setup.
- **Windows check of the routing key directory:** on macOS and Linux, the redirector refuses a key file readable by group or others, and setup checks the directory's mode and owner. On Windows, setup sets a protected ACL but nothing checks it later (`internal/az/route_redirect.go:83`, `routeSystemOwnedByUser` in `route_system_windows.go`).
- **Platform parity gaps** the docs reveal:
  - Linux is documented and tested only on Ubuntu (systemd, nftables, sudo, GIO).
  - Browser trust coverage per OS: check Chrome/Chromium (and Edge) on macOS, Windows and Linux as well as Firefox. Managed setup trusts automatically only in some combinations; Firefox needs manual trust everywhere; the manual trust table covers only Chromium on Ubuntu.
  - Live PR and Boards writes were tested only with a macOS backend.
  - There is no opt-in systemd live test (launchd and Task Scheduler have one).
  - External links from the Windows T3 app are unverified.
  - Credential-store setup on Linux and Windows is described only as a test workaround.
  - Hosts-edit recovery is documented only for Windows.

## Docs and process

- **Test environment runbook:** document how to set up the environments used for verification (an Azure DevOps Server testbed with two collections, macOS, Windows 11 and Ubuntu machines or VMs, credential stores, T3 install) as a runbook in `docs/operations/`. Starting points:
  - `docs/operations/development.md`: T3 adapter test prerequisites, Windows logon with a loaded profile for Credential Manager, an unlocked Secret Service collection on Linux, the live system test variables.
  - `docs/verification/live-t3-2026-11.md`: server version 2022.2, two collections on one server (one name with a space) with distinct aliases, T3 Nightly with an isolated data directory, GNOME Keyring session on Ubuntu, password-backed Windows logon, explicitly trusted testbed CA, Git for Windows' OpenSSH.
  - `docs/verification/browser-routing.md`: disposable browser profiles, Windows over SSH versus RDP, `t3 browser setup` for AppArmor on Ubuntu.
- **Structure-phase rule:** remove the structure-phase no-code-changes rule from AGENTS.md once the repo setup is done.
