<!-- naming-allow-file: this guideline lists the forms it rules out -->

# Naming

TFSaz runs as `az` and answers the calls T3 makes to Microsoft's Azure CLI. Three different things therefore answer to similar names: TFSaz itself, its `az` shim and Microsoft's Azure CLI. One form per name keeps docs, help texts and agents from mixing them up. The [glossary in AGENTS.md](../../AGENTS.md#glossary) defines the terms; this page says how to write them.

## Forms

| Name | Write | Not |
| --- | --- | --- |
| The product | TFSaz, in prose, headings, titles and at the start of a sentence: "TFSaz maps the alias to the collection". | tfsaz, Tfsaz, TFSAZ, "the CLI" |
| Technical names | `tfsaz`, lowercase: the binary and command (`tfsaz init`, `tfsaz.exe`), the Go module and repository URL (`github.com/toolspell/tfsaz`), file and directory names (`~/.tfsaz`, `tfsaz/config.json`), the `tfsaz` keyring service, `tfsaz-<24 hex>` alias hosts, `remote.origin.vcs=tfsaz`, link handler IDs and code identifiers. Code format where the text names the literal. | TFSaz for a literal, such as "run TFSaz init" |
| TFSaz under the name `az` | the `az` shim | the TFSaz launcher, the az wrapper |
| The command T3 runs | `az`, always in code format, only where TFSaz replaces Microsoft's Azure CLI: the `az` shim and `az ...` invocations that T3 or a user runs against TFSaz. | az in plain text, `az` for Microsoft's tool |
| Microsoft's tool | Microsoft's Azure CLI. "Azure CLI" without "Microsoft's" only where the context already names it, such as the page title "Azure CLI compatibility". | stock `az`, the real `az`, Microsoft's `az` |
| The Git remote helper | `git-remote-tfsaz`, always in code format; "the Git remote helper" when describing it | the TFSaz remote helper, the transport helper |
| The `route run` helper | the Git HTTPS helper | the remote helper (that is `git-remote-tfsaz`), the transport helper |
| Both shim names | the shims (`az` and `git-remote-tfsaz`), as `shim install` creates them | launchers, helper names |
| Environment variables | `TFSAZ_CONFIG`, `TFSAZ_TRACE`, always in code format | |
| Identifiers that must be PascalCase | `Tfsaz`, as in the Windows link handler IDs | |

The technical names are persisted on users' machines and in their checkouts, so they stay lowercase `tfsaz` even though the product name is TFSaz. Changing one needs a migration.

## Azure DevOps

| Name | Write | Not |
| --- | --- | --- |
| The on-premises product | Azure DevOps Server; "on-premises" as the adjective | TFS, on-prem, ADO |
| The cloud product | Azure DevOps Services; "(cloud)" as a gloss is fine | Azure DevOps cloud, VSTS |
| Both, the REST API or the web UI | Azure DevOps, as in "T3's Azure DevOps adapter" or "Azure DevOps returned HTTP 500" | |
| The server-side unit TFSaz talks to | collection, the real collection URL | organization |
| The cloud-side unit | organization: an Azure DevOps Services organization, T3's `--organization` argument, or the profile's `organization` field | organization for the collection, organization for a company ("your network" instead) |

TFS (Team Foundation Server) is the server's former name. Use it only in literal identifiers such as the `X-TFS-FedAuthRedirect` header.

## T3

| Name | Write |
| --- | --- |
| The product | T3 Code on first mention in a page's introduction, T3 afterwards |
| Unmodified T3 | stock T3, when the point is that T3 needs no patch or fork |
| The process that runs `az` | the T3 backend: the desktop app's backend or the T3 server |
| The desktop app | T3 Desktop |
| A remote backend for the web client | the T3 server (`t3 serve`) |
| The source repository | `t3code`, only for the repository or a path in it |

## Aliases and URLs

| Name | Write | Not |
| --- | --- | --- |
| The `*.visualstudio.com` name T3 recognizes and TFSaz maps to a collection | Azure-style alias; the alias after first mention | organization alias, Azure-style organization |
| A generated hostname `tfsaz-<24 hex>.visualstudio.com` | alias host | synthetic host, synthetic hostname, synthetic name |
| A URL on an alias host | synthetic URL; for a remote, the synthetic origin | Azure-style URL, Azure-style link |
| The repository's real HTTPS or SSH URL | native URL | real URL, on-prem URL |

## Where the rules apply

- Docs, AGENTS.md, CLI help and error texts, code comments, and the labels TFSaz writes into the OS: link handler names, the browser extension name, service and task descriptions.
- Dated records in `docs/verification/` keep their wording unless a fix changes no fact.
- Quote other projects' names as they write them, such as T3's "Open on Azure DevOps" button.
- Strings that a newer TFSaz must read back from an older one keep their spelling even when it breaks a rule here, such as the `tfsaz hosts undo 1` journal header. Changing one needs a migration.

## The naming check

`TestNaming` (`naming_test.go`) checks every text file Git tracks or would track, except `docs/verification/`. It runs in `go run ./tools/dev check` and `ready`, in the commit hook and in CI. It reports:

- Misspellings of the name: any case other than TFSaz, `tfsaz`, `TFSAZ_` in environment variables and `Tfsaz` inside a PascalCase identifier, and the name split by a space, hyphen or underscore.
- A lowercase `tfsaz` in Markdown prose outside code format.
- A lowercase `tfsaz` in code, config and code blocks unless it is part of a path, host, URL or identifier, a whole quoted literal, the `tfsaz:` diagnostic prefix, or followed by a command, an option or a version. Everything else reads as the product name.
- TFSaz followed by a command, such as "run TFSaz init".
- The forms in the "Not" columns above that a pattern finds reliably: TFS, on-prem, VSTS, Azure DevOps cloud, organization alias, synthetic host, Azure-style URL, real URL, stock `az`, the TFSaz launcher, the az wrapper and "the CLI".

A deliberate exception carries its reason: `naming-allow: <reason>` on the line or the line above, or `naming-allow-file: <reason>` in a file's first five lines, as this page does.
