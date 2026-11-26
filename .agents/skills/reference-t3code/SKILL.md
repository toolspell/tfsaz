---
name: reference-t3code
description: Read T3 Code's stock Azure DevOps adapter, the source of tfsaz's compatibility contract, before implementing or changing a command T3 calls, an output shape or error T3 consumes, remote or URL recognition, discovery probes, or when T3 releases a new version. Not for how Microsoft's az itself behaves; use reference-azure-cli for that.
---

# Reference: T3 Code

T3's invocation of `az` is tfsaz's compatibility contract. T3 Code is a reference, not a template: read what its adapter calls and consumes, then implement that in tfsaz's own way. Never copy its code, names or feature scope.

## Get the source

- Run `go run ./tools/syncrepos --repo t3code` from the repository root. It fetches a sparse checkout of the pinned commit into the gitignored `.reference-apps/t3code` and verifies it; rerunning is cheap.
- Read T3 only there, never from another T3 checkout on this machine, which may be a fork, a nightly or modified.
- The pin (tag and commit) is the `t3code` entry in `tools/syncrepos/main.go`. It must match the revision in `docs/internals/t3-compatibility.md`.

## Files that matter

In `packages/source-control-azure-devops/src/`:

| File | Read it for |
| --- | --- |
| `shared/definition.ts` | Remote and PR URL recognition |
| `server/AzureDevOpsCli.ts` | Argument rewriting, `--detect` scope, `@-` stdin text |
| `server/AzureDevOpsPullRequestCli.ts`, `server/AzureDevOpsPullRequestProvider.ts` | The calls, JSON and exit behavior for PRs |
| `server/AzureDevOpsIssueTracker.ts` | Boards and work-item calls |
| `server/AzureDevOpsSourceControlProvider.ts` | Discovery probes (`--version`, `account show`), auth errors, install hint |
| `server/azureCliLauncher.ts` | How T3 finds `az` on Windows |

Each has a `*.test.ts` next to it; the tests show the exact arguments and expected output. `packages/source-control-core` holds the provider interfaces, and `docs/user/source-control.md` is T3's user guide.

## How to use it

1. Start at `docs/internals/t3-compatibility.md` in this repo, then the adapter file and its test.
2. Follow the arguments T3 actually passes, including explicit `--organization` and checkout-derived scope.
3. Match the shapes and errors T3 reads, not everything Microsoft's Azure CLI returns.
4. `TestStockT3Adapter` cannot run against this sparse checkout: it needs a full T3 tree with dependencies installed, passed as `TFSAZ_T3_ROOT`.

## When T3 releases

1. Find the new tag and commit with `git ls-remote --tags https://github.com/pingdotgg/t3code.git`.
2. Compare the old and new commit for `packages/source-control-azure-devops` and `packages/source-control-core`.
3. Re-pin the `t3code` entry in `tools/syncrepos/main.go`, run the sync, and follow "Keeping coverage current" in `docs/internals/t3-compatibility.md`, updating the revision there in the same commit.
