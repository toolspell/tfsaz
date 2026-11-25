# tfsaz

`tfsaz` lets stock T3 Code work with Git repositories, pull requests and work items in an on-premises Azure DevOps Server collection. It runs as `az` on the T3 **server process's** PATH and talks directly to the configured collection over HTTPS. T3 can publish repositories, open and complete PRs, show diffs and policy checks, and create, edit and comment on work items.

## Choose the setup for your environment

| Goal | Current entry point |
| --- | --- |
| Connect an installed T3 app | Use `tfsaz shim install --dir DIR`, put DIR on the backend’s PATH, and run `tfsaz init --collection URL`. |
| Use an existing clone | Run `tfsaz repo setup PATH`. New setups keep the original SSH or HTTPS transport. Use `tfsaz repo verify PATH` to check it and `tfsaz repo restore PATH` to recover the original URL offline. |
| Clone a synthetic HTTPS URL in a shell or job | Start Git under `tfsaz route run --` and configure Git credentials for the native collection. This also works when no checkout exists yet. |
| Open links from T3 Desktop on macOS | Export the macOS link handler on the machine where the link opens. It works with the user’s selected browser. |

## Build and configure

Use the installed T3 Code desktop app. Only tfsaz developers running the adapter integration tests need a T3 source checkout.

Build with Go 1.26 or newer, or use an installed tfsaz executable. For a local macOS build, these commands put the CLI and both helper names in a user-owned directory.

```sh
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/tfsaz" .
"$HOME/.local/bin/tfsaz" shim install --dir "$HOME/.local/bin"
```

Put that directory early on the login-shell PATH that T3 Desktop uses, then fully quit and reopen the app. The shim installer creates links for `az` and `git-remote-tfsaz` and refuses to replace another installation. Choose a separate directory if you also use Microsoft’s Azure CLI. On Windows, build `tfsaz.exe`; if symbolic links are unavailable, install matching copies as `az.exe` and `git-remote-tfsaz.exe` and update those copies together. For a remote T3 environment install the shims on the server. Browser link handling belongs on the machine where links open.

```sh
tfsaz init --collection 'https://devops.example.invalid/tfs/DefaultCollection' --id engineering
tfsaz profile list
tfsaz doctor
```

`init` generates a stable Azure-style alias from the collection URL and verifies the PAT before saving it. On a terminal it can prompt for the collection URL and hides PAT input. For a job, pass the URL as an argument and pipe the PAT on stdin from a secret provider. `--no-login` writes only the nonsecret configuration and works without access to the OS keyring. Add another collection with `tfsaz profile add --collection URL`; `profile list` prints profiles in discovery order. Supply `--organization URL` to choose the organization alias, or `--ca-file PATH` for a private CA. Use **Settings → Source control & issues → Rescan** to confirm discovery in the installed T3 app.

Alternatively, create `tfsaz/config.json` under your OS user configuration directory, or set `TFSAZ_CONFIG` to the absolute path of your configuration file for both the CLI and T3. For T3 Desktop launched from Finder, the default location avoids relying on a custom variable reaching its backend. The default directories are `~/Library/Application Support` on macOS, `%AppData%` on Windows, and `$XDG_CONFIG_HOME` or `~/.config` on Linux.

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

Enter the PAT at the hidden prompt, or pipe one line from a secret provider on stdin. Login verifies the PAT against the collection before saving it in the OS keyring under the stable profile ID. Profiles created by the CLI use a keyring service scoped to the collection URL. Existing configuration files keep their original `tfsaz` service. `tfsaz profile logout --profile ID` removes the selected local credential and keeps the profile configured. Server-side token revocation is separate. Reading PRs requires Code (Read), and reading work items requires Work Items (Read). For edits, grant Code (Read & write) or Work Items (Read & write) as appropriate. Creating a repository also requires code management scope and the project’s Create repository permission. Listing assignee candidates uses project and team read access. Resolving a login through the identities endpoint can also require identity read access.

## Set up a checkout for stock T3

Use an existing clone whose `origin` points to a configured Azure DevOps Server collection. Install the binary at its intended permanent path before running setup. Restore the checkout before moving its Git directory or the installed helper.

```sh
tfsaz repo setup /path/to/checkout
tfsaz repo list
tfsaz repo verify /path/to/checkout
tfsaz repo restore /path/to/checkout
```

**New setups keep the checkout’s existing Git transport on macOS, Windows and Linux.** T3 sees an Azure-style repository URL. Git sees `remote.origin.vcs=tfsaz` and invokes `git-remote-tfsaz` for operations on that remote. The helper reads the bound native target from the checkout’s common Git configuration. An HTTPS origin continues using Git’s HTTPS credentials and trust settings. An SSH origin continues using the native SSH endpoint and Git’s SSH settings. Ordinary Git commands in an already-managed checkout need the helper on PATH and can run with T3 closed and `TFSAZ_CONFIG` unset. Clients using a separate Git implementation must support configured remote helpers.

- **Inspect or verify.** `tfsaz repo show PATH` displays the saved origin, native Git target, transport and profile offline. `repo verify PATH` also checks current configuration and live read access. Use `repo list` for the registry and `repo verify` to check all managed checkouts. Setup refuses conflicting fetch/push destinations. Git credentials for the native collection are separate from the PAT used for CLI API calls.
- **Select the mode.** Use `--preserve-transport` to select the default per-remote helper explicitly. `--managed-ssh` selects the wrapper explicitly on all platforms, and `--ssh-url URL` selects it with a native SSH endpoint override. The wrapper manages the checkout’s `core.sshCommand` and checks public-key access. Use `--profile ID` if the native SSH URL could identify more than one collection.
- **Restore or change modes.** Running setup again on an already-managed checkout retains its recorded mode unless you explicitly request a different one. To switch modes, restore the checkout and run setup for the desired mode. Both modes journal setup and restore and check for drift. Recover from interruption by rerunning the operation. Restore recovers the saved origin offline.
- **Linked worktrees.** Run setup once from any checkout. Existing worktrees and ones created later by Git or T3 share the origin mapping. Restore from any surviving worktree to return all of them to the original remote. If the worktree used for setup is removed or moved with Git, `repo list` and `repo verify` can locate another checkout registered under the same Git common directory.
- **Fresh clones.** A per-remote setting takes effect after the checkout exists. To clone directly from a synthetic HTTPS URL, use `tfsaz route run -- git clone URL`. You can also clone from the native server with plain Git, then run `repo setup`.

## Diagnose a CLI or checkout

Use `tfsaz doctor [PATH]` or `tfsaz doctor --json [PATH]`. The report identifies the `az` selected by this process’s PATH, the config file, and the profile selected by the checkout. For managed checkouts it runs the repository verifier and checks the selected collection’s saved PAT. Outside a checkout, it checks configured profiles in order. A failed check produces a nonzero exit, including with JSON output. Run it in the same environment as the failing program: a terminal and an app launched from Finder may resolve different executables.

Offline help works for groups and individual commands, including `tfsaz repo setup --help` and `az devops invoke --help`. To trace an Azure command, set `TFSAZ_TRACE=1` in its launch environment. Trace lines on stderr contain the command, profile ID, selection reason, HTTP status and elapsed time. Regular stdout is preserved. The trace omits arguments, URLs, identities, headers and request and response bodies.

## Route synthetic HTTPS URLs

### Installed T3 Code desktop app on macOS

T3 Desktop sends an external web link to the Mac’s default browser. The optional tfsaz link handler receives that URL, translates a registered Azure link using the current configuration, and opens the result in your chosen browser. Browser sign-in takes place at the real collection. You can use Safari, Firefox, or another installed browser. This path needs only the installed tfsaz binary and its configuration. The handler starts on demand.

For Safari, run the following once with tfsaz installed at its permanent path.

```sh
mkdir -p "$HOME/Applications"
tfsaz route mac-handler-export "$HOME/Applications/tfsaz Links.app" com.apple.Safari
```

For Firefox use `org.mozilla.firefox` as the final argument. First try the handler with `open -a "$HOME/Applications/tfsaz Links.app" "YOUR_AZURE_PR_URL"`. To have desktop links use it automatically, select **tfsaz Links** in **System Settings → Desktop & Dock → Default web browser**. Ordinary web links go to the chosen browser. Unsupported paths on a registered alias produce an error. To undo the change, select your usual default browser again and remove the exported app. The handler captures the CLI and config file paths at export. Changes to the contents of that file or to active repo setups apply on the next click. If either path moves, or if you switch browsers, export a new handler.

The OS hands off links from applications such as T3 Desktop. Typing a URL into a browser or clicking a link in the browser version of T3 usually keeps navigation inside that browser. For these cases use `tfsaz route resolve URL` or the optional Chromium integration below. Scripts can also consume `route resolve` directly. Exact hostname entries can separately catch traffic from programs that resolve names locally.

| Caller | How to use it |
| --- | --- |
| Human or script | `tfsaz route explain URL` shows the action, collection profile and destination. `tfsaz route resolve URL` prints the real URL for a shell client. Check its exit status before using the output. Supply the client's normal authentication for that destination. |
| Optional in-browser routing for Chromium | Run `tfsaz route browser-export NEW_DIRECTORY`. In the intended Chromium profile, enable developer mode and load the unpacked directory. Supported repository, PR, commit, work-item and build links navigate to the real origin, where browser sign-in applies. The extension blocks other requests to the registered alias spellings it recognizes. Use `tfsaz route browser-verify DIRECTORY` to compare the files with the current configuration. After a profile change or repo restore, remove the old extension and load a fresh export. Removing the extension disables browser routing for that profile. |
| Git or stock T3 server | Use `tfsaz route run -- git clone URL`, `tfsaz route run -- t3 serve`, or launch an entire shell with `tfsaz route run -- zsh`. Processes in that tree inherit a private Git HTTPS helper. The original Git helper contacts the real collection and uses Git's TLS and credential settings. Git retains the synthetic origin for T3 detection. A process that discards the inherited Git environment can bypass the helper. Closing the launched process ends the override. A separately started service needs its own launch configuration. |
| Unattended job | Run the job under `tfsaz route run --` with Git credentials configured for the real collection under the job's OS account. The launcher disables Git terminal and Git Credential Manager interaction when stdin is not a terminal. Choose a non-interactive credential helper for the job. Git does not automatically receive the CLI's Keychain PAT. |

**macOS hostname catch-all.** `tfsaz route hosts` prints IPv4 and IPv6 loopback entries for the registered dedicated aliases. Review the output, then add those lines as a distinct block in the Mac's `/etc/hosts` if system-wide resolution is wanted. Remove that block to undo it. Regenerate it when the aliases change. `tfsaz route verify-hosts` checks the current system resolver for each name. `tfsaz route sink` explicitly closes TCP connections arriving on port 443 on both loopback addresses, without reading or recording the payload. An unused loopback port already refuses connections when the listener is stopped. The listener needs both addresses and an available port. Use `--port N` for a different listener port.

Hostname mapping covers ordinary local DNS lookups for all ports. A client using a remote proxy, private DNS resolver or direct IP can bypass it. Browser profiles on other machines, VMs and containers need their own configuration. None of these checks certifies machine-wide egress enforcement.

**Optional PAC/SOCKS catch-all.** `tfsaz route proxy --port 1080` serves `http://127.0.0.1:1080/proxy.pac` and refuses SOCKS5 at that port. For registered hosts, the PAC lists SOCKS5 first and an HTTP proxy on the same loopback port second. The HTTP entry supports clients such as WinHTTP that ignore SOCKS5. The service refuses HTTP proxy requests and HTTPS CONNECT tunnels locally. Unregistered hosts get DIRECT. In macOS Network settings, the active network service’s **Details → Proxies → Automatic proxy configuration** accepts this URL. Record and restore the prior proxy configuration when enabling or disabling it. A standalone PAC replaces the previous PAC for that network service. Where an organization requires an existing proxy, integrate the exact-host cases into its existing policy. `tfsaz route pac` prints the rules for inspection or independent hosting. Restart the proxy or redistribute the PAC after the alias list changes. [Apple’s proxy settings guide](https://support.apple.com/guide/mac-help/mchlp2591/mac)

An OS proxy setting applies to clients that consult it. For example, curl can explicitly select SOCKS with remote name resolution using `--proxy socks5h://127.0.0.1:1080`. The SOCKS5 refusal happens during the handshake. An HTTP proxy client supplies its destination in the request; the service sends a refusal without forwarding or logging it. In the macOS Chromium test, a cold start with an unavailable PAC URL attempted a direct connection. Use the hostname mapping alongside PAC when this failure mode matters. A SOCKS tunnel to the on-prem server alone would still leave the client checking TLS for the synthetic hostname and sending the cloud-form path. [Chromium proxy behavior](https://chromium.googlesource.com/chromium/src/+/HEAD/net/docs/proxy.md)

The hostname and PAC commands omit the shared `dev.azure.com` hostname by default. Use `--include-dev-azure` only on a machine or network service where catching **every** organization on that hostname is intended. DNS and SOCKS cannot select an organization by HTTPS path. The Chromium rules can match the configured organization path before TLS. The extension and Git helper translate supported traffic without TLS interception. SSH still needs its separate repository-path translation. Raw REST clients can use `az rest` with an explicit collection.

**Verify a standalone Node client.** Run `tfsaz route verify-node` under the account and environment that launch the client. It runs the `node` on that PATH, checks the certificate chain and hostname for every configured real collection, and exits nonzero if any handshake fails. It sends no HTTP request or PAT. The profile’s `caFile` configures the Go CLI. To trust the same PEM in Node, set `NODE_EXTRA_CA_CERTS` to its absolute path in the client’s launch environment. The check applies to that Node runtime. An installed desktop app can bundle a different runtime, and GUI launches and background jobs can inherit different settings from a terminal. Ordinary T3 Azure CLI calls through tfsaz need neither this check nor a separate Node installation.

**TLS for Node processes.** Browser and Git routing select the native hostname before the client opens TLS. A DNS override or a SOCKS tunnel alone leaves the synthetic hostname as the TLS identity. A server certificate must match the hostname in the URL (or the IP address for an IP-literal URL) and chain to a trusted certificate. On the test Mac, Node 26.13.1 rejected the LAN listener with `DEPTH_ZERO_SELF_SIGNED_CERT` using its bundled roots. Launching Node with `NODE_USE_SYSTEM_CA=1` completed TLS and received the server’s expected unauthenticated HTTP 401. On supported Node versions this setting adds the system roots. Alternatively, set `NODE_EXTRA_CA_CERTS=/absolute/path/to/approved-ca.pem` before launching the Node app. Apply this to the process making the HTTPS request. T3’s Azure CLI calls launch the Go binary, which has its own system and per-profile CA support. [Node TLS environment options](https://nodejs.org/api/cli.html#node_use_system_ca1) · [TLS service identity](https://www.rfc-editor.org/rfc/rfc9525.html)

**Long-running processes.** The hosts-file block and installed browser rules persist while tfsaz is stopped. Git starts its transport helper per operation. For a Mac that uses the local PAC service continuously, run `tfsaz route proxy` as a per-user launchd LaunchAgent with an absolute executable path, an explicit `TFSAZ_CONFIG`, and `KeepAlive`. Restarting the process restores PAC availability. The hostname mapping provides a separate fallback during the restart for clients resolving locally. A service launched before user login needs its own account, environment and supervision. [Apple’s launchd job guide](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)

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

On the test Mac, the installed T3 Code Nightly 0.1.12-nightly.20261125.5230 bundle contained the external-link handoff. A separate Launch Services test delivered a stock-format PR link to the exported app and verified that a disposable receiving app got the native URL, including query and fragment. macOS listed the handler as an HTTPS candidate. The test explicitly selected the handler for that event and left the user's default browser in place. The Node verifier rejected both live collection certificates with bundled roots and verified both with `NODE_USE_SYSTEM_CA=1`.

Transport-preservation tests used the installed T3 Nightly backend in an isolated macOS GUI launchd job, with the helper directory on the test login-shell PATH. T3 pulled and resolved a PR for all four combinations of collection and original transport. Ordinary unattended Git also fetched with `TFSAZ_CONFIG` unset. Separate tests pushed over SSH and HTTPS to disposable repositories in both collections, checked the advertised commit IDs, and recycled the fixture repositories. Live onboarding checked stdin login, ordered profile listing, doctor in and outside a checkout, sanitized tracing, and logout of only the temporary credential slots.

On macOS, linked worktrees created before and after setup passed Git fetch, push dry-runs, scoped doctor checks, and live PR lookup through the stock T3 adapter over both transports in both collections. The installed T3 Nightly backend also created its own linked worktree and resolved PR 118 from it. After removal of the worktree that ran setup, pathless verification succeeded and restore from the later worktree returned the surviving checkouts to their original URL.

The macOS HTTPS routing run exercised fresh clone, fetch and push against both real collections. Stock T3 retried two rejected SSH clones over HTTPS and retained recognized Azure-style origins. The Git test supplied preemptive Basic authentication scoped to each real collection. In a disposable Chromium profile, a stock T3 “Open on Azure DevOps” click reached the native PR path, and links from both collections reached the corresponding server URLs. The browser had no on-prem login session. The same stock T3 instance linked a live work item in each project, displayed the attachments and returned the correct thread in the reverse lookup. Both test links were removed.

### Windows 11 x64 and Ubuntu 26.04 ARM64

The installed Windows T3 Nightly desktop app and the backend from the Ubuntu Nightly desktop package both created linked worktrees and resolved the expected PR across all four combinations of collection and transport. Git fetched with `TFSAZ_CONFIG` unset. Restore from a linked worktree recovered the native origin for the whole clone. A separate test of the default `repo setup` pushed a commit from a linked worktree in each of the eight OS, collection and transport combinations. Reading back each branch at the native endpoint confirmed the commit before the test repositories were recycled.

- **Credential stores.** On Ubuntu the automatic desktop login had no default persistent Secret Service collection. An isolated, unlocked GNOME Keyring session handled the test PATs. On Windows, Credential Manager rejected writes directly from an SSH logon. Starting the test process under a password-backed Windows logon with its user profile loaded allowed it to store, use and remove the test credentials.
- **Git clients and TLS.** The live HTTPS tests trusted the testbed CA explicitly and supplied Git credentials scoped to the native collection. The Windows SSH tests passed with Git for Windows’ bundled OpenSSH. With Windows’ system OpenSSH, a direct native Git read authenticated and then stalled on this VM. Confirm native `git ls-remote` works with the chosen client before setup.
- **HTTPS routing.** On each OS, `route resolve` produced native PR URLs and `route run -- git clone` retained the synthetic origin while fetching the native HEAD. In isolated browser profiles, Windows Edge and Ubuntu Firefox refused registered aliases and reached an unrelated local test site directly. Windows WinHTTP selected the loopback HTTP proxy for a registered host. Ubuntu’s GNOME GIO resolver selected loopback SOCKS5 followed by the loopback HTTP proxy. Both resolvers selected DIRECT for an unrelated host. These tests left the machine-wide proxy settings in place. Native desktop link-handler export is available on macOS.

### Feature coverage at this T3 revision

The command table above lists implemented commands. The table below shows how the stock adapter uses those commands and which capabilities its Azure provider exposes. The audit compared the stock source-control, PR and issue providers and their tests with the CLI dispatcher. The live checks above cover the macOS server and stock web UI. The write-specific HTTP and Git checkout regression tests also ran on Windows and Linux. The Windows and Linux tests used local fixtures.

| Feature | What works or is missing |
| --- | --- |
| Discovery, project recognition and link matching | Version and account probes work. PR web URLs preserve the recognized checkout identity. An exported Chromium extension opens supported HTTPS links on the native server. |
| Clone, branch and checkout | `repos show` supplies clone URLs and default branch. T3 finds a branch's PR with `repos pr list`. For same-repo PR checkout it reads `repos pr show` and uses Git fetch/switch. PR templates also use local Git. Clone transports were exercised live. Template and checkout paths were checked in upstream code/tests. T3 rejects fork checkout. The displayed shell command `az repos pr checkout` also works for same-repository PRs. |
| Create or publish | Supports `repos create` and `repos pr create`, including draft PR creation. |
| PR board, preview, detail, linked thread and checks | List/show and paginated policy reads work. T3 can show authors, reviewers, body, state, policy checks and the labels included in the PR response. Linked summaries use those same reads for refresh. Watch notifications, mobile surfaces and agent tools were not independently exercised end to end. |
| PR activity, labels and code | The supported Git resources are `pullRequestThreads`, `pullRequestLabels`, `pullRequestIterations`, `pullRequestIterationChanges`, `blobs` and `items`. The stock adapter uses them for comment history, label reads, head revision, changed-file count, patches and file contents. All six were exercised live. |
| Lifecycle, editing and reviewers | Supports merge and squash, auto-complete, ready/draft, abandon/reopen, title/body edits, and adding or removing requested reviewers. Stock T3's Azure provider already disables PR comment writes, review submission, thread replies/resolution, reactions, text search and reviewer candidate discovery. |
| Work-item lists, search, detail and history | Supports `boards work-item show` and the `wit` reads for `workitemtypes`, `wiql`, `workitemsbatch`, `comments` and `updates`. Linked PR details use `repos pr show`. |
| Work-item changes and candidates | T3 can list project tags and sample team members as assignee candidates. The stock issue tracker creates and updates work items with the dedicated Boards commands. It posts comments and toggles their reactions through catalog-resolved invoke. |
