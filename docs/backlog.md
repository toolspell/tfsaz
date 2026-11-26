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
- **License and contribution policy:** the README has placeholders for both.
- **Secrets scan** in the pre-commit hook and CI; PATs are the core risk. Pointer: Toolspell `scripts/check-secrets.ts`.
- **Pinned-actions check:** the workflow actions are SHA-pinned today, but nothing enforces it. Pointer: Toolspell `scripts/check-pinned.ts`.
- **Go lint and safety checks:** golangci-lint, govulncheck and a `go mod tidy` diff check, as the Go equivalent of Toolspell's lint and typecheck steps.
- **Doc link and anchor checker** for README, AGENTS.md and `docs/`. The docs-overhaul threads used throwaway versions of one.

## At the public / PR switch

- **PR checks:** PR title in conventional-commit form, PR size labels, PR limits and the stale bot. Pointers: Toolspell `.github/workflows/pr-title.yml`, `pr-size.yml`, `pr-limits.yml`, `stale-needs-more-info.yml`.
- **Review bots:** CodeRabbit config with path instructions, Macroscope approvability, Codex review, and a `claude.yml` workflow (actor-restricted, comment-only). Pointers: Toolspell `.coderabbit.config.ts`, `.macroscope/approvability.md`, `.github/workflows/claude.yml`.
- **Dependabot** for GitHub Actions and Go modules, plus the labels workflow. Pointers: Toolspell `.github/dependabot.yml`, `.github/workflows/labels.yml`.
- **Agent context budget** (decided): measured AGENTS.md bytes in a JSON file, a Go test that keeps it exact, an AGENTS.md rule, and a label-gated CI check. Pointers: Toolspell `scripts/agent-context-budget.json`, `scripts/agent-docs.test.ts`, `.github/workflows/agent-context-budget.yml`.

## Code

- **Real keyring in managed-SSH setup:** `internal/az/setup_route.go:217` reads the system keyring when managed-SSH `repo setup` runs on an HTTPS origin without `--ssh-url`. Tests avoid it only because they use `ssh://` origins.
- **Stale doc references in CLI texts** (for the planned code-reference scan):
  - `internal/az/help.go:204` points at the removed `docs/route-redirect.md`; the content is now in `docs/user/browser-routing.md`.
  - `internal/az/help.go:207` points at a README section on the loopback redirect service that no longer exists; the content is now in `docs/user/browser-routing.md` and `docs/user/browser-routing-manual.md`.
  - `internal/az/route_redirect.go:153` says "as the README describes" about the port-443 redirect; that text is now in `docs/user/browser-routing-manual.md#5-run-the-service-on-port-443`.

## Platforms

- **WSL 2 and WSL 3:** check for a platform gap. The code has no WSL-specific handling. Check:
  - T3 backend inside WSL with the browser or desktop app on Windows: which side needs `route setup`, the hosts file, the trust stores and the desktop link handler.
  - Secret Service availability for go-keyring inside WSL.
  - Git and SSH transport across the WSL/Windows boundary.
  - T3's own adapter already mentions "Windows az running in WSL" (`AzureDevOpsCli.ts`, scope detection and `@-` stdin text): check which side's `az` T3 runs in that setup.

## Tooling

- **Local CI**, in two parts as in Toolspell:
  - Run the real workflow locally with nektos/act, optionally on a remote Docker context (Toolspell `vp run ci:local`, `scripts/ci-local.ts`). A green run is evidence, not proof. act runs only Linux containers, so the macOS and Windows jobs of `.github/workflows/test.yml` need another route, for example the test machines or VMs from the test-environment runbook item.
  - One fast, affected-only check entry point (gofmt, `go vet`, a linter, `go test` on affected packages) that agents run before committing, plus a pre-commit hook for the fast checks. Document it in `docs/operations/development.md` and the AGENTS.md "Verify" section. Pointers: Toolspell `scripts/check-changed.ts` (`vp run check:changed`, merge base with `main`, uncommitted and untracked files included) and the `staged` hook list in `vite.config.ts` (`.vite-hooks/pre-commit`).

## Docs and process

- **Test environment runbook:** document how to set up the environments used for verification (an Azure DevOps Server testbed with two collections, macOS, Windows 11 and Ubuntu machines or VMs, credential stores, T3 install) as a runbook in `docs/operations/`. Starting points:
  - `docs/operations/development.md`: T3 adapter test prerequisites, Windows logon with a loaded profile for Credential Manager, an unlocked Secret Service collection on Linux, the live system test variables.
  - `docs/verification/live-t3-2026-11.md`: server version 2022.2, two collections on one server (one name with a space) with distinct aliases, T3 Nightly with an isolated data directory, GNOME Keyring session on Ubuntu, password-backed Windows logon, explicitly trusted testbed CA, Git for Windows' OpenSSH.
  - `docs/verification/browser-routing.md`: disposable browser profiles, Windows over SSH versus RDP, `t3 browser setup` for AppArmor on Ubuntu.
- **Reference repos in a dotfolder:** adopt Toolspell's approach of pinned, read-only reference checkouts in a gitignored dotfolder (Toolspell: `.reference-apps/` synced by `scripts/sync-repos.ts`, each entry with a pinned commit and sparse-checkout exclusions; its AGENTS.md says to read reference code only from there, never from another checkout on the machine). Candidates: `pingdotgg/t3code` (pinned to the commit `docs/internals/t3-compatibility.md` names), `Azure/azure-cli`, `Azure/azure-devops-cli-extension`. Then point AGENTS.md and `t3-compatibility.md` at that folder instead of "a T3 Code checkout" and GitHub links.
- **Structure-phase rule:** remove the structure-phase no-code-changes rule from AGENTS.md once the repo setup is done.
