# T3 compatibility

T3's invocation of `az` is the compatibility contract. This page records where that contract is defined and how to keep tfsaz aligned with it.

## References

Read reference code only from the gitignored `.reference-apps/` folder, synced with `go run ./tools/syncrepos` ([Development](../operations/development.md#reference-checkouts)). Each entry is a sparse checkout of a pinned commit, so file paths and citations stay valid until someone re-pins. Never read it from another checkout or installation on the machine, which may be a fork or at another revision.

| Entry | Pin | Role |
| --- | --- | --- |
| `t3code` | `v0.1.13-nightly.20261125.5258` (`bf961f8121`), the revision below | The compatibility contract: what T3 calls and consumes. Skill: `reference-t3code`. |
| `azure-cli` | `azure-cli-2.93.0` | Behavioral reference for `az`: command surface, parsing, output, errors and exit codes. Skill: `reference-azure-cli`. |
| `azure-devops-cli-extension` | `20261117.1` (1.0.12) | Behavioral reference for the DevOps commands, `devops invoke`, connection data and identity lookups. Skill: `reference-azure-cli`. |
| `knack` | `v0.14.0`, the version azure-cli pins | The CLI framework under azure-cli: `-o` formats, `--query` and parser exit codes. Skill: `reference-azure-cli`. |

They are references, not templates: tfsaz reproduces observable behavior and copies no code. Where T3's calls and Microsoft's Azure CLI differ, T3 wins.

In T3's `packages/source-control-azure-devops` package, start with these files and their tests:

| File | What it defines |
| --- | --- |
| `src/shared/definition.ts` | Remote and PR URL recognition |
| `src/server/AzureDevOpsCli.ts` | Argument rewriting |
| `src/server/AzureDevOpsPullRequestCli.ts` and `src/server/AzureDevOpsPullRequestProvider.ts` | The JSON and exit behavior T3 consumes |
| `src/server/AzureDevOpsSourceControlProvider.ts` | The discovery probes (`--version`, `account show`) and the install hint |
| `src/server/azureCliLauncher.ts` | How T3 finds `az` on Windows |

T3's source-control user guide is `docs/user/source-control.md` in that checkout. The extension's `azure-devops/azext_devops/dev/common/identities.py` illustrates the collection connection-data lookup.

`TestStockT3Adapter` cannot use the sparse checkout: it needs a full T3 source tree with its dependencies installed, passed as `TFSAZ_T3_ROOT` ([Development](../operations/development.md#stock-t3-adapter-test)).

## Why stock T3 cannot reach Azure DevOps Server

Checked against T3 Code `v0.1.13-nightly.20261125.5258` (commit `bf961f8121c6336915ecf7b3f97ce6e68ab38fd7`). The upstream compare from `c478fde9fe` to that commit (75 commits) touches no file in `source-control-azure-devops` or `source-control-core`, so this holds for both revisions. Live verification ran on `v0.1.12-nightly.20261125.5230` (commit `ca81b7b`); see the [live results](../verification/live-t3-2026-11.md).

How the stock adapter works:

- **Remote recognition.** `recognizeRemote` in `definition.ts` accepts only hostnames `dev.azure.com`, `*.dev.azure.com` and `*.visualstudio.com`. Its test expects `https://azure.example/o/p/_git/r` to be rejected. A checkout whose origin is an Azure DevOps Server gets no Azure provider, so T3 shows no PRs or work items for it.
- **Link parsing is broader.** PR and commit URL parsing accepts any hostname with a `_git` route and keeps a non-default port ("Azure DevOps Server runs on any hostname"). Only remote recognition is cloud-only.
- **Scope from the remote.** For `--detect true` calls, `AzureDevOpsCli.ts` reads `git remote -v` and rewrites the call to `--detect false --organization URL`, plus `--project` and `--repository` where the command accepts them. `scopeFromRemoteUrl` accepts only `https://dev.azure.com/{org}/{project}/_git/{repo}`, `https://{org}.visualstudio.com/[DefaultCollection/]{project}/_git/{repo}` and the `ssh.dev.azure.com` / `vs-ssh.visualstudio.com` `v3/` SSH forms. The organization it passes is always a cloud URL. An on-premises path such as `/tfs/Collection/Project/_git/Repo` does not parse.
- **The `az` executable.** T3 runs `az` from the backend's PATH with the `azure-devops` extension: discovery runs `az --version` and `az account show --query user.name -o tsv`, and the missing-CLI hint is `az extension add --name azure-devops`. On Windows, if PATH resolves to an `az.cmd` or `az.bat` launcher of a recognized MSI or ZIP install, T3 runs that install's `python.exe` directly; any other `az`, such as the tfsaz `az.exe`, runs as is.
- **Authentication errors.** T3 classifies stderr containing `az devops login` or `az login` as an authentication failure and tells the user to run `az devops login`. A failed discovery probe without its own message falls back to "Run `az login`".
- **Text arguments.** `az` reads a value starting with `@` from a file. T3 therefore passes free text as `--option=@-` on stdin and rejects remote-derived scope parts starting with `@`.
- **Links.** T3 prefers the web URL the `az` output returns. Without one, it builds PR and commit URLs from the remote, using the canonical `dev.azure.com` form for cloud hosts. Publishing always targets `dev.azure.com`.

So with Microsoft's Azure CLI, an on-premises checkout is either not recognized, or every call is scoped to a cloud organization that is not the server.

How tfsaz bridges each gap:

| Gap | tfsaz answer |
| --- | --- |
| On-premises remote not recognized | `repo setup` sets an Azure-style origin on a generated `tfsaz-<24 hex>.visualstudio.com` host, which `recognizeRemote` and `scopeFromRemoteUrl` accept. |
| Calls scoped to a cloud organization | tfsaz maps the organization URL T3 passes (or the checkout's alias) to a configured profile and sends the request to that profile's real collection URL. |
| Git transport | The `git-remote-tfsaz` helper sends Git to the native HTTPS or SSH URL, so the synthetic origin never reaches the network. |
| `az account show` and sign-in | tfsaz answers with the login the collection's PAT authenticates as, and `devops login` stores an Azure DevOps Server PAT. |
| Extension and Python install | tfsaz is one executable named `az`; no Python, extension or `az login` is involved. |
| Links on the alias host | tfsaz returns web links under the same Azure-style alias, and routing ([redirector, link handler, extension](architecture.md#routing-compared-with-a-proxy)) takes the browser to the real collection. |

## Adding a command

- Follow the actual CLI calls the adapter makes and the API versions the target server supports.
- Match the adapter's output shapes and errors, using fixtures stripped of identities, internal URLs and secrets.
- Cover T3's explicit organization flags as well as checkout-derived scope.
- T3 groups some viewer reads by host. Verify “my PRs” with distinct recognized host aliases.

## Keeping coverage current

The user page [What works in T3](../user/t3-features.md) states what the stock adapter can do with tfsaz, and the [command table](../user/az-compatibility.md#commands-t3-calls) lists the implemented calls. Both are tied to the T3 revision above.

- When moving to a newer T3 revision, repeat the audit: compare the stock source-control, PR and issue providers and their tests with the tfsaz dispatcher, then update both pages, the T3 constraint in the [README](../../README.md#before-you-use-it), the revision and the `t3code` pin in `tools/syncrepos/main.go`.
- Record new live checks in [`docs/verification/`](../verification/), not on the user pages.
