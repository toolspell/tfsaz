# tfsaz

`tfsaz` lets stock T3 Code work with Git repositories, pull requests and work items in an on-premises Azure DevOps Server collection. It runs as `az` on the T3 **server process's** PATH and talks directly to the configured collection over HTTPS. T3 can publish repositories, open and complete PRs, show diffs and policy checks, and create, edit and comment on work items.

## Choose the setup for your environment

| Goal | Current entry point |
| --- | --- |
| Connect an installed T3 app | Use `tfsaz shim install --dir DIR`, put DIR on the backend’s PATH, and run `tfsaz init --collection URL`. |
| Use an existing clone | Run `tfsaz repo setup PATH`. New setups keep the original SSH or HTTPS transport. Use `tfsaz repo verify PATH` to check it and `tfsaz repo restore PATH` to recover the original URL offline. |
| Clone a synthetic HTTPS URL in a shell or job | Start Git under `tfsaz route run --` and configure Git credentials for the native collection. This also works when no checkout exists yet. |
| Open managed links in a browser | Run `tfsaz route setup` and `tfsaz route verify` on the host running the browser. See [the redirect guide](docs/route-redirect.md). |

## Build and configure

Install tfsaz on the machine running the T3 backend, whether you use the installed desktop app or connect to a remote T3 server. A T3 source checkout is needed only for the optional stock-adapter integration test.

Build with Go 1.26 or newer, or use an installed tfsaz executable. From the tfsaz source directory on macOS or Ubuntu, build the CLI, install the two helper names and enable them in the current shell.

```sh
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/tfsaz" .
"$HOME/.local/bin/tfsaz" shim install --dir "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"
```

On Windows, run these commands from PowerShell in the tfsaz source directory.

```powershell
$bin = Join-Path $env:LOCALAPPDATA 'tfsaz\bin'
New-Item -ItemType Directory -Force $bin | Out-Null
go build -o "$bin\tfsaz.exe" .
& "$bin\tfsaz.exe" shim install --dir $bin
$env:Path = "$bin;$env:Path"
```

Both examples set PATH for the current shell. Put the same directory first on the PATH used by T3 Desktop, the remote backend service or the job that launches Git. Fully quit and reopen the app or restart the service after changing its environment. `tfsaz doctor` checks that the `az` selected on its own PATH matches the running tfsaz binary. If you also use Microsoft’s Azure CLI, keep its installation in a separate directory and choose PATH order per process.

The shim installer creates `az` and `git-remote-tfsaz` and refuses to overwrite a different executable. On Windows it falls back to executable copies when symbolic links are unavailable. After upgrading tfsaz, remove the two old **copies** from your dedicated shim directory and rerun `shim install`.

```sh
tfsaz init --collection 'https://devops.example.invalid/DefaultCollection' --id engineering
tfsaz profile list
```

`init` verifies the PAT before saving it. It generates a stable Azure-style alias from the collection URL when you omit `--organization`. On a terminal it can prompt for the collection URL and hides PAT input. For a job, pass the URL as an argument and pipe the PAT on stdin from a secret provider. `--no-login` writes only the nonsecret configuration and works without access to the OS keyring. Add another collection with `tfsaz profile add --collection URL`. `profile list` prints profiles in discovery order. Use `--ca-file PATH` for a private CA. For multiple collections, use distinct `*.visualstudio.com` hostnames so T3’s viewer lookup can distinguish them. Use **Settings → Source control & issues → Rescan** to confirm discovery in the installed T3 app.

Alternatively, create `tfsaz/config.json` under your OS user configuration directory, or set `TFSAZ_CONFIG` to the absolute path of your configuration file for both the CLI and T3. For T3 Desktop launched from Finder, the default location avoids relying on a custom variable reaching its backend. The default directories are `~/Library/Application Support` on macOS, `%AppData%` on Windows, and `$XDG_CONFIG_HOME` or `~/.config` on Linux.

```json
{
  "profiles": [
    {
      "id": "engineering",
      "credentialVersion": 2,
      "organization": "https://engineering-example.visualstudio.com",
      "collectionUrl": "https://devops.example.invalid/DefaultCollection"
    },
    {
      "id": "platform",
      "credentialVersion": 2,
      "organization": "https://platform-example.visualstudio.com",
      "collectionUrl": "https://server.example.invalid:8443/tfs/Second Collection",
      "aliases": ["https://vpn.example.invalid/tfs/Second Collection"],
      "caFile": "private-ca.pem"
    }
  ]
}
```

Give each collection a unique, stable ID. Use its full collection URL, including any port and virtual directory. Optional aliases are full collection or Azure-style organization URLs that select the same profile. Every authenticated request uses the profile's `collectionUrl`. Redirects fail closed. TLS uses system trust plus the optional PEM CA file, whose path can be relative to the configuration file.

Interactive `init` already stores the PAT. For a hand-written profile, a profile created with `--no-login`, or a replacement PAT, run login as the OS user that runs T3. This example uses the organization in the JSON above.

```sh
tfsaz devops login --organization https://engineering-example.visualstudio.com
```

Enter the PAT at the hidden prompt, or pipe one line from a secret provider on stdin. Login verifies the PAT against the collection before saving it in the OS keyring under the stable profile ID. The backend is Keychain on macOS, Credential Manager on Windows, and Secret Service on Linux. New profiles use `credentialVersion: 2`, which scopes the keyring service to the collection URL. Older profiles without this field use the original `tfsaz` service. Preserve the existing credential version when editing a profile so it can still find its saved PAT. `tfsaz profile logout --profile ID` removes the selected local credential and keeps the profile configured. Server-side token revocation is separate. Reading PRs requires Code (Read), and reading work items requires Work Items (Read). For edits, grant Code (Read & write) or Work Items (Read & write) as appropriate. Creating a repository also requires code management scope and the project’s Create repository permission. Listing assignee candidates uses project and team read access. Resolving a login through the identities endpoint can also require identity read access.

## Set up a checkout for stock T3

Use an existing clone whose `origin` points to a configured Azure DevOps Server collection. Install the binary at its intended permanent path before running setup. Restore the checkout before moving its Git directory or the installed helper.

```sh
tfsaz repo setup /path/to/checkout
tfsaz repo show /path/to/checkout
tfsaz doctor /path/to/checkout
```

After `doctor` verifies the checkout and PAT, open the checkout in T3 and view an existing PR. To open its Azure-style URL inside the browser, follow [Route synthetic HTTPS URLs](#route-synthetic-https-urls).

**New setups keep the checkout’s existing Git transport on macOS, Windows and Linux.** T3 sees an Azure-style repository URL. Git sees `remote.origin.vcs=tfsaz` and invokes `git-remote-tfsaz` for operations on that remote. The helper reads the bound native target from the checkout’s common Git configuration. An HTTPS origin continues using Git’s HTTPS credentials and trust settings. An SSH origin continues using the native SSH endpoint and Git’s SSH settings. Ordinary Git commands in an already-managed checkout need the helper on PATH and can run with T3 closed and `TFSAZ_CONFIG` unset. Clients using a separate Git implementation must support configured remote helpers.

- **Inspect or verify.** `tfsaz repo show PATH` displays the saved origin, native Git target, transport and profile offline. `repo verify PATH` also checks current configuration and live read access. Use `tfsaz repo list` for the registry and `repo verify` to check all managed checkouts. Setup refuses conflicting fetch/push destinations. Git credentials for the native collection are separate from the PAT used for CLI API calls.
- **Select the mode.** Use `--preserve-transport` to select the default per-remote helper explicitly. `--managed-ssh` selects the wrapper explicitly on all platforms, and `--ssh-url URL` selects it with a native SSH endpoint override for an HTTPS origin. The wrapper manages the checkout’s `core.sshCommand` and checks public-key access. Use `--profile ID` if the native SSH URL could identify more than one collection.
- **Restore or change modes.** Running setup again on an already-managed checkout retains its recorded mode and rejects a conflicting mode flag. To switch modes, restore the checkout and run setup for the desired mode. Both modes journal setup and restore and check for drift. Recover from interruption by rerunning the operation. Restore recovers the saved origin offline.
- **Linked worktrees.** Run setup once per clone from any of its worktrees. Repositories in the same collection reuse one profile and PAT. Each project/repository pair gets its own stable hostname, shared by clones using the same profile and collection. Existing worktrees and ones created later by Git or T3 share the origin mapping. Restore from any surviving worktree to return all of them to the original remote. If the worktree used for setup is removed or moved with Git, `repo list` and `repo verify` can locate another checkout registered under the same Git common directory.
- **Fresh clones.** A per-remote setting takes effect after the checkout exists. To clone directly from a synthetic HTTPS URL, use `tfsaz route run -- git clone URL`. You can also clone from the native server with plain Git, then run `repo setup`.

## Diagnose a CLI or checkout

Use `tfsaz doctor [PATH]` or `tfsaz doctor --json [PATH]`. The report identifies the `az` selected by this process’s PATH, the config file, and the profile selected by the checkout. For managed checkouts it runs the repository verifier and checks the selected collection’s saved PAT. With no checkout path and a working directory outside Git, it checks all configured profiles in order. In an unrelated Git checkout, it skips authentication. Pass the path to your managed clone for a definite checkout result. A failed check produces a nonzero exit, including with JSON output. Run it in the same environment as the failing program: a terminal and an app launched from Finder may resolve different executables.

Offline help works for groups and individual commands, including `tfsaz repo setup --help` and `az devops invoke --help`. To trace an Azure command, set `TFSAZ_TRACE=1` in its launch environment. Trace lines on stderr contain the command, profile ID, selection reason, HTTP status and elapsed time. Regular stdout is preserved. The trace omits arguments, URLs, identities, headers and request and response bodies.

## Route synthetic HTTPS URLs

For browser links from a managed checkout, configure the machine **where the browser runs**. For T3 Desktop and an external browser this is the workstation. For the web client’s server-hosted Browser panel it is the T3 server, under the account that launches Chromium.

```sh
tfsaz route setup
tfsaz route verify
```

The managed redirect serves exact `tfsaz-<24 hex>.visualstudio.com` names from active checkout setups or registered profiles. If the browser runs on another host, configure the same alias and native collection on that host as well. After adding or retiring aliases, stop using their links, then run `tfsaz route restore` followed by `tfsaz route setup` to refresh the certificate and hosts entries.

To check a particular PR, run `tfsaz route verify 'AZURE_STYLE_PR_URL'` and open that link in the intended browser. The address bar should show the real collection, including the query and fragment. Browser sign-in takes place there. Use `tfsaz route restore` to remove the managed installation. Setup needs administrator access to edit hosts. Ubuntu also uses it to configure port forwarding. [Browser routing and trust by platform](docs/route-redirect.md)

For a one-off link, `tfsaz route resolve URL` prints the destination. For desktop handoffs on machines where you cannot change the hosts file, the per-user link handler can open links using the selected system browser. [Desktop link handler](docs/desktop-link-handler.md)

| Caller | How to use it |
| --- | --- |
| Human or script | `tfsaz route explain URL` shows the action, collection profile and destination. `tfsaz route resolve URL` prints the real URL for a shell client. Check its exit status before using the output. Supply the client's normal authentication for that destination. |
| Optional in-browser routing for Chromium | Run `tfsaz route browser-export NEW_DIRECTORY`. In the intended Chromium profile, enable developer mode and load the unpacked directory. Supported repository, PR, commit, work-item and build links navigate to the real origin, where browser sign-in applies. The extension blocks other requests to the registered alias spellings it recognizes. Use `tfsaz route browser-verify DIRECTORY` to compare the files with the current configuration. After a profile change or repo restore, remove the old extension and load a fresh export. Removing the extension disables browser routing for that profile. |
| Git or stock T3 server | Use `tfsaz route run -- git clone URL`, `tfsaz route run -- t3 serve`, or launch an entire shell with `tfsaz route run -- zsh`. Processes in that tree inherit a private Git HTTPS helper. The original Git helper contacts the real collection and uses Git's TLS and credential settings. Git retains the synthetic origin for T3 detection. A process that discards the inherited Git environment can bypass the helper. Closing the launched process ends the override. A separately started service needs its own launch configuration. |
| Unattended job | Run the job under `tfsaz route run --` with Git credentials configured for the real collection under the job's OS account. The launcher disables Git terminal and Git Credential Manager interaction when stdin is not a terminal. Choose a non-interactive credential helper for the job. Git does not automatically receive the CLI's Keychain PAT. |

<details>
<summary>Optional hosts-file, connection refusal and proxy settings</summary>

**Local hostname catch-all.** `tfsaz route hosts` prints IPv4 and IPv6 loopback entries for the registered dedicated aliases. Review the output, then add those lines as a distinct block in the system hosts file if you want machine-wide resolution. On macOS and Ubuntu this is `/etc/hosts`. On Windows it is `%SystemRoot%\System32\drivers\etc\hosts`. Editing the file requires the appropriate OS privileges. Remove that block to undo it. Regenerate it when the aliases change. `tfsaz route verify-hosts` checks the current system resolver for each name. `tfsaz route sink` explicitly closes TCP connections arriving on port 443 on both loopback addresses, without reading or recording the payload. An unused loopback port already refuses connections when the listener is stopped. The listener needs both addresses and an available port. Use `--port N` for a different listener port.

Hostname mapping covers ordinary local DNS lookups for all ports. A client using a remote proxy, private DNS resolver or direct IP can bypass it. Browser profiles on other machines, VMs and containers need their own configuration. None of these checks certifies machine-wide egress enforcement.

**Optional PAC/SOCKS catch-all.** `tfsaz route proxy --port 1080` serves `http://127.0.0.1:1080/proxy.pac` and refuses SOCKS5 at that port. For registered hosts, the PAC lists SOCKS5 first and an HTTP proxy on the same loopback port second. The HTTP entry supports clients such as WinHTTP that ignore SOCKS5. The service refuses HTTP proxy requests and HTTPS CONNECT tunnels locally. Unregistered hosts get DIRECT. On macOS enter the URL in the active network service’s **Details → Proxies → Automatic proxy configuration**. On Windows 11 use **Settings → Network & internet → Proxy → Use setup script**. On Ubuntu GNOME use **Network → Network Proxy → Automatic**. Record and restore the prior proxy configuration when enabling or disabling it. A standalone PAC replaces the previous PAC for that network service. Where an organization requires an existing proxy, integrate the exact-host cases into its existing policy. `tfsaz route pac` prints the rules for inspection or independent hosting. Restart the proxy or redistribute the PAC after the alias list changes. [Apple proxy settings](https://support.apple.com/guide/mac-help/mchlp2591/mac) · [Windows proxy settings](https://support.microsoft.com/en-us/windows/experience/connectivity-networking/use-a-proxy-server-in-windows) · [GNOME proxy settings](https://help.gnome.org/gnome-help/net-proxy.html)

An OS proxy setting applies to clients that consult it. For example, curl can explicitly select SOCKS with remote name resolution using `--proxy socks5h://127.0.0.1:1080`. The SOCKS5 refusal happens during the handshake. An HTTP proxy client supplies its destination in the request; the service sends a refusal without forwarding or logging it. In the macOS Chromium test, a cold start with an unavailable PAC URL attempted a direct connection. Use the hostname mapping alongside PAC when this failure mode matters. A SOCKS tunnel to the on-prem server alone would still leave the client checking TLS for the synthetic hostname and sending the cloud-form path. [Chromium proxy behavior](https://chromium.googlesource.com/chromium/src/+/HEAD/net/docs/proxy.md)

The hostname and PAC commands omit the shared `dev.azure.com` hostname by default. Use `--include-dev-azure` only on a machine or network service where catching **every** organization on that hostname is intended. DNS and SOCKS cannot select an organization by HTTPS path. The Chromium rules can match the configured organization path before TLS. The extension and Git helper translate supported traffic without TLS interception. SSH still needs its separate repository-path translation. Raw REST clients can use `az rest` with an explicit collection.

</details>

**Verify a standalone Node client.** Run `tfsaz route verify-node` under the account and environment that launch the client. It runs the `node` on that PATH, checks the certificate chain and hostname for every configured real collection, and exits nonzero if any handshake fails. It sends no HTTP request or PAT. The profile’s `caFile` configures the Go CLI. To trust the same PEM in Node, set `NODE_EXTRA_CA_CERTS` to its absolute path in the client’s launch environment. The check applies to that Node runtime. An installed desktop app can bundle a different runtime, and GUI launches and background jobs can inherit different settings from a terminal. Ordinary T3 Azure CLI calls through tfsaz need neither this check nor a separate Node installation.

**TLS for Node processes.** The loopback redirect uses TLS for the alias first. Following its redirect opens a second TLS connection to the collection. The Git helper connects directly to the collection. A DNS override or a SOCKS tunnel alone leaves the synthetic hostname as the TLS identity. A server certificate must match the hostname in the URL (or the IP address for an IP-literal URL) and chain to a trusted certificate. On the test Mac, Node 26.13.1 rejected the LAN listener with `DEPTH_ZERO_SELF_SIGNED_CERT` using its bundled roots. Launching Node with `NODE_USE_SYSTEM_CA=1` completed TLS and received the server’s expected unauthenticated HTTP 401. On supported Node versions this setting adds the system roots. Alternatively, set `NODE_EXTRA_CA_CERTS=/absolute/path/to/approved-ca.pem` before launching the Node app. Apply this to the process making the HTTPS request. T3’s Azure CLI calls launch the Go binary, which has its own system and per-profile CA support. [Node TLS environment options](https://nodejs.org/api/cli.html#node_use_system_ca1) · [TLS service identity](https://www.rfc-editor.org/rfc/rfc9525.html)

**Long-running processes.** The hosts-file block and installed browser rules persist while tfsaz is stopped. Git starts its transport helper per operation. For a Mac that uses the local PAC service continuously, run `tfsaz route proxy` as a per-user launchd LaunchAgent with an absolute executable path, an explicit `TFSAZ_CONFIG`, and `KeepAlive`. On Ubuntu a systemd user service can run the same command with `Restart=on-failure`. On Windows use a Task Scheduler logon trigger for the current user with the restart-on-failure setting, a permanent executable path and a user configuration accessible to the task. Restarting the process restores PAC availability. The hostname mapping provides a separate fallback during the restart for clients resolving locally. A service launched before user login needs its own account, environment and supervision. [Apple launchd jobs](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html) · [systemd restart settings](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html) · [Windows Task Scheduler restarts](https://learn.microsoft.com/en-us/windows/win32/taskschd/taskschedulerschema-restartonfailure-settingstype-element)

## Azure CLI compatibility

These are the Azure CLI calls used by the stock T3 adapter at revision `c478fde9fe`. Use the `tfsaz` executable directly for manual calls, or use `az` with the tfsaz launcher first on PATH.

| Command | Behavior |
| --- | --- |
| `az --version` | Prints the tfsaz version before configuration or authentication. |
| `az account show --query user.name -o tsv` | Prints the first verified login in profile order. Fails if none verify. |
| `az account show --query user` | Returns the verified login for the selected collection as JSON. If connection data gives a bare account, the CLI queries the same collection for its qualified login. |
| `az repos create --project P --name NAME` | Creates a Git repository in the selected project. |
| `az repos show` | Returns repository details, including clone URLs and default branch. |
| `az repos pr show --id N` | Returns the PR with its Azure-style web link. |
| `az repos pr list` | Returns a JSON array with T3's repository, branch, status, creator/reviewer and paging filters. |
| `az repos pr create` and `az repos pr update` | Create draft or ready PRs, edit title and description, change draft or lifecycle status, configure auto-complete, and complete with merge or squash. |
| `az repos pr reviewer add` and `remove` | Resolve reviewers within the collection and update the selected PR. |
| `az repos pr policy list --id N --top 100 --skip 0` | Returns one array of raw evaluations. T3 advances the offset and computes the check state. |
| `az devops invoke --area git --resource NAME` | Reads PR threads, labels, iterations, iteration changes, file revisions and batched blobs for the diff view. |
| `az boards work-item create` and `update` | Create typed work items and edit title, description, state and named fields, including tags and assignee. |
| `az boards work-item show --id N --expand relations` | Reads a work item and the relations T3 uses to find linked PRs. |
| `az devops invoke --area wit --resource NAME` | Reads workflow states, searches with WIQL, loads result rows in batches, and reads comments, update history and tags. T3 also uses invoke to post work-item comments and add or remove comment reactions. |
| `az devops team list` and `az devops team list-member` | Lists project teams and members for T3’s assignee picker. |

T3 also displays `az repos pr checkout --id N` for manual use. It fetches and switches to a same-repository PR branch through `origin` or the remote named by `--remote-name`. The remote and an explicit `--organization` must identify the same collection. The PR commands and dedicated Boards and team commands use JSON output. `devops invoke` and `rest` also support formatted and raw output as described below.

Use `devops login --organization URL` to set up the on-premises PAT, including when T3’s upstream diagnostic mentions Azure sign-in. An unsupported invocation exits with status **2**, leaves stdout empty, and reports the limitation on stderr. Configuration, authentication and server failures exit with status **1**.

For API requests, an explicit `--organization` or `--org` selects the profile even from a checkout belonging to another collection. Outside a recognized checkout, supply the organization and any required project and repository. Work-item links use the selected Azure-style organization. WIQL queries and batch reads use HTTP POST to retrieve data.

```sh
az account show --query user -o json --org https://engineering-example.visualstudio.com
az repos pr list --org https://engineering-example.visualstudio.com \
  --project 'My Project' --repository 'My Repo' --status active --reviewer me --include-links --top 20
az repos pr policy list --org https://engineering-example.visualstudio.com --id 42 --top 100 --skip 0
az boards work-item show --org https://engineering-example.visualstudio.com --id 42 --expand relations
```

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

Use `devops invoke --http-method POST --in-file request.json` or `rest --method POST --body @request.json` for JSON request bodies. For JSON Patch use `--media-type application/json-patch+json` with invoke, or `--headers Content-Type=application/json-patch+json` with rest. REST headers support Accept, Content-Type, conditional headers such as If-Match, Range and Prefer. Pass headers and URI parameters as `key=value` arguments, a JSON string dictionary or `@file`. Direct REST also accepts a text body and an explicit media type. JSON responses support `--query` and json, tsv, table or none output. Use `--out-file` with invoke or `--output-file` with rest to save a raw response to a new file. Direct REST also creates an empty output file for HEAD and HTTP 204. The DevOps extension’s invoke behavior creates a file only when the server supplies a content type.

## Verification

Run `go vet ./...` and `go test -count=1 ./...` for the native suite. Tests use temporary Git repositories, loopback HTTP/TLS servers and a test credential store. Install Git and OpenSSH for transport tests, Node for the TLS probe, and GIO on Linux for desktop dispatch. On Windows use Git for Windows with its bundled shell. The [native test workflow](.github/workflows/test.yml) runs this suite on macOS, Windows and Ubuntu. All three jobs enable Go’s race detector. To run it locally, use `go test -race -count=1 ./...`. Windows x64 needs a compatible MinGW-w64 C compiler on PATH. [Go race detector requirements](https://go.dev/doc/articles/race_detector#Requirements)

The opt-in integration test builds the actual executable and runs T3's existing Azure adapter against it. It requires Git, Node with TypeScript support (tested with Node 26), a T3 source tree with its lockfile dependencies installed, and a usable OS keyring. It creates a random keyring entry for its loopback server and removes the entry at the end.

```sh
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

In PowerShell, set `$env:TFSAZ_T3_ROOT = 'C:\path\to\t3code'`, then run the same `go test` command. Use a logon with a loaded user profile and an accessible Credential Manager. For unattended Linux runs, start an unlocked Secret Service collection on the test session's D-Bus. The fixture uses its own random keyring entry.

At T3 revision `c478fde9fe`, the offline adapter test exercises viewer, default branch, authored PR list, link identity and two pages of policies. Two successive summary reads observe a queued fixture policy becoming approved. The fixture also exercises managed alias recognition, comment and label reads, a paginated diff, batched ZIP blobs and per-file content fallback. The Go suite covers distinct users, rejected credentials, unavailable collections, and profile isolation. T3's own Azure provider package passed all 200 upstream tests in an isolated copy of that revision.

The same fixture runs T3’s issue tracker against a collection under `/tfs/`. It covers filtered and empty searches, result order and pagination, linked PRs, comment reactions and continuation tokens, update history spanning two pages, and tag and assignee candidates. The adapter also updates a PR description via stdin and posts a work-item comment via a temporary JSON file. The fixture checks the exact multiline Unicode text, API version and destination of both writes.

The native suites passed with the Go race detector and the stock T3 adapter fixture on macOS ARM64, Windows 11 x64 and Ubuntu ARM64. The setup and restore tests exercise Git worktrees, preserved HTTPS and SSH, and the managed SSH path. Git for Windows uses its bundled shell and a disposable SSH agent for those fixtures. The routing tests exercise the PAC server, IPv4 and IPv6 sink, and detection of obsolete browser rules without installing system proxy settings. The loopback redirect tests use generated leaf certificates. Native runs covered macOS, Windows and Ubuntu. The [managed installer results](docs/route-redirect.md#managed-installer-verification) record the separate live checks for OS trust, service supervision and restore.

### Live results, 25–26 November 2026

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

**Built-in browsers.** The stock T3 server-hosted Browser panel on macOS, Windows and Ubuntu followed a managed alias to the native PR 118 authentication page through machine routing. Each browser preserved the query and fragment. The installed desktop apps also reached the native page. The Windows and Ubuntu desktop checks used isolated resolver and trust settings. [Verification details](docs/route-redirect.md#machine-wide-installation-results)

### Windows 11 x64 and Ubuntu 26.04 ARM64

The installed Windows T3 Nightly desktop app and the backend from the Ubuntu Nightly desktop package both created linked worktrees and resolved the expected PR across all four combinations of collection and transport. Git fetched with `TFSAZ_CONFIG` unset. Restore from a linked worktree recovered the native origin for the whole clone. A separate test of the default `repo setup` pushed a commit from a linked worktree in each of the eight OS, collection and transport combinations. Reading back each branch at the native endpoint confirmed the commit before the test repositories were recycled.

- **Credential stores.** On Ubuntu the automatic desktop login had no default persistent Secret Service collection. An isolated, unlocked GNOME Keyring session handled the test PATs. On Windows, Credential Manager rejected writes directly from an SSH logon. Starting the test process under a password-backed Windows logon with its user profile loaded allowed it to store, use and remove the test credentials.
- **Git clients and TLS.** The live HTTPS tests trusted the testbed CA explicitly and supplied Git credentials scoped to the native collection. The Windows SSH tests passed with Git for Windows’ bundled OpenSSH. With Windows’ system OpenSSH, a direct native Git read authenticated and then stalled on this VM. Confirm native `git ls-remote` works with the chosen client before setup.
- **HTTPS routing.** On each OS, `route resolve` produced native PR URLs and `route run -- git clone` retained the synthetic origin while fetching the native HEAD. In isolated browser profiles, Windows Edge and Ubuntu Firefox refused registered aliases and reached an unrelated local test site directly. Windows WinHTTP selected the loopback HTTP proxy for a registered host. Ubuntu’s GNOME GIO resolver selected loopback SOCKS5 followed by the loopback HTTP proxy. Both resolvers selected DIRECT for an unrelated host. The browser checks used isolated proxy configurations.
- **Windows desktop links.** Windows listed **tfsaz Links** for HTTP and HTTPS. When the shell explicitly selected that handler, the browser receiver got the native PR URL with its encoded query and fragment intact. Ordinary HTTP links passed through unchanged. Edge also opened a local page in a disposable profile. The tests covered installation, removal and a registration collision. The installed T3 Nightly app displayed the linked PR. Shell dispatch and navigation inside T3’s built-in browser were verified separately. The destination of an external-handler click from the Windows T3 app remains unverified.
- **Ubuntu desktop links.** Clicking **Open on Azure DevOps** in the installed T3 Nightly app with its sandbox enabled delivered the native PR URL to a browser receiver through GIO and the exported handler. The tests used isolated desktop defaults for HTTP and HTTPS. After repo restore, the handler refused the former alias on the next click. Firefox also opened a local test page in a disposable profile.

### Feature coverage at this T3 revision

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
