# Documentation

Where each kind of text belongs, for writing and reviewing docs, help and error texts. The rules every change needs are [Documentation rules](../../AGENTS.md#documentation-rules) in AGENTS.md; how to write names is in [Naming](naming.md).

## Where docs go

| Where | For | What belongs there |
| --- | --- | --- |
| README.md | Everyone | What TFSaz is, how it works, features, status, constraints, and the [docs index](../../README.md#documentation). |
| SECURITY.md | Everyone | What TFSaz protects and changes on the machine, scope, how to report a vulnerability. |
| `docs/user/` | Operators and script authors | How an operator does a task, in the product's voice: what a feature does, how to start, what is unexpected. No implementation details, no contributor tooling, no dated test logs (link to the record). Treat macOS, Windows and Linux equally. |
| `docs/internals/` | Maintainers and agents | Design decisions and their reasons, constraints across components, traps hard to see in the code. Before adding a paragraph, ask what a maintainer would get wrong without it. No feature lists, field lists or control-flow narration. |
| `docs/operations/` | Maintainers | Runbooks. [Development](../operations/development.md) covers dev setup, checks, CI, GitHub automation, issues and the reference checkouts. |
| `docs/verification/` | Maintainers | Dated live results, kept in full. |
| AGENTS.md, CLAUDE.md, skill frontmatter | Agents | They load in every session. What stays there and what moves out: [Agent context budget](../operations/development.md#agent-context-budget). |

## Internals pages

| Page | What it covers |
| --- | --- |
| [Architecture](architecture.md) | Authentication and routing rules, design decisions |
| [T3 compatibility](t3-compatibility.md) | Why TFSaz is needed, the adapter files, keeping coverage current |
| [REST API](rest-api.md) | Catalog routes, API version policy |
| [Testing](testing.md) | Test rules |
| [Naming](naming.md) | How to write TFSaz, the `az` shim, Microsoft's Azure CLI and the glossary terms |
