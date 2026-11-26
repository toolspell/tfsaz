# Security

## Report a vulnerability

Report it privately through [GitHub's private vulnerability reporting](https://github.com/toolspell/tfsaz/security/advisories/new), the **Report a vulnerability** button on the repository's Security tab. Do not open a public issue, PR or discussion about it.

Include the commit you tested, the boundary below that the finding crosses, the operating system, and the shortest steps or test that show it. Until there are releases, only the latest `main` is supported.

The rest of this file is for agents and maintainers auditing TFSaz or triaging a report. The reasons behind the rules are in [Architecture](docs/internals/architecture.md).

## The model in one paragraph

TFSaz runs as the OS user that runs the T3 backend, as the `az` shim and as `git-remote-tfsaz`. Its one secret is a PAT per collection, kept in the OS keyring. Security work here is about where a PAT can travel, which identity a command acts as, and what the routing features change on the machine: they edit the hosts file, trust a local certificate and run a loopback service so a synthetic URL reaches the real collection.

## What TFSaz protects

| Asset | Rule | Where it is enforced |
| --- | --- | --- |
| PATs | Stored only through go-keyring: macOS Keychain, Windows Credential Manager, Linux Secret Service. Read from a hidden prompt or stdin, never from an argument, configuration file or log. | `internal/az/credentials.go` |
| Where a PAT goes | Only to its profile's real `collectionUrl`. An Azure-style alias or an absolute `az rest --url` only selects a profile; a URL that matches no configured collection is refused. A parent collection's PAT does not reach a nested collection. `--headers` cannot set `Authorization`. | `client.go`, `rest_url.go`, `api_call.go` |
| Redirects | Every authenticated request refuses redirects; a 3xx is an error. | `newClient` in `client.go` |
| TLS | System trust plus the profile's optional CA file. Verification is never disabled on an authenticated connection. | `client.go` |
| Identity | A checkout, remote or `--organization` selects exactly one profile. If its PAT fails, the command fails; it never falls back to another collection's identity. Discovery (`az account show --query user.name`) reports the first profile that verifies, in configured order, each PAT checked against its own collection. | `cli.go`, `repo_commands.go` |
| Git credentials | `git-remote-tfsaz` and the `route run` Git HTTPS helper never read the keyring. They map the synthetic URL to the native URL and hand over to Git, which authenticates with its own credentials. | `preserve_git.go`, `https_git.go` |
| Trace output | `TFSAZ_TRACE=1` prints the command name, profile ID, selection reason, HTTP status and elapsed time. It omits arguments, URLs, identities, headers and bodies. | `trace.go` |

## What TFSaz changes on the machine

Besides its configuration file, `<config>.repo-setups.json`, `<config>.browser-route` and the resource-catalog cache `<config>.cache` (server responses, no credentials), TFSaz changes only the state below. Each change is deliberate and needs the user's command; [Reversible and verifiable system changes](AGENTS.md#reversible-and-verifiable-system-changes) is the rule, and the [issues labeled `reversibility`](https://github.com/toolspell/tfsaz/issues?q=is%3Aissue%20is%3Aopen%20label%3Areversibility) track where undo or check is still missing.

| Change | Why | Undo | Check |
| --- | --- | --- | --- |
| `az` and `git-remote-tfsaz` in the chosen directory (`shim install`; symlinks, or copies on Windows without symlink rights) | T3 runs `az`; Git runs the remote helper | Delete both files ([Uninstall](docs/user/install.md#uninstall)) | `doctor` checks the `az` on its own PATH |
| A keyring entry per profile, service `tfsaz/v2/<hash>` or `tfsaz`, account the profile ID (`init`, `profile add`, `devops login`) | The PAT | `profile logout` | `doctor` (online) |
| The clone's `remote.origin.url`, `remote.origin.vcs`, `remote.origin.tfsazBinding` or `core.sshCommand`, and a `.ssh.conf` in its Git directory (`repo setup`) | T3 recognizes only Azure-style remotes; Git keeps the native transport | `repo restore` | `repo show`, `repo verify`, `repo list` |
| Hosts lines, a trusted leaf, a loopback service and, on Linux, an nftables redirect (`route setup`) | A browser reaches the real collection from a synthetic URL | `route restore` | `route verify`, plus the names in [Check what is installed](docs/user/browser-routing.md#check-what-is-installed) |
| `~/.tfsaz/route-recovery` (`route setup`) | Restore still finds the OS entries after the configuration is deleted | The last `route restore` removes it and `~/.tfsaz` | Directory listing |
| Temporary Git helper directories in `<config>.run` (`route run`) | Inherited Git HTTPS helper for the child process | Removed when the child exits, `<config>.run` with the last one; delete leftovers after a kill | Directory listing |
| Lock files `<config>.lock`, `<config>.repo-setups.json.lock`, `tfsaz-repo-setup.lock` in a clone's Git directory and `<id>.pem.lock` in `~/.tfsaz/route-recovery` | Serialize concurrent edits | Removed when the command ends; delete leftovers after a kill | Directory listing |
| Exported files only (`route handler-export`, `route browser-export`) | The user registers the handler, picks the default browser and loads the extension | The documented removal steps | `route browser-verify` for the extension files |

Hosts entries and PAC settings from `route hosts` and `route pac` are copied in by the user; TFSaz only prints them. `route proxy`, `route sink` and `route redirect` run in the foreground and change nothing persistent.

## Routing features

These are opt-in. Each changes local state that a reviewer should know about.

### Loopback redirect listener

`route redirect`, and the service `route setup` installs, redirect a browser from an alias host to the real collection. It never proxies and reads no PAT. It refuses:

- any address other than `127.0.0.1` and `::1`, TLS below 1.2, and protocols other than HTTP/1.1;
- a handshake without SNI or for a name outside its certificate, and a `Host` that differs from the SNI;
- methods other than `GET` and `HEAD`, request bodies and `Authorization` headers;
- shared hosts (`dev.azure.com`, ordinary `*.visualstudio.com` organizations) and real collection hosts;
- paths other than repository, PR, commit, work item and build pages, so `_apis` and Git endpoints get `404`;
- at startup, a certificate that is not a single unexpired leaf (`CA:FALSE`, `serverAuth`) naming only registered aliases, and on macOS and Linux a key file readable by group or others.

Every response carries `Cache-Control: no-store` and `Referrer-Policy: no-referrer`. It rereads the configuration on every request, so a restored setup stops redirecting at once. Details: [What the redirector does and refuses](docs/user/browser-routing.md#what-the-redirector-does-and-refuses).

### `route setup` and `route restore`

Setup creates a self-signed 90-day leaf (ECDSA P-256, `CA:FALSE`, exact alias names) and changes system state. Restore undoes it. Both are journaled, and rerunning a command after an interruption finishes or undoes it.

| OS | What setup changes |
| --- | --- |
| macOS | Two user LaunchAgents that own the loopback sockets on port 443; trust in the login keychain limited to SSL and each exact alias name; the hosts file (with administrator rights). |
| Windows | A per-user logon task that listens on port 443; the leaf in the current user's Root store; the hosts file through one elevated script, with an undo journal beside it. |
| Linux | A systemd user service on a high loopback port; through sudo, an nftables table and a boot unit (with its rules file `/etc/tfsaz-route-<id>.nft`) that redirect loopback port 443 for the installing user only, the port reservation `/etc/tfsaz-browser-port-443.owner`, and the hosts file; trusted-peer entries in Chromium NSS databases. |

- On Windows and Linux the trust entry is not limited to a hostname. The protection there is that the certificate is a leaf with exact names and its key stays in the owner-only directory.
- On macOS and Linux setup refuses to run as root; it asks for administrator rights only where the table says. On every OS it stops when another program already holds loopback port 443.
- A copy of the public leaf in `~/.tfsaz/route-recovery` lets restore find the OS entries after the configuration is deleted. Restore removes that copy, and the last restore removes the directory.
- `route verify` checks resolution and the redirect, not trust stores or service registration. The exact names to check by hand are in [Check what is installed](docs/user/browser-routing.md#check-what-is-installed).

### The on-disk key exception

The leaf's private key `alias-key.pem` lives in `<config>.browser-route`, beside the certificate and the installation identity. It is the one secret outside the keyring. The directory is owner-only (mode 700 with 600 files on macOS and Linux, a protected ACL for the user and SYSTEM on Windows). Moving it into the keyring is tracked in [#9](https://github.com/toolspell/tfsaz/issues/9).

Whoever holds the key can impersonate the alias hosts to this user's browsers, which trust the leaf. It cannot impersonate the real collection or obtain a PAT.

### Desktop link handler

`route handler-export` writes a per-user handler (a macOS app, a Windows HKCU ProgID with install scripts, a Linux desktop entry). It never makes itself the default browser; the user chooses it.

- It translates only links on registered aliases with supported page paths and refuses other paths on those aliases.
- Other `http` and `https` links pass unchanged to the chosen browser, which runs without a shell. Other schemes, credentials in the URL and control characters are refused.
- Generated scripts quote their paths; paths that cannot be quoted safely are refused at export.
- It reads no PAT.

### Chromium extension export

`route browser-export` writes an unpacked Manifest V3 extension with the `declarativeNetRequest` permission and host permissions for the registered hosts, which can include `dev.azure.com`. It contains rules that map aliases to collection URLs and no secret. It redirects supported top-level page loads and blocks every other request to a registered alias.

### Proxy and sink

`route proxy` serves a PAC file on `127.0.0.1` and refuses every SOCKS5 and HTTP proxy request; it never connects anywhere. `route sink` accepts and closes connections on the loopback addresses without reading them.

## Out of scope

| Not a finding | Why |
| --- | --- |
| Anything that needs the user's own OS account or an administrator | That user owns the keyring, the configuration, the trust stores and the T3 backend. Another process running as that user can read the alias key and, once the keyring is unlocked, usually its entries too; TFSaz does not defend against that. |
| The Azure DevOps Server itself, its PAT scopes and its TLS setup | TFSaz is a client. A PAT acts with the permissions the server grants it. |
| T3 Code | Bugs in T3, its adapter or its Browser panel belong to T3. TFSaz's contract is the `az` calls T3 makes. |
| Microsoft's Azure CLI | TFSaz does not run or modify it. |
| Git, OpenSSH and Git credential helpers | TFSaz hands Git transport to them. |
| Public DNS resolving an alias host to Azure when no routing is set up | Documented; routing is opt-in. The hosts entries, PAC refusal and sink exist for it. |
| Clients that bypass the hosts file, the PAC or the inherited Git environment | Documented limits of each routing method. |

A real finding crosses a boundary above: a PAT reaching another host or appearing in a file, argument or log; a command acting as another collection's identity; an authenticated redirect being followed; the redirector serving or forwarding something it should refuse; or any command changing more than [What TFSaz changes on the machine](#what-tfsaz-changes-on-the-machine) lists.

## How to audit and triage

- Prove a finding with a failing test at the lowest layer, isolated as [Testing](docs/internals/testing.md) requires. Never run a proof of concept against the real configuration, keyring, hosts file or trust stores ([Ways to hurt yourself](AGENTS.md#ways-to-hurt-yourself)).
- Check the reported commit against `main`.
- Treat report attachments as untrusted. Read a proof of concept before running anything.
- When a boundary here changes, update this file and [Architecture](docs/internals/architecture.md) in the same change.
