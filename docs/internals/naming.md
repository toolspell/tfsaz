# Naming

tfsaz runs as `az` and answers the calls T3 makes to Microsoft's Azure CLI. Three different things therefore answer to similar names: tfsaz itself, its `az` shim and Microsoft's Azure CLI. One form per name keeps docs, help texts and agents from mixing them up. The [glossary in AGENTS.md](../../AGENTS.md#glossary) defines the terms; this page says how to write them.

## tfsaz is a working name

"tfsaz" is a working name; the product name is undecided ([backlog](../backlog.md#before-going-public)). Until then, write tfsaz as below. A rename touches more than text: the keyring service names, the `TFSAZ_` variables, `~/.tfsaz`, the generated `tfsaz-<24 hex>` alias hosts, `remote.origin.vcs=tfsaz` in managed checkouts and the link handler IDs are all persisted.

## Forms

| Name | Write | Not |
| --- | --- | --- |
| The tool | tfsaz, lowercase even at the start of a sentence or in a heading. Plain text for the tool, code format for a command line or literal: `tfsaz doctor`, the `tfsaz` keyring service, `tfsaz.exe`. | Tfsaz, TFSAZ, `tfsaz` as the plain product name, "the CLI" |
| tfsaz under the name `az` | the `az` shim | the tfsaz launcher, the az wrapper |
| The command T3 runs | `az`, always in code format. Bare `az` means whatever answers on that PATH; say which when it matters. | az in plain text |
| Microsoft's tool | Microsoft's Azure CLI. "Azure CLI" without "Microsoft's" only where the context already names it, such as the page title "Azure CLI compatibility". | stock `az`, the real `az`, Microsoft's `az` |
| The Git remote helper | `git-remote-tfsaz`, always in code format; "the Git remote helper" when describing it | the tfsaz remote helper, the transport helper |
| The `route run` helper | the Git HTTPS helper | the remote helper (that is `git-remote-tfsaz`) |
| Both shim names | the shims (`az` and `git-remote-tfsaz`), as `shim install` creates them | launchers, helper names |
| Environment variables | `TFSAZ_CONFIG`, `TFSAZ_TRACE`, always in code format | |
| Identifiers that must be PascalCase | `Tfsaz`, as in the Windows link handler IDs | |

## Azure DevOps

| Name | Write | Not |
| --- | --- | --- |
| The on-premises product | Azure DevOps Server; "on-premises" as the adjective | TFS, on-prem, ADO |
| The cloud product | Azure DevOps Services; "(cloud)" as a gloss is fine | Azure DevOps cloud, VSTS |
| Both, the REST API or the web UI | Azure DevOps, as in "T3's Azure DevOps adapter" or "Azure DevOps returned HTTP 500" | |
| The server-side unit tfsaz talks to | collection, the real collection URL | organization |
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
| The `*.visualstudio.com` name T3 recognizes and tfsaz maps to a collection | Azure-style alias; the alias after first mention | organization alias, Azure-style organization |
| A generated hostname `tfsaz-<24 hex>.visualstudio.com` | alias host | synthetic host, synthetic hostname, synthetic name |
| A URL on an alias host | synthetic URL; for a remote, the synthetic origin | Azure-style URL, Azure-style link |
| The repository's real HTTPS or SSH URL | native URL | real URL, on-prem URL |

## Where the rules apply

- Docs, AGENTS.md, CLI help and error texts. Known exceptions are listed in the [backlog](../backlog.md#code).
- Dated records in `docs/verification/` keep their wording unless a fix changes no fact.
- Quote other projects' names as they write them, such as T3's "Open on Azure DevOps" button.
