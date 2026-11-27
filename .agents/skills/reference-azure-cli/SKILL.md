---
name: reference-azure-cli
description: Check Microsoft's Azure CLI, its azure-devops extension and knack before changing argument parsing, --query, -o output, error texts or exit codes, account show, rest, devops invoke, devops login, devops team, repos, repos pr, boards work-item, or connection-data and identity lookups.
---

# Reference: Azure CLI and the DevOps extension

TFSaz answers as `az`, so it mimics Microsoft's Azure CLI and its `azure-devops` extension: their command surface, argument parsing, output formats, errors and exit codes, and some internals such as the connection-data and identity lookups. These are behavioral references, not templates: implement the behavior in Go, in TFSaz's own way, and copy no code. Where T3's adapter (skill `reference-t3code`) and Microsoft's Azure CLI differ, T3's actual calls win.

## Get the source

- Run `go run ./tools/syncrepos --repo azure-cli`, `--repo azure-devops-cli-extension` and `--repo knack` from the repository root, or `go run ./tools/syncrepos` for all entries. Each lands as a verified sparse checkout of its pinned release in the gitignored `.reference-apps/`.
- Read them only there, never from an installed Azure CLI or another checkout on this machine.
- Pins and sparse paths are in `tools/syncrepos/main.go`. Test directories are excluded.

## Where to look

| Behavior | Path in `.reference-apps/` |
| --- | --- |
| `-o json/jsonc/tsv/table/yaml/none`, `--query` JMESPath | `knack/knack/output.py`, `knack/knack/query.py`, `knack/knack/invocation.py`; Azure overrides in `azure-cli/src/azure-cli-core/azure/cli/core/_output.py` and `commands/__init__.py` |
| Argument parsing, exit code 2 for usage errors | `knack/knack/parser.py`, `azure-cli/src/azure-cli-core/azure/cli/core/parser.py` |
| Error classes and exit codes | `azure-cli/src/azure-cli-core/azure/cli/core/azclierror.py`, `util.py` (`handle_exception`), `azure-cli/src/azure-cli/azure/cli/__main__.py` |
| `az rest` | `azure-cli/.../command_modules/util/` and `send_raw_request` in `core/util.py` |
| `az account show` | `azure-cli/.../command_modules/profile/`, `core/_profile.py` |
| `devops invoke`, `devops login`, `devops team` | `azure-devops-cli-extension/azure-devops/azext_devops/dev/team/` (`invoke.py`, `credentials.py`, `team.py`) |
| `repos`, `repos pr`, policies | `.../azext_devops/dev/repos/` |
| `boards work-item` | `.../azext_devops/dev/boards/` |
| Organization detection, connection data, identities, PAT store | `.../azext_devops/dev/common/` (`services.py`, `identities.py`, `git.py`, `uri.py`, `_credentials.py`) |
| REST client, resource-area and options lookup | `.../azext_devops/devops_sdk/` (`client.py`, `connection.py`, `v5_0/location/`) |

Each command module's `commands.py` and `arguments.py` define the public command names and options; `_help.py` has the documented examples.

## How to use it

1. Find the behavior in the table, read it, then check what T3 actually needs.
2. Reproduce observable behavior: flags, defaults, JSON shapes, output formatting, stderr wording T3 matches, and exit codes. Cloud-only paths (Azure sign-in, extension install, telemetry) are out of scope.
3. When a path is missing from the sparse checkout, add it to the entry in `tools/syncrepos/main.go`. Keep the set small.

## New releases

Re-pin deliberately, for example when the behavior TFSaz mirrors changed upstream. The skill `update-references` runs the whole bump for all entries, including the impact check. In short:

1. Find the latest release tag and its commit with `git ls-remote --tags <url>` (azure-cli tags are `azure-cli-X.Y.Z`; the extension uses date tags). Pin knack to the version azure-cli's `src/azure-cli-core/pyproject.toml` requires.
2. Update the entry, run the sync; it fails if a sparse path no longer exists.
