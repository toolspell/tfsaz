# tfsaz

`tfsaz` lets stock T3 Code work with Git repositories, pull requests and work items in an on-premises Azure DevOps Server collection. It runs as `az` on the T3 **server process's** PATH and talks directly to the configured collection over HTTPS. T3 can publish repositories, open and complete PRs, show diffs and policy checks, and create, edit and comment on work items.

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

Enter the PAT at the hidden prompt, or pipe one line from a secret provider on stdin. Login verifies the PAT against the collection before saving it in the OS keyring under service `tfsaz` and the profile ID. Reading PRs requires Code (Read), and reading work items requires Work Items (Read). For edits, grant Code (Read & write) or Work Items (Read & write) as appropriate. Creating a repository also requires code management scope and the project’s Create repository permission. Listing assignee candidates uses project and team read access. Resolving a login through the identities endpoint can also require identity read access.

## Set up a checkout for stock T3

Use an existing clone whose `origin` points to a configured Azure DevOps Server collection. Install the binary at its intended permanent path before running setup. Restore the checkout before moving its Git directory or the installed helper.

```sh
tfsaz repo setup /path/to/checkout
tfsaz repo list
tfsaz repo verify /path/to/checkout
tfsaz repo restore /path/to/checkout
```

Setup matches the full collection path, creates a stable `tfsaz-…` organization for the repository, and installs an SSH origin that stock T3 recognizes. For example, T3 can see `ssh://git@vs-ssh.visualstudio.com/v3/tfsaz-…/App/api` while Git connects to `ssh://ado.example.invalid:2222/Second%20Collection/App/_git/api`. If several configured HTTPS listeners match a native SSH URL, select the collection with `--profile ID`.

- **Starting with HTTPS.** The CLI uses the profile’s PAT to read the server’s advertised SSH clone URL. You can also pass the server’s native clone URL with `--ssh-url URL`. While setup is active, ordinary Git fetch, pull and push to `origin` use SSH. Restore returns the checkout to HTTPS.
- **SSH credentials.** Setup and verify run a noninteractive `git ls-remote` against the repository. This tests read access with a key. Configure the key with OpenSSH or load it into your SSH agent, register its public key in Azure DevOps Server, and ensure the server’s host key is trusted. The check distinguishes rejected keys, host-key problems and unreachable servers. Push permissions are separate.
- **Where the settings live.** Git’s common repository config stores the managed origin and `core.sshCommand`. A private SSH config in the common Git directory records the alias endpoint. The helper verifies that mapping, translates the repository path and invokes ordinary OpenSSH with the real hostname, so your usual host settings and agent apply. Linked worktrees share the setup. Git can run with T3 closed and uses the saved path to the installed helper. The registry next to the profile config stores the original URL and routing metadata. With the default filename it is `config.json.repo-setups.json`.
- **Verification and recovery.** `repo verify` with no path checks every registered checkout. It checks the effective Git settings, SSH configuration, profile binding and SSH access. Setup refuses conflicting fetch/push URLs or a pre-existing custom SSH command. Setup and restore journal their changes and switch both Git settings together. After an interruption, rerun setup or restore. If a user edited managed settings, verify reports the conflict and restore preserves those edits until it can recognize the saved state. Restore can run offline.

The CLI maps the synthetic organization to the real collection for REST calls. Direct Git access to a real HTTPS URL uses ordinary Git authentication. Browser navigation and Git access to the synthetic **HTTPS** URL require separate routing. Clients must honor Git’s repository-local SSH command to use the managed remote. Manually configured Azure-style remotes and organization aliases remain supported.

## Commands used by T3

| T3 command | Behavior |
| --- | --- |
| `az --version` | Prints the tfsaz version before configuration or authentication. |
| `az account show --query user.name -o tsv` | Prints the first verified login in profile order. Fails if none verify. |
| `az account show --query user -o json` | Returns the checkout’s verified login. If connection data gives a bare account, the CLI queries the same collection for its qualified login. |
| `az repos create --project P --name NAME` | Creates a Git repository in the selected project. |
| `az repos show` | Returns repository details, including clone URLs and default branch. |
| `az repos pr show --id N` | Returns the PR with its Azure-style web link. |
| `az repos pr list` | Returns a JSON array with T3's repository, branch, status, creator/reviewer and paging filters. |
| `az repos pr create` and `az repos pr update` | Create draft or ready PRs, edit title and description, change draft or lifecycle status, configure auto-complete, and complete with merge or squash. |
| `az repos pr reviewer add` and `remove` | Resolve reviewers within the collection and update the selected PR. |
| `az repos pr checkout --id N` | Fetch the same-repository PR branch and switch to it. Accepts `--remote-name` and preserves existing local commits and edits. |
| `az repos pr policy list --id N --top 100 --skip 0` | Returns one array of raw evaluations. T3 advances the offset and computes the check state. |
| `az devops invoke --area git` | Reads PR threads, labels, iterations, iteration changes, file revisions and batched blobs for the diff view. |
| `az boards work-item create` and `update` | Create typed work items and edit title, description, state and named fields, including tags and assignee. |
| `az boards work-item show --id N --expand relations` | Reads a work item and the relations T3 uses to find linked PRs. |
| `az devops invoke --area wit` | Reads workflow states, searches with WIQL, loads result rows in batches, and reads comments, update history and tags. |
| `az devops team list` and `az devops team list-member` | Lists project teams and members for T3’s assignee picker. |

When `tfsaz` is installed as `az`, an unsupported invocation exits with status **2**, leaves stdout empty, and explains the limitation on stderr. The diagnostic identifies `tfsaz`, points to this README, and names `tfsaz --help` for offline use. Configuration, authentication, and server failures exit with status **1**.

An explicit `--organization` or `--org` selects the profile even in a checkout belonging to another collection. Outside a recognized checkout, supply the organization and any required project and repository. Work-item links use the selected Azure-style organization. WIQL queries and batch reads use HTTP POST to retrieve data.

## Discover and call REST resources

From a configured checkout, list the selected collection’s live resource catalog with `az devops invoke`. Outside a checkout, add `--org` and the configured organization or collection URL. Each row includes the server’s area, resource name, route template and version range. Area and resource selectors are case-insensitive. JMESPath comparisons use the casing in the returned JSON.

```sh
az devops invoke --area build --query '[].[resourceName,routeTemplate]' -o tsv
az devops invoke --area core --resource projects --query 'value[].name' -o tsv
az devops invoke --area build --resource definitions --route-parameters 'project=My Project' --api-version 7.1
az rest --method get --url '/_apis/projects?api-version=7.1' --query 'value[].name' -o tsv
```

For generic `devops invoke` calls, `tfsaz` requests `OPTIONS _apis?allHostTypes=true` from the collection, resolves the advertised route, and negotiates the requested version (default 5.0). Supply placeholders with `--route-parameters key=value` and query values with `--query-parameters key=value`. Stock T3’s existing PR and Boards calls retain their specialized handling for file contents, workflow states and links. Supply IDs that occur between fixed route segments. If multiple templates still match, use `az rest` with the explicit collection API URL.

`az rest --url` accepts a configured collection’s real HTTPS URL, its Azure-style alias, or a collection-relative API path in a recognized checkout. The CLI routes REST (`_apis`), Analytics (`_odata`) and Artifacts (`_packaging`) paths directly to the configured collection and refuses redirects. Both REST commands default to GET and accept explicit HEAD, OPTIONS, POST, PUT, PATCH and DELETE. Write requests operate with the selected PAT’s permissions.

Use `devops invoke --http-method POST --in-file request.json` or `rest --method POST --body @request.json` for JSON request bodies. For JSON Patch use `--media-type application/json-patch+json` with invoke, or `--headers Content-Type=application/json-patch+json` with rest. REST headers support Accept, Content-Type, conditional headers such as If-Match, Range and Prefer. Pass headers and URI parameters as `key=value` arguments, a JSON string dictionary or `@file`. Direct REST also accepts a text body and an explicit media type. JSON responses support `--query` and json, tsv, table or none output. Use `--out-file` with invoke or `--output-file` with rest to save a raw response to a new file.

## Verification

Run `go test ./...` for the offline suite. It uses temporary Git repositories, synthetic HTTPS responses and an in-memory credential store.

The opt-in integration test builds the actual executable and runs T3's existing Azure adapter against it. It requires Git, Node with TypeScript support (tested with Node 26), a T3 source tree with its lockfile dependencies installed, and a usable OS keyring. It creates a random keyring entry for its loopback server and removes the entry at the end.

```sh
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

At T3 revision `c478fde9fe`, the offline adapter test exercises viewer, default branch, authored PR list, link identity and two pages of policies. Two successive summary reads observe a queued fixture policy becoming approved. The fixture also exercises managed alias recognition on macOS/Linux, comment and label reads, a paginated diff, batched ZIP blobs and per-file content fallback. The Go suite covers distinct users, rejected credentials, unavailable collections, and profile isolation. T3's own Azure provider package passed all 200 upstream tests in an isolated copy of that revision.

The same fixture runs T3’s issue tracker against a collection under `/tfs/`. It covers filtered and empty searches, result order and pagination, linked PRs, comment reactions and continuation tokens, update history spanning two pages, and tag and assignee candidates.

### Live results, 25 November 2026

Stock T3 Code `v0.1.12-nightly.20261125.5230` ran on macOS with an isolated data directory and `tfsaz` on its PATH.

The live server ran Azure DevOps Server 2022.2. The CLI sends REST API version 7.1 for Boards and team reads, with an explicit preview version for work-item comments. Compatibility with earlier server versions is unverified.

- **Linking and refresh.** T3 linked PR 118 in DefaultCollection and PR 1 in Second Collection. On the same linked PR 118, the UI changed from “1 of 1 running” to “All checks passed” after the test status changed, without an agent issuing a refresh. The temporary status was removed.
- **Collection routing and Git.** The collections shared a real server and used distinct recognized `*.visualstudio.com` aliases. The tests covered a collection directly below the host, a collection name with a space, equivalent literal and percent-encoded paths, and rejection of an unconfigured `/tfs/` prefix. Git cloned each repository over HTTPS and SSH, with matching HEADs across transports. The CLI selected the correct collection in native HTTPS checkouts. Stock T3 still requires a recognized remote to enable its Azure provider. The server's native SSH URL alone does not enable it.
- **Viewer and PR board.** Each profile used its own PAT for the same account. The stock All/Open board displayed 18 authored and 20 review-requested entries across the projects. After fixing viewer lookup, live reads through the stock adapter matched the viewer to PR 43's author in the first collection and PR 1's reviewer in the second. Every returned row in the sampled authored and reviewing lists matched the viewer for its filter. Testing with distinct users was outside this live run.
- **Looking up another account.** In both collections, `repos pr list` returned the same PR IDs for a reviewer’s qualified login, bare login and identity GUID. Each result included the known reviewed PR. The same comparison passed for the creator filter. Through the stock T3 adapter, each sampled authored or review-requested row matched the requested account. These reads used the existing viewer’s PAT.
- **Resource discovery and REST.** Each collection advertised 1,198 resource locations across 99 areas. Catalog-resolved project, repository and build-definition reads returned the same IDs as direct REST calls. A WIQL POST returned the same two work-item IDs through both command forms. The stock issue tracker also used catalog-resolved writes to post a work-item comment and add and remove its reaction in each live collection.
- **PR content.** Both collections returned the expected PR links, summaries, branch search results, default branch and clone URL fields. In a subsequent live run, the stock adapter read 3 comments and 2 labels on PR 43 in DefaultCollection, and 0 comments and 1 label on PR 1 in Second Collection. Each returned one changed file and a patch. Blob batches decoded both revisions of the modified file and the single revision of the added file. Independently fetched file contents hashed to all three Git blob IDs.
- **Work items.** The stock T3 issue tracker listed two pages in each collection and found the sampled item by title. In both collections it opened work items with existing comments, history events and linked PRs, and generated work-item links under the selected organization alias. Candidate lookup returned 11 project tags and 4 assignee candidates in DefaultCollection, and 6 tags and 4 candidates in Second Collection. The live comment samples fit on one page. The fixture covers subsequent comment and update pages.
- **Repository and PR writes.** The stock adapter created a disposable repository and draft PR in each collection. Git pushed the base and feature branches through managed SSH origins. Read-back confirmed title and multiline body edits, draft and ready transitions, reviewer addition and removal by qualified login, abandon and reopen, and auto-complete enable and disable. The displayed PR checkout command selected the expected branch. After completion the remote target matched the PR’s reported merge commit. Git history showed one parent for squash and two for regular merge. After verification the test checkouts were restored and the two disposable repositories and work items were recycled.
- **Boards writes.** In both collections the stock issue tracker created a work item, changed its title, body, assignee, tags and workflow state, then read each change back. Clearing body, assignee and all tags worked. Server 2022.2 acknowledges an empty tag string but retains existing tags, so tfsaz removes the tag field with JSON Patch.
- **Managed SSH origins.** Four disposable checkouts covered two collections and both original transports. With setup active, plain Git read the same HEAD as the native SSH remote, fetched, and completed a push dry-run without `TFSAZ_CONFIG`. Verification succeeded for all four together, and restore reproduced each original URL.

### Feature coverage at this T3 revision

The command table above lists implemented commands. The table below shows how the stock adapter uses those commands and which capabilities its Azure provider exposes. The audit compared the stock source-control, PR and issue providers and their tests with the CLI dispatcher. The live checks above cover the macOS server and stock web UI. The write-specific HTTP and Git checkout regression tests also ran on Windows and Linux. The Windows and Linux tests used local fixtures.

| Feature | What works or is missing |
| --- | --- |
| Discovery, project recognition and link matching | Version and account probes work. PR web URLs preserve the recognized checkout identity. Opening such a URL in an external browser still depends on the host setup. |
| Clone, branch and checkout | `repos show` supplies clone URLs and default branch. T3 finds a branch's PR with `repos pr list`. For same-repo PR checkout it reads `repos pr show` and uses Git fetch/switch. PR templates also use local Git. Clone transports were exercised live. Template and checkout paths were checked in upstream code/tests. T3 rejects fork checkout. The displayed shell command `az repos pr checkout` also works for same-repository PRs. |
| Create or publish | Supports `repos create` and `repos pr create`, including draft PR creation. |
| PR board, preview, detail, linked thread and checks | List/show and paginated policy reads work. T3 can show authors, reviewers, body, state, policy checks and the labels included in the PR response. Linked summaries use those same reads for refresh. Watch notifications, mobile surfaces and agent tools were not independently exercised end to end. |
| PR activity, labels and code | The supported Git resources are `pullRequestThreads`, `pullRequestLabels`, `pullRequestIterations`, `pullRequestIterationChanges`, `blobs` and `items`. The stock adapter uses them for comment history, label reads, head revision, changed-file count, patches and file contents. All six were exercised live. |
| Lifecycle, editing and reviewers | Supports merge and squash, auto-complete, ready/draft, abandon/reopen, title/body edits, and adding or removing requested reviewers. Stock T3's Azure provider already disables PR comment writes, review submission, thread replies/resolution, reactions, text search and reviewer candidate discovery. |
| Work-item lists, search, detail and history | Supports `boards work-item show` and the `wit` reads for `workitemtypes`, `wiql`, `workitemsbatch`, `comments` and `updates`. Linked PR details use `repos pr show`. |
| Work-item changes and candidates | T3 can list project tags and sample team members as assignee candidates. The stock issue tracker creates and updates work items with the dedicated Boards commands. It posts comments and toggles their reactions through catalog-resolved invoke. |
