# tfsaz

`tfsaz` lets stock T3 Code work with Git repositories, pull requests and work items in an on-premises Azure DevOps Server collection. It runs as `az` on the T3 **server process's** PATH and talks directly to the configured collection over HTTPS. T3 can publish repositories, open and complete PRs, show diffs and policy checks, and create, edit and comment on work items.

## Choose the setup for your environment

| Goal | Current entry point |
| --- | --- |
| Connect an installed T3 app | Use `tfsaz shim install --dir DIR`, put DIR on the backend’s PATH, and run `tfsaz init --collection URL`. |
| Use an existing clone | Run `tfsaz repo setup PATH`. New setups keep the original SSH or HTTPS transport. Use `tfsaz repo verify PATH` to check it and `tfsaz repo restore PATH` to recover the original URL offline. |
| Clone a synthetic HTTPS URL in a shell or job | Start Git under `tfsaz route run --` and configure Git credentials for the native collection. This also works when no checkout exists yet. |
| Open managed links in a browser | Run `tfsaz route setup` and `tfsaz route verify` on the host running the browser. See [the redirect guide](docs/user/browser-routing.md). |

## Documentation

| Audience | Guides |
| --- | --- |
| Operators | [Install](docs/user/install.md) · [Configure collections and PATs](docs/user/configure.md) · [Set up a checkout](docs/user/checkout-setup.md) · [Diagnose](docs/user/diagnose.md) · [Route synthetic HTTPS URLs](docs/user/routing.md) · [Browser routing](docs/user/browser-routing.md) · [Desktop link handler](docs/user/desktop-link-handler.md) · [Azure CLI compatibility](docs/user/az-compatibility.md) |
| Maintainers and agents | [Architecture](docs/internals/architecture.md) · [T3 compatibility](docs/internals/t3-compatibility.md) · [Development](docs/operations/development.md) · [AGENTS.md](AGENTS.md) |
| Verification records | [Offline fixtures and native suites](docs/verification/offline-fixtures.md) · [Live T3 results, November 2026](docs/verification/live-t3-2026-11.md) · [Browser routing](docs/verification/browser-routing.md) |
