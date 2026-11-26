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
- **Naming guidelines:** check or create them. tfsaz stands for itself but has no product name yet, and it shims the `az` CLI command, so a guideline on how to write and refer to tfsaz, the `az` shim, `git-remote-tfsaz` and Microsoft's Azure CLI is important. Pointers: Toolspell `docs/internals/naming.md`, `scripts/check-naming.ts`.
- **License decision and CONTRIBUTING.md:** the README has only placeholders, and GitHub shows no license.
- **SECURITY.md:** threat model and reporting path, covering PAT handling, the redirect listener and the hosts and trust-store changes. The repo has none. Pointer: Toolspell `SECURITY.md`.
- **CI path filter:** skip the 3-OS test matrix (about 5 minutes) on docs-only changes, using affected detection. Pointer: Toolspell `scripts/ci-changes.ts`.
- **Secrets scan** in the pre-commit hook and CI; PATs are the core risk. Pointer: Toolspell `scripts/check-secrets.ts`.
- **Pinned-actions check:** the workflow actions are SHA-pinned today, but nothing enforces it. Pointer: Toolspell `scripts/check-pinned.ts`.
- **Go lint and safety checks:** golangci-lint, govulncheck and a `go mod tidy` diff check, as the Go equivalent of Toolspell's lint and typecheck steps. Lint rules:
  - `nolintlint`, so every `//nolint` carries a reason (like Toolspell's `require-suppression-reason`);
  - an em-dash check for markdown (like `no-em-dash-copy`);
  - optional: a custom analyzer as the Go equivalent of `no-low-value-comments`.

  Pointers: Toolspell `oxlint-plugin-toolspell/rules/`.
- **Dead-code check:** `deadcode` (`golang.org/x/tools/cmd/deadcode`) for unreachable functions, the `unused` linter in golangci-lint, and `go mod tidy` for dependencies. Worth it for roughly 23,000 agent-written lines. Toolspell uses knip for unused files, exports and dependencies.
- **actionlint** for the workflow files. Pointer: Toolspell `vp run actionlint`.
- **Local CI**, in three parts as in Toolspell:
  - **Workflow run:** run the real workflow locally with nektos/act, optionally on a remote Docker context (Toolspell `vp run ci:local`, `scripts/ci-local.ts`). A green run is evidence, not proof. act runs only Linux containers, so the macOS and Windows jobs of `.github/workflows/test.yml` need another route, for example the test machines or VMs from the test-environment runbook item.
  - **Checks before a commit or push:** one fast, affected-only entry point (gofmt, `go vet`, a linter, `go test` on affected packages) that agents run before committing, and a `ready` entry point next to it for the full local suite before a push. Document both in `docs/operations/development.md` and the AGENTS.md "Verify" section. Pointers: Toolspell `scripts/check-changed.ts` (`vp run check:changed`, merge base with `main`, uncommitted and untracked files included) and `ready` in `package.json`.
  - **Pre-commit hook:** a versioned `.githooks/pre-commit`, enabled with `git config core.hooksPath .githooks`, running `gofmt -l`, `go vet`, `gitleaks --staged` and the em-dash check on staged files. Pointers: Toolspell `.vite-hooks/pre-commit`, which runs `vp staged` with the `staged:` list in `vite.config.ts` (format, lint, naming, pinned actions, gitleaks `--staged`).
- **Agent skills:** keep skills in `.agents/skills/`, linked to `.claude/skills`, as Toolspell does. Candidates: a `reference-t3code` skill (pairs with the reference-repo item) and a testbed/VM verification skill (pairs with the test-environment runbook item).
- **Doc link and anchor checker** for README, AGENTS.md and `docs/`. The docs-overhaul threads used throwaway versions of one.

## At the public / PR switch

- **PR checks:** PR title in conventional-commit form, PR size labels, PR limits and the stale bot. Pointers: Toolspell `.github/workflows/pr-title.yml`, `pr-size.yml`, `pr-limits.yml`, `stale-needs-more-info.yml`.
- **Review bots and review rules:** CodeRabbit config with path instructions, Macroscope approvability, Codex review, and a `claude.yml` workflow (actor-restricted, comment-only). Add a "Code review rules" section to AGENTS.md that maps areas to the docs owning their rules, and Macroscope check-run agents such as test-signal. Pointers: Toolspell `.coderabbit.config.ts`, `.macroscope/approvability.md`, `.macroscope/check-run-agents/`, `.github/workflows/claude.yml`.
- **Dependabot** for GitHub Actions and Go modules, plus the labels workflow. Pointers: Toolspell `.github/dependabot.yml`, `.github/workflows/labels.yml`.
- **Issue and PR templates:** bug, feature and config issue templates and a PR template (Problem / Change / Verification). Pointers: Toolspell `.github/ISSUE_TEMPLATE/`, `.github/pull_request_template.md`.
- **Repo settings and rulesets** (Toolspell's phase-1 setup):
  - squash-only merges and auto-delete of merged branches;
  - a ruleset on `main` with required checks, the strict up-to-date rule and no force push;
  - secret scanning with push protection, private vulnerability reporting and the org security configuration.
- **CodeQL default setup for Go plus Copilot Autofix**, both free for public repositories.
- **Vouch and contribution triage** for outside contributors. Pointers: Toolspell `.github/workflows/vouch.yml`, `.github/VOUCHED.td`, `.agents/skills/contribution-triage/`.
- **Agent context budget** (decided): measured AGENTS.md bytes in a JSON file, a Go test that keeps it exact, an AGENTS.md rule, and a label-gated CI check. Pointers: Toolspell `scripts/agent-context-budget.json`, `scripts/agent-docs.test.ts`, `.github/workflows/agent-context-budget.yml`.

## Later

- **Release pipeline:** signed or checksummed binaries for macOS, Windows and Linux (for example with goreleaser). The README states there is no release yet.
- **Worktree setup script**, run as the T3 project's "Setup Worktree" script when a worktree is created, so new agent worktrees are ready to use. For Go probably just `go mod download` plus enabling the hooks path. Pointer: Toolspell `scripts/setup-worktree.ts`.

## Code

- **Real keyring in managed-SSH setup:** `internal/az/setup_route.go:217` reads the system keyring when managed-SSH `repo setup` runs on an HTTPS origin without `--ssh-url`. Tests avoid it only because they use `ssh://` origins.

## Platforms

- **WSL 2 and WSL 3:** check for a platform gap. The code has no WSL-specific handling. Check:
  - T3 backend inside WSL with the browser or desktop app on Windows: which side needs `route setup`, the hosts file, the trust stores and the desktop link handler.
  - Secret Service availability for go-keyring inside WSL.
  - Git and SSH transport across the WSL/Windows boundary.
  - T3's own adapter already mentions "Windows az running in WSL" (`AzureDevOpsCli.ts`, scope detection and `@-` stdin text): check which side's `az` T3 runs in that setup.
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
- **Reference repos in a dotfolder:** adopt Toolspell's approach of pinned, read-only reference checkouts in a gitignored dotfolder (Toolspell: `.reference-apps/` synced by `scripts/sync-repos.ts`, each entry with a pinned commit and sparse-checkout exclusions; its AGENTS.md says to read reference code only from there, never from another checkout on the machine). Candidates: `pingdotgg/t3code` (pinned to the commit `docs/internals/t3-compatibility.md` names), `Azure/azure-cli`, `Azure/azure-devops-cli-extension`. Then point AGENTS.md and `t3-compatibility.md` at that folder instead of "a T3 Code checkout" and GitHub links.
- **Structure-phase rule:** remove the structure-phase no-code-changes rule from AGENTS.md once the repo setup is done.
