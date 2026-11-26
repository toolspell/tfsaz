> [!CAUTION]
> # DO NOT USE YET
> **WORK IN PROGRESS, KNOWN REAL ISSUES.**

# TFSaz

TFSaz lets stock T3 Code work with Git repositories, pull requests and work items in an on-premises Azure DevOps Server collection. T3 needs no patch or fork: TFSaz answers the `az` calls T3 already makes.

## Why it is needed

T3 Code's Azure DevOps integration is built for Azure DevOps Services in the cloud. It enables itself only for Git remotes on `dev.azure.com` or `*.visualstudio.com`, derives a cloud organization URL from the remote, and runs Microsoft's Azure CLI (`az`) with the `azure-devops` extension against that URL. A checkout cloned from an on-premises server is not recognized, and Microsoft's Azure CLI would talk to the cloud instead of the server. TFSaz closes both gaps without changing T3. Details: [T3 compatibility](docs/internals/t3-compatibility.md#why-stock-t3-cannot-reach-azure-devops-server).

## How it works

T3 Code talks to Azure DevOps by running the `az` command. TFSaz is a single Go executable that runs as `az` on the T3 **backend's** PATH and talks directly to the configured collection over HTTPS, using the collection's REST API and a personal access token (PAT) from the OS keyring.

Stock T3 turns on its Azure DevOps features only for repositories whose remote looks like an Azure cloud URL. TFSaz therefore gives each checkout an Azure-style alias (a generated `*.visualstudio.com` alias host). T3 sees the alias; Git reaches the real server over the checkout's original HTTPS or SSH transport through the Git remote helper `git-remote-tfsaz`. Links T3 opens under the alias can be routed to the real collection, where the browser signs in as usual.

This keeps T3 unmodified and keeps every credential pointed at the real server.

## Features

- **Pull requests in T3:** create, publish, list, review diffs and policy checks, edit, change reviewers, set auto-complete, and complete with merge or squash.
- **Work items in T3:** search, show, create and edit, including state, tags and assignee, plus comments and comment reactions.
- **Repositories:** create a repository from T3 and set up existing clones, including linked worktrees, without changing how Git connects.
- **Several collections:** one ordered list of collection profiles, each with its own PAT and optional private CA.
- **Link routing:** open T3's synthetic URLs at the real collection through a local browser redirect, a desktop link handler, a Chromium extension or a Git launcher.
- **Scripting:** `az devops invoke` and `az rest` reach any REST resource the collection advertises.
- **Diagnostics:** `tfsaz doctor` checks PATH, configuration, PAT and checkout; `TFSAZ_TRACE=1` traces calls without secrets.

## Status

Early alpha. Version 0.1.0, no release and no prebuilt binaries yet: build it from source. Tested on a dedicated testbed; see the [verification records](#documentation).

## Before you use it

| Constraint | Details |
| --- | --- |
| Server | On-premises Azure DevOps Server only. Live-tested against Azure DevOps Server 2022.2; earlier versions are unverified. Azure DevOps Services (cloud) sign-in is out of scope. |
| Authentication | PAT only, stored in the OS keyring (macOS Keychain, Windows Credential Manager, Linux Secret Service). |
| T3 Code | The `az` calls follow T3's stock Azure DevOps adapter at revision `c478fde9fe`; the adapter is unchanged up to `v0.1.13-nightly.20261125.5258`. Live tests used T3 Code `v0.1.12-nightly.20261125.5230`. Other T3 versions may call `az` differently. |
| Operating systems | macOS, Windows and Linux. Tested on macOS ARM64, Windows 11 x64 and Ubuntu 26.04 ARM64. |
| Scope of `az` | Only the subset T3 uses, plus generic REST calls. No `az login` for Azure cloud and no extension installation. Keep Microsoft's Azure CLI in a separate directory if you also need it. |
| Install location | TFSaz runs on the machine that runs the T3 backend, which is the T3 server for a remote web client. |

## Documentation

| Audience | Guides |
| --- | --- |
| Operators | [Getting started](docs/user/getting-started.md) · [Install](docs/user/install.md) · [Configure collections and PATs](docs/user/configure.md) · [Set up a checkout](docs/user/checkout-setup.md) · [What works in T3](docs/user/t3-features.md) · [Diagnose](docs/user/diagnose.md) · [Route synthetic HTTPS URLs](docs/user/routing.md) · [Browser routing](docs/user/browser-routing.md) · [Manual browser routing](docs/user/browser-routing-manual.md) · [Desktop link handler](docs/user/desktop-link-handler.md) |
| Script authors | [Azure CLI compatibility and REST calls](docs/user/az-compatibility.md) |
| Maintainers and agents | [AGENTS.md](AGENTS.md) · [Architecture](docs/internals/architecture.md) · [T3 compatibility](docs/internals/t3-compatibility.md) · [REST API](docs/internals/rest-api.md) · [Testing](docs/internals/testing.md) · [Naming](docs/internals/naming.md) · [Documentation](docs/internals/documentation.md) · [Development](docs/operations/development.md) · [Issues](https://github.com/toolspell/tfsaz/issues) |
| Security | [SECURITY.md](SECURITY.md): what TFSaz protects, what is out of scope, how to report a vulnerability |
| Verification records | [Offline fixtures and native suites](docs/verification/offline-fixtures.md) · [Live T3 results, November 2026](docs/verification/live-t3-2026-11.md) · [Browser routing](docs/verification/browser-routing.md) |

## Contributing

For now only maintainers can open issues and pull requests. [CONTRIBUTING.md](CONTRIBUTING.md) has the rules that apply once that changes, and [SECURITY.md](SECURITY.md) how to report a vulnerability.

## License

[MIT](LICENSE).
