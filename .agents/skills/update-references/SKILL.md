---
name: update-references
description: Bump tfsaz's pinned reference checkouts (T3 Code, Azure CLI, the azure-devops extension, knack) to the latest upstream releases and assess whether tfsaz must follow. Use when asked to update or check the reference pins, when T3 Code or Azure CLI ships a release, or before a compatibility audit.
---

# Update the reference pins

The pins in `tools/syncrepos/main.go` fix which upstream revision tfsaz's docs, skills and audits cite. This skill moves them to the latest releases and finds out whether tfsaz must change because of it. It changes pins, docs and the backlog, never `internal/`.

Rules that frame every finding (AGENTS.md):

- **Compatibility contract:** a T3 change that alters what T3 calls or reads is a tfsaz gap, even when tfsaz itself did not change.
- **Outside in only:** never propose a change, fork or PR to T3 Code, Microsoft's Azure CLI or the extension. Findings go to `docs/backlog.md`.

## 1. Find the latest releases

```sh
go run ./tools/syncrepos -latest               # every entry
go run ./tools/syncrepos -latest --repo t3code # one entry
```

It runs `git ls-remote --tags` per entry, read-only, and prints the pinned tag next to the newest tag of each release channel with its commit and a status (`current`, `newer release`, `not pinned`).

| Entry | Channels | Which to pin |
| --- | --- | --- |
| `t3code` | `stable` (`vX.Y.Z`), `nightly` (`vX.Y.Z-nightly.DATE.BUILD`) | The latest nightly, by default. See below. |
| `azure-cli` | `azure-cli-X.Y.Z` | The latest release. |
| `azure-devops-cli-extension` | date tags `YYYYMMDD.N` | The latest release. Its version is `VERSION` in `azure-devops/azext_devops/version.py`. |
| `knack` | `vX.Y.Z` | Not the latest tag: the version the new azure-cli requires (`knack~=` in `src/azure-cli-core/pyproject.toml`). |

**Why T3 nightly by default:** a nightly is the newest code T3 will ship, so a contract change shows up there before it reaches stable, and the live verification ran on a nightly. Stable lags behind. Also diff the latest stable against the new nightly pin in the adapter paths (step 2): if they differ, tfsaz must keep working with both, and the stable side goes into the findings too. Pin stable instead only when the maintainer asks.

If every entry is `current` (and T3 stable has no adapter diff against the pin), report that and stop.

## 2. Diff old and new pins

Use a throwaway bare repository outside the worktree, so no blob outside the paths is downloaded:

```sh
tmp=$(mktemp -d) && git -C "$tmp" init -q --bare
git -C "$tmp" fetch -q --depth=1 --filter=blob:none <url> <old-commit> <new-commit>
git -C "$tmp" diff --stat <old-commit> <new-commit> -- <paths>
git -C "$tmp" diff <old-commit> <new-commit> -- <paths>
rm -rf "$tmp"
```

A GitHub compare (`/repos/OWNER/REPO/compare/OLD...NEW`) lists at most 300 files, so on a busy repository it can silently miss the adapter; prefer the local diff.

The paths are the entry's sparse paths in `tools/syncrepos/main.go` (drop the leading `/` and the `!` excludes). What to read in them:

| Entry | Paths that carry the contract |
| --- | --- |
| `t3code` | `packages/source-control-azure-devops/` (all, with tests): `src/shared/definition.ts` for hosts and URL shapes, `src/server/AzureDevOpsCli.ts`, the PR, issue and source-control providers, `src/server/azureCliLauncher.ts`. `packages/source-control-core/`. `docs/user/source-control.md`. |
| `azure-cli` | `core/_output.py`, `parser.py`, `azclierror.py`, `util.py`, `commands/__init__.py`, `__main__.py`, `command_modules/profile/` and `util/` |
| `azure-devops-cli-extension` | `azext_devops/dev/{team,repos,boards,common}/`, `devops_sdk/` client, connection and location |
| `knack` | `knack/output.py`, `query.py`, `parser.py`, `invocation.py` |

Classify every change:

| Class | Meaning | Examples |
| --- | --- | --- |
| No impact | Nothing tfsaz mirrors changed. | Refactors, tests only, telemetry, cloud sign-in, commands tfsaz does not implement. |
| tfsaz must follow | Observable behavior tfsaz reproduces changed. | T3: a new or changed `az` call, argument, `--detect` rewrite, JSON field read, exit-code or stderr handling, recognized host or URL shape, Windows launcher rule. Azure CLI, extension, knack: a changed flag, default, JSON shape, output format, error text T3 matches, or exit code of a command tfsaz implements. |
| Docs only | Only tfsaz's citations or explanations are stale. | A moved file or function cited in `docs/` or a skill, a reworded T3 user guide. |

For T3, check each changed call against the tfsaz dispatcher (`internal/az/cli.go`) and the command table in `docs/user/az-compatibility.md`. Where T3 and Microsoft's Azure CLI differ, T3 wins.

## 3. Apply

1. Update each entry in `tools/syncrepos/main.go`: tag, full 40-hex commit (the `^{}` line for annotated tags; `-latest` already peels them), and sparse paths if upstream moved a file. Run `go run ./tools/syncrepos`; it fails when a listed path no longer exists.
2. Update the revisions and versions in `docs/internals/t3-compatibility.md` (the pin table and "Why stock T3 cannot reach Azure DevOps Server", with the compare range checked) and in any skill that cites them. For a T3 bump, follow its "Keeping coverage current": the user pages and the README constraint name the revision.
3. Put every "tfsaz must follow" finding in `docs/backlog.md` under `## Code`: what changed upstream (file and function at the new pin) and where tfsaz starts (`internal/az/<file>.go`). Implement it in the same run only when it is trivial and the maintainer asked.
4. Fix "docs only" findings in the same commit.
5. Check the Go patch level, which Dependabot does not bump: if `https://go.dev/dl/?mode=json` lists a newer patch of the `go` line's minor release in `go.mod`, raise the line in a separate `build:` commit, update the Go version in the docs that state it, and run `go run ./tools/dev ready` (govulncheck must be clean).
6. Run `go run ./tools/dev check`. Commit as `build(refs): bump <entry> to <tag>` or `docs: ...` when only docs change.

`TestStockT3Adapter` cannot run against the sparse checkout. After a T3 bump, run it against a full T3 tree at the new commit with dependencies installed ("Stock T3 adapter test" in `docs/operations/development.md`); if none is available, say in the report that it did not run. Live verification against a server is a separate, maintainer-triggered step.

## 4. Report

One short table per entry, then the backlog changes:

| Entry | Old pin | New pin | Diff in tracked paths | Findings |
| --- | --- | --- | --- | --- |
| `t3code` | `v0.1.13-nightly...` (`bf961f8`) | ... | n files | no impact / must follow: ... / docs only: ... |

Also state the T3 stable tag and whether its adapter paths differ from the pin, whether `TestStockT3Adapter` ran, and what was postponed.
