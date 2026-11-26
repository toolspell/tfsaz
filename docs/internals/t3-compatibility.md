# T3 compatibility

## References

- Use the sibling `../t3code` checkout as the source and documentation reference. The initial investigation used commit `c478fde9fe`. Check the actual adapter and its tests when implementing a command.
- Start with `packages/source-control-azure-devops/src/shared/definition.ts` for remote and PR URL recognition, `src/server/AzureDevOpsCli.ts` in that package for argument rewriting, and `src/server/AzureDevOpsPullRequestCli.ts` and `AzureDevOpsPullRequestProvider.ts` for the JSON and exit behavior T3 consumes. The source-control user guide is `docs/user/source-control.md`.
- Local sibling checkouts `../azure-cli` and `../azure-devops-cli-extension` are behavioral references. The extension's `azure-devops/azext_devops/dev/common/identities.py` illustrates the collection connection-data lookup.

## Feature coverage at this T3 revision

The command table above lists implemented commands. The table below shows how the stock adapter uses those commands and which capabilities its Azure provider exposes. The audit compared the stock source-control, PR and issue providers and their tests with the CLI dispatcher. The live PR and Boards write checks used the macOS backend against both collections. Windows and Ubuntu also ran the write regression fixtures and live Git, worktree and browser-routing checks described above.

| Feature | What works or is missing |
| --- | --- |
| Discovery, project recognition and link matching | Version and account probes work. PR web URLs preserve the recognized checkout identity. The loopback redirect opens supported pages at the real collection in a browser. Native desktop handlers and the Chromium extension are also available. |
| Clone, branch and checkout | `repos show` supplies clone URLs and default branch. T3 finds a branch's PR with `repos pr list`. For same-repo PR checkout it reads `repos pr show` and uses Git fetch/switch. PR templates also use local Git. Clone transports were exercised live. Template and checkout paths were checked in upstream code/tests. T3 rejects fork checkout. The displayed shell command `az repos pr checkout` also works for same-repository PRs. |
| Create or publish | Supports `repos create` and `repos pr create`, including draft PR creation. |
| PR board, preview, detail, linked thread and checks | List/show and paginated policy reads work. T3 can show authors, reviewers, body, state, policy checks and the labels included in the PR response. Linked summaries use those same reads for refresh. Watch notifications, mobile surfaces and agent tools were not independently exercised end to end. |
| PR activity, labels and code | The supported Git resources are `pullRequestThreads`, `pullRequestLabels`, `pullRequestIterations`, `pullRequestIterationChanges`, `blobs` and `items`. The stock adapter uses them for comment history, label reads, head revision, changed-file count, patches and file contents. All six were exercised live. |
| Lifecycle, editing and reviewers | Supports merge and squash, auto-complete, ready/draft, abandon/reopen, title/body edits, and adding or removing requested reviewers. Stock T3's Azure provider already disables PR comment writes, review submission, thread replies/resolution, reactions, text search and reviewer candidate discovery. |
| Work-item lists, search, detail and history | Supports `boards work-item show` and the `wit` reads for `workitemtypes`, `wiql`, `workitemsbatch`, `comments` and `updates`. Linked PR details use `repos pr show`. |
| Work-item changes and candidates | T3 can list project tags and sample team members as assignee candidates. The stock issue tracker creates and updates work items with the dedicated Boards commands. It posts comments and toggles their reactions through catalog-resolved invoke. |
