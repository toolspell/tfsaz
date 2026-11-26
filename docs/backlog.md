# Backlog

Deferred work, so any session or worktree can pick it up.

- Only deferred work. No task status and no plans: work in progress lives in its thread.
- Add an entry when something is deliberately deferred; remove it in the commit that lands it.
- Keep entries short: the decision and where to start.

## Before going public

- **Browser-redirect TLS key on disk:** move the leaf private key `alias-key.pem` in `<config>.browser-route`, written by `internal/az/route_managed.go`, into the OS secret store, or decide otherwise.
- **History rewrite before going public:** the audit on 26.11.2026 (gitleaks v8.30.1 over all refs, plus greps over the full history and commit messages) found no secrets, hosts, IPs, collection URLs or accounts. Two things should still go:
  - the developer's machine path to the T3 checkout in AGENTS.md (added in `89e90eb`, removed in `a8e2385`);
  - research pointers to a private repository in the same commits;
  - a sentence about a deferred integration in the AGENTS.md intro, from `89e90eb` until its removal on 26.11.2026. It's not repo content.

  Do it last, right before the switch, so later commits can't bring in new problems:
  1. Re-run the audit on the commits made since.
  2. On a fresh clone, run `git filter-repo --replace-text <rules> --refs main`. Keep the rules file outside the repo, because it contains the path.
  3. Check that `git log -p main` no longer contains any of the removed strings.
  4. Force-push `main`.

  Testbed names in `docs/verification/` (PR numbers, collection names) were judged acceptable. Never push the local T3 checkpoint refs (`refs/t3/...`), so no `git push --mirror` or `--all`.
- **Decide the product name:** tfsaz is a working name. A rename also changes persisted names (keyring services, `TFSAZ_` variables, `~/.tfsaz`, `tfsaz-<24 hex>` alias hosts, `remote.origin.vcs=tfsaz`, link handler IDs) and needs a migration. [Naming](internals/naming.md#tfsaz-is-a-working-name) lists them.
- **License decision and CONTRIBUTING.md:** the README has only placeholders, and GitHub shows no license.
- **Low-value comment analyzer** (optional): a custom analyzer as the Go equivalent of Toolspell's `no-low-value-comments`. Pointer: Toolspell `oxlint-plugin-toolspell/rules/`.
- **Agent skills:** a testbed/VM verification skill in `.agents/skills/` (pairs with the test-environment runbook item).

## At the public / PR switch

- **PR checks:** PR title in conventional-commit form, PR size labels, PR limits and the stale bot. Pointers: Toolspell `.github/workflows/pr-title.yml`, `pr-size.yml`, `pr-limits.yml`, `stale-needs-more-info.yml`.
- **Review bots and review rules:** CodeRabbit config with path instructions, Macroscope approvability, Codex review, and a `claude.yml` workflow (actor-restricted, comment-only). Add a "Code review rules" section to AGENTS.md that maps areas to the docs owning their rules, and Macroscope check-run agents such as test-signal. Pointers: Toolspell `.coderabbit.config.ts`, `.macroscope/approvability.md`, `.macroscope/check-run-agents/`, `.github/workflows/claude.yml`.
- **Required check:** make the roll-up job `Check` of `.github/workflows/test.yml` the required check in the ruleset, not the individual jobs ([CI](operations/development.md#required-check)).
- **Repo settings and rulesets** (Toolspell's phase-1 setup):
  - squash-only merges and auto-delete of merged branches;
  - a ruleset on `main` with required checks, the strict up-to-date rule and no force push;
  - secret scanning with push protection, private vulnerability reporting and the org security configuration.
- **Security contact link:** `.github/ISSUE_TEMPLATE/config.yml` links `SECURITY.md` as the place to report vulnerabilities, and [SECURITY.md](../SECURITY.md#report-a-vulnerability) sends reporters to GitHub's private vulnerability reporting once the repository is public. Enable private vulnerability reporting at the switch, or both lead to a policy with no private channel.
- **CodeQL default setup for Go plus Copilot Autofix**, both free for public repositories.
- **Vouch and contribution triage** for outside contributors. Pointers: Toolspell `.github/workflows/vouch.yml`, `.github/VOUCHED.td`, `.agents/skills/contribution-triage/`.
- **Agent context budget** (decided): measured AGENTS.md bytes in a JSON file, a Go test that keeps it exact, an AGENTS.md rule, and a label-gated CI check. Pointers: Toolspell `scripts/agent-context-budget.json`, `scripts/agent-docs.test.ts`, `.github/workflows/agent-context-budget.yml`.

## Later

- **Release pipeline:** signed or checksummed binaries for macOS, Windows and Linux (for example with goreleaser). The README states there is no release yet. Build with a current Go patch release: `go.mod` says `go 1.26.0`, and govulncheck finds 28 standard library vulnerabilities reachable from tfsaz in Go 1.26.0 (all fixed in later patches). A `toolchain` line in `go.mod` would also move the test matrix, which runs `go.mod`'s version.
- **Skills link on Windows:** `.claude/skills` is a committed symlink, which Git checks out as a text file on Windows without `core.symlinks`, so Claude Code sees no project skills there. Let the worktree setup command (`go run ./tools/dev setup`) create a directory junction instead, and verify it on Windows.

## Code

- **Existing golangci-lint findings** (v2.15.0, `.golangci.yml`; the gates see only new ones): 225 on darwin, 209 on linux, 217 on windows. Decide per linter: fix, or disable with a reason in `.golangci.yml`. List them with `go tool -modfile=tools/golangci-lint/go.mod golangci-lint run --max-issues-per-linter=0 --max-same-issues=0 ./...` (set `GOOS` for another system).
  - `errcheck`, 149 on darwin, 3 of them in `tools/syncrepos/main.go` (unchecked `fmt.Fprintf`), 79 in tests (unchecked `fmt.Fprintf`/`io.WriteString` in test handlers). Production code: mostly deferred `Close` and best-effort `os.Remove`/`os.RemoveAll`; most in `route_proxy.go` (10), `route_system.go` (7), `setup_manager.go` and `invoke.go` (5 each).
  - `staticcheck`, 69: 67 `ST1005` (capitalized or punctuated error strings; check whether some mirror Azure CLI texts on purpose), one `QF1001`, one `QF1003`.
  - `unused`, 7 on darwin: the same symbols as the dead-code entry below.
- **Dead code** (deadcode v0.53.0 and `unused`; both depend on `GOOS`): unreachable on every system unless noted. Delete, or move to the file of the system that uses it.
  - `routeSystemLeafNames` (`route_system.go:218`, darwin and linux), `routeSystemSystemTrust` (const in `route_system_darwin.go`, `_linux.go`, `_windows.go`).
  - darwin only: `linkHandlerPaths` (`link_open.go:50`); `writeRootFile`, `reserveRoutePort`, `ownsRoutePort`, `releaseRoutePort` and the const `routePortOwner` in `route_system_unix.go`, which only linux uses.
  - linux only: `editRouteHosts` (`route_system_hosts.go:24`). windows only: `routeHostsShell` and `shellQuote` (`route_system_hosts.go:91`, `:134`), and the test helpers `routeSystemCall` and `fakeRouteSystemCommands` (`route_system_test.go:254`, `:261`).
- **Doc text that breaks the [naming rules](internals/naming.md):** `docs/user/routing.md` ("Long-running processes") says "Git starts its transport helper" for what is the Git HTTPS helper.
- **Poll in `TestDesktopLinkHandlerGIOArguments`** (`internal/az/link_export_linux_test.go`): polls a trace file every 10 ms because `gio launch` starts the receiver detached. A FIFO as the trace file would give an event: the test blocks on reading it until the receiver writes and closes. Needs a Linux run to verify. The Windows live test's polls stay on purpose; their comments say why.


## Reversibility and verify gaps

Code gaps against [Reversible and verifiable system changes](../AGENTS.md#reversible-and-verifiable-system-changes), from the audit on 26.11.2026. The user docs and [SECURITY.md](../SECURITY.md#what-tfsaz-changes-on-the-machine) describe the manual workaround for each.

- **No routing status:** `route verify` (`internal/az/route_check.go`) checks resolution and the redirect only. Nothing reports whether the OS entries are installed (LaunchAgents, systemd user and boot units, nftables table, scheduled task, trust entries, hosts lines, port reservation) or what is left after a partial setup or a restore. `doctor.go` has no routing check. Add a read-only status, ideally from `route verify` or `doctor`.
- **Routing residue:** `route restore` keeps `~/.tfsaz/route-recovery` and `<id>.pem.lock`, and a restore with nothing installed creates both (`route_managed.go:158`, gofrs/flock never deletes its file). Remove them when the last installation is gone.
- **Restore bound to the config path:** the installation ID hashes the configuration path (`route_system.go:178`). Restore under another `TFSAZ_CONFIG` prints "No installation recorded" and leaves every OS change. Let restore list and pick installations from `~/.tfsaz/route-recovery`.
- **Linux restore misses moved state:** the user unit is removed only at the current `$XDG_CONFIG_HOME` path (`route_system_linux.go:240`). NSS entries are removed only in the databases of the `<id>.nss.json` receipt; `linuxNSSDBs(true)` is never called, although its comment says restore also checks the default location (`route_system_linux.go:61`, `:309`).
- **Windows Local Machine copy blocks restore:** a copy of the leaf in the Local Machine store makes restore fail until an administrator removes it (`route_system_windows.go:373`).
- **Unsupported OS leaves the key behind:** on a system other than macOS, Linux or Windows, `route setup` writes `<config>.browser-route` (with the private key) and the recovery certificate before it fails, and `route restore` refuses to remove them (`route_managed.go`, `route_system_other.go`). Check the platform first.
- **No `shim uninstall` and no shim check:** `shim` accepts only `install` (`shim.go:46`). `doctor` checks only the `az` on its own PATH, not `git-remote-tfsaz` and not stale Windows copies (`doctor.go:54`).
- **No `profile remove`, keyring orphans not reachable:** removing a profile means editing the config. Changing a profile's ID, `collectionUrl` or `credentialVersion`, or deleting the config before `profile logout`, strands the PAT; no command lists or removes it. `profile list` does not show whether a PAT is stored (`profiles.go:306`, `:314`). Legacy profiles share the service `tfsaz`, so a logout in one config deletes the PAT of another config with the same profile ID.
- **Stale checkout records:** `repo restore` needs the clone (`setup_manager.go:1125`), so a record of a deleted or moved clone stays in `repo list` and fails `repo verify` for good. Drift makes restore refuse with no override (`setup_manager.go:811`).
- **Lock files left behind:** `<gitDir>/tfsaz-repo-setup.lock`, `<config>.lock` and `<config>.repo-setups.json.lock` survive restore and logout (`setup_manager.go:1131`, `:1136`, `profiles.go:273`).
- **`route run` overlays:** `<user config dir>/tfsaz/run/git-*` survives a kill and nothing cleans stale ones; the `tfsaz/run` directory is never removed. The path ignores `TFSAZ_CONFIG`, so manual runs with a temp config still write to the real user config directory (`route_cli.go:168`).
- **Link handlers:** no command checks an exported handler or its registration. macOS and Linux have no uninstall helper, while Windows has `Uninstall.ps1`, which leaves an empty `HKCU\Software\Toolspell` (`link_export_windows.go:123`). Whether LaunchServices on macOS keeps a record of a deleted app is unverified.
- **Managed-SSH config mode on Windows:** `repo verify` checks the `.ssh.conf` mode on macOS and Linux only (`setup_manager.go:678`).
- **`tools/dev setup` has no revert:** it sets `core.hooksPath` in the clone's shared config (`tools/dev/commands.go:210`) with no undo flag. Add `setup -undo` or document the `git config --unset core.hooksPath` step as the official one (it is in [Development](operations/development.md#pre-commit-hook)).

## Platforms

- **Check commands on Windows and Linux:** `tools/dev` (`check`, `ready`, `staged`, `setup`) and `.githooks/pre-commit` were verified only on macOS; on Linux only `ci-changes` and `ci-checks` run, in CI and act. Verify on Windows (the hook in Git for Windows' shell, CRLF checkouts, `-race` with MinGW-w64, `go tool` paths) and on Linux.
- **WSL 2 and WSL 3:** check for a platform gap. The code has no WSL-specific handling. Check:
  - T3 backend inside WSL with the browser or desktop app on Windows: which side needs `route setup`, the hosts file, the trust stores and the desktop link handler.
  - Secret Service availability for go-keyring inside WSL.
  - Git and SSH transport across the WSL/Windows boundary.
  - T3's own adapter already mentions "Windows az running in WSL" (`AzureDevOpsCli.ts`, scope detection and `@-` stdin text): check which side's `az` T3 runs in that setup.
- **Windows check of the routing key directory:** on macOS and Linux, the redirector refuses a key file readable by group or others, and setup checks the directory's mode and owner. On Windows, setup sets a protected ACL but nothing checks it later (`internal/az/route_redirect.go:83`, `routeSystemOwnedByUser` in `route_system_windows.go`).
- **macOS and Windows CI jobs locally:** `go run ./tools/dev ci-local` runs only the Linux jobs, because act runs only Linux containers. The test matrix on macOS and Windows needs another route, for example the test machines or VMs from the test-environment runbook item.
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
