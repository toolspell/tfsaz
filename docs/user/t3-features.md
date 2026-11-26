# What works in T3

This page lists which T3 Code features work with TFSaz at T3 revision `c478fde9fe`, the revision of the stock Azure DevOps adapter TFSaz follows. The `az` commands behind them are listed in [Azure CLI compatibility](az-compatibility.md#commands-t3-calls).

| Feature | What works or is missing |
| --- | --- |
| Discovery, project recognition and link matching | Version and account probes work. PR web URLs preserve the recognized checkout identity. The loopback redirect opens supported pages at the real collection in a browser. Native desktop handlers and the Chromium extension are also available. |
| Clone, branch and checkout | `repos show` supplies clone URLs and default branch. T3 finds a branch's PR with `repos pr list`. For same-repo PR checkout it reads `repos pr show` and uses Git fetch/switch. PR templates also use local Git. T3 rejects fork checkout. The displayed shell command `az repos pr checkout` also works for same-repository PRs. |
| Create or publish | Supports `repos create` and `repos pr create`, including draft PR creation. |
| PR board, preview, detail, linked thread and checks | List/show and paginated policy reads work. T3 can show authors, reviewers, body, state, policy checks and the labels included in the PR response. Linked summaries use those same reads for refresh. |
| PR activity, labels and code | The supported Git resources are `pullRequestThreads`, `pullRequestLabels`, `pullRequestIterations`, `pullRequestIterationChanges`, `blobs` and `items`. The stock adapter uses them for comment history, label reads, head revision, changed-file count, patches and file contents. |
| Lifecycle, editing and reviewers | Supports merge and squash, auto-complete, ready/draft, abandon/reopen, title/body edits, and adding or removing requested reviewers. Stock T3's Azure provider already disables PR comment writes, review submission, thread replies/resolution, reactions, text search and reviewer candidate discovery. |
| Work-item lists, search, detail and history | Supports `boards work-item show` and the `wit` reads for `workitemtypes`, `wiql`, `workitemsbatch`, `comments` and `updates`. Linked PR details use `repos pr show`. |
| Work-item changes and candidates | T3 can list project tags and sample team members as assignee candidates. The stock issue tracker creates and updates work items with the dedicated Boards commands. It posts comments and toggles their reactions through catalog-resolved invoke. |

## How this was checked

| Area | Checked by |
| --- | --- |
| Commands and output shapes | An audit of T3's stock source-control, PR and issue providers and their tests against the TFSaz dispatcher, plus the [offline adapter fixture](../verification/offline-fixtures.md). |
| Clone transports, the six PR activity resources, PR and Boards writes | Exercised live. The live PR and Boards write checks used the macOS backend against both collections. Windows and Ubuntu ran the write regression fixtures and live Git, worktree and browser-routing checks. See [Live T3 results](../verification/live-t3-2026-11.md). |
| PR templates and same-repository checkout | Upstream T3 code and tests only. |
| Watch notifications, mobile surfaces and agent tools | Not independently exercised end to end. |
