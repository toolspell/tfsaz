# tfsaz

`tfsaz` lets stock T3 Code read pull requests from on-premises Azure DevOps Server. It runs as `az` on the T3 **server process's** PATH and talks directly to the configured collection over HTTPS. The CLI implements discovery, authenticated viewer, repository details, PR show/list, and paginated PR policy reads.

## Build and configure

Build with Go 1.26 or newer. On macOS or Linux run `go build -o tfsaz .` and place a symlink named `az` in a directory early on T3's PATH. On Windows build `tfsaz.exe` and place a link or copy named `az.exe` on that PATH. Restart the T3 server with the updated environment. Keep the normal Azure CLI on a separate PATH if you use both.

Create `tfsaz/config.json` under your OS user configuration directory, or set `TFSAZ_CONFIG` to the absolute path of your configuration file for both the CLI and T3. The default directories are `~/Library/Application Support` on macOS, `%AppData%` on Windows, and `$XDG_CONFIG_HOME` or `~/.config` on Linux.

```json
{
  "profiles": [
    {
      "id": "engineering",
      "organization": "https://engineering-example.visualstudio.com",
      "collectionUrl": "https://devops.example.invalid/DefaultCollection"
    },
    {
      "id": "platform",
      "organization": "https://dev.azure.com/platform-example",
      "collectionUrl": "https://server.example.invalid:8443/tfs/Second Collection",
      "aliases": ["https://vpn.example.invalid/tfs/Second Collection"],
      "caFile": "private-ca.pem"
    }
  ]
}
```

Give each collection a unique, stable ID. Use its full collection URL, including any port and virtual directory. Optional aliases are full collection or Azure-style organization URLs that select the same profile. Every authenticated request uses the profile's `collectionUrl`. Redirects fail closed. TLS uses system trust plus the optional PEM CA file, whose path can be relative to the configuration file.

Store the PAT once per profile, as the OS user that runs T3. Log in to the first example profile with the following command.

```sh
tfsaz devops login --organization https://engineering-example.visualstudio.com
```

Enter the PAT at the hidden prompt, or pipe one line from a secret provider on stdin. Login verifies the PAT against the collection before saving it in the OS keyring under service `tfsaz` and the profile ID. A rejected replacement preserves the previous credential. The PR reads require code read access. Resolving a login through the identities endpoint can also require identity read access.

## Checkout and commands

Give the checkout a Git remote T3 recognizes, such as `https://engineering-example.visualstudio.com/MyProject/_git/MyRepo`. An SSH remote such as `git@ssh.dev.azure.com:v3/platform-example/MyProject/MyRepo` selects the modern organization in the example. Arrange Git transport to the real server separately. PR web links retain the Azure-style identity T3 uses for matching.

| T3 command | Behavior |
| --- | --- |
| `az --version` | Prints the tfsaz version before configuration or authentication. |
| `az account show --query user.name -o tsv` | Prints the first verified login in profile order. Fails if none verify. |
| `az account show --query user -o json` | Returns the checkout’s verified login. If connection data gives a bare account, the CLI queries the same collection for its qualified login. |
| `az repos show` | Returns repository details, including clone URLs and default branch. |
| `az repos pr show --id N` | Returns the PR with its Azure-style web link. |
| `az repos pr list` | Returns a JSON array with T3's repository, branch, status, creator/reviewer and paging filters. |
| `az repos pr policy list --id N --top 100 --skip 0` | Returns one array of raw evaluations. T3 advances the offset and computes the check state. |

An explicit `--organization` or `--org` selects the profile even in a checkout belonging to another collection. Repository and viewer commands fail if the selected credential fails. Outside a recognized checkout, supply the organization and any required project and repository. Diffs, work items and write operations are outside the current command set.

## Verification

Run `go test ./...` for the offline suite. It uses temporary Git repositories, synthetic HTTPS responses and an in-memory credential store.

The opt-in integration test builds the actual executable and runs T3's existing Azure adapter against it. It requires Git, Node with TypeScript support (tested with Node 26), a T3 source tree with its lockfile dependencies installed, and a usable OS keyring. It creates a random keyring entry for its loopback server and removes the entry at the end.

```sh
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

At T3 revision `c478fde9fe`, the offline adapter test exercises viewer, default branch, authored PR list, link identity and two pages of policies. Two successive summary reads observe a queued fixture policy becoming approved. The Go suite covers distinct users, rejected credentials, unavailable collections, and profile isolation. T3's own Azure provider package passed all 200 upstream tests in an isolated copy of that revision.

### Live results, 25 November 2026

Stock T3 Code `v0.1.12-nightly.20261125.5230` ran on macOS with an isolated data directory and `tfsaz` on its PATH.

- **Linking and refresh.** T3 linked PR 118 in DefaultCollection and PR 1 in Second Collection. On the same linked PR 118, the UI changed from “1 of 1 running” to “All checks passed” after the test status changed, without an agent issuing a refresh. The temporary status was removed.
- **Collection routing and Git.** The collections shared a real server and used distinct recognized `*.visualstudio.com` aliases. The tests covered a collection directly below the host, a collection name with a space, equivalent literal and percent-encoded paths, and rejection of an unconfigured `/tfs/` prefix. Git cloned each repository over HTTPS and SSH, with matching HEADs across transports. The CLI selected the correct collection in native HTTPS checkouts. Stock T3 still requires a recognized remote to enable its Azure provider. The server's native SSH URL alone does not enable it.
- **Viewer and PR board.** Each profile used its own PAT for the same account. The stock All/Open board displayed 18 authored and 20 review-requested entries across the projects. After fixing viewer lookup, live reads through the stock adapter matched the viewer to PR 43's author in the first collection and PR 1's reviewer in the second. Every returned row in the sampled authored and reviewing lists matched the viewer for its filter. Testing with distinct users was outside this live run.
- **PR content.** Both collections returned the expected PR links, summaries, branch search results, default branch and clone URL fields. The adapter reported two checks on PR 43 and zero on PR 1. On both PRs, activity was marked truncated, changed files fell back to zero and the diff read failed.

### Feature coverage at this T3 revision

The command table above lists implemented commands. The table below also covers operations that T3 advertises but the current CLI cannot serve. The audit compared the stock source-control, PR and issue providers and their tests with the CLI dispatcher. An executable check exercised all missing command families and named resources below with an absent configuration file. They exited nonzero before opening a collection. The live checks above cover the macOS server and stock web UI. Windows and Linux builds are cross-compiled.

| Feature | What works or is missing |
| --- | --- |
| Discovery, project recognition and link matching | Version and account probes work. PR web URLs preserve the recognized checkout identity. Opening such a URL in an external browser still depends on the host setup. |
| Clone, branch and checkout | `repos show` supplies clone URLs and default branch. T3 finds a branch's PR with `repos pr list`. For same-repo PR checkout it reads `repos pr show` and uses Git fetch/switch. PR templates also use local Git. Clone transports were exercised live. Template and checkout paths were checked in upstream code/tests. T3 rejects fork checkout. The displayed shell command `az repos pr checkout` is unsupported by tfsaz. |
| Create or publish | Missing `repos create` and `repos pr create`, including draft PR creation. |
| PR board, preview, detail, linked thread and checks | List/show and paginated policy reads work. T3 can show authors, reviewers, body, state, policy checks and the labels included in the PR response. Linked summaries use those same reads for refresh. Watch notifications, mobile surfaces and agent tools were not independently exercised end to end. |
| PR activity, labels and code | Missing Git resources for `devops invoke` are `pullRequestThreads`, `pullRequestLabels`, `pullRequestIterations`, `pullRequestIterationChanges`, `blobs` and `items`. This affects comment history, dedicated label reads, head revision, changed-file count, patches and file contents/revisions. A displayed zero for changed files can mean the lookup failed. |
| Lifecycle, editing and reviewers | Missing `repos pr update` for merge/squash, auto-complete, ready/draft, abandon/reopen and title/body edits. Missing `repos pr reviewer add/remove` for reviewer requests. Stock T3's Azure provider already disables PR comment writes, review submission, thread replies/resolution, reactions, text search and reviewer candidate discovery. |
| Work-item lists, search, detail and history | Missing `boards work-item show` and `devops invoke --area wit` resources `workitemtypes`, `wiql`, `workitemsbatch`, `comments` and `updates`. The linked-PR subread uses the supported `repos pr show`, but the containing work item cannot be loaded. |
| Work-item changes and candidates | Missing `boards work-item create/update`, including assignment, tags, text and state changes. Comments and reactions require the `comments` and `commentsReactions` resources. Candidate lookup needs `tags`, `devops team list` and `devops team list-member`. |
