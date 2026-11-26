# Browser routing for registered aliases

Use `tfsaz route setup` to open a managed checkout's synthetic URLs directly in a browser, including T3's built-in Browser panel. The local service redirects the browser to the real collection, where browser sign-in takes place. It serves exact `tfsaz-<24 hex>.visualstudio.com` names registered by `tfsaz init` or `tfsaz repo setup`, including a matching alias explicitly configured on a profile.

Alternatives:

- For links an app hands to the OS default browser, the [desktop link handler](desktop-link-handler.md) needs no administrator access.
- To manage the listener, trust and hosts entries yourself, follow [Manual browser routing](browser-routing-manual.md).

## Managed browser routing

Configure the machine **where the browser runs**:

- For T3 Desktop and an external browser, this is the workstation.
- For the web client's server-hosted Browser panel, it is the T3 server, under the account that runs the T3 server and launches Chromium.
- If the browser runs on another host, configure the same alias and native collection on that host as well. Its tfsaz configuration or local checkout registry must contain the exact alias used in the link and the real collection URL.

The redirector needs no PAT. Use a tfsaz binary at a permanent path. If an upgrade moves or removes the installed binary, rerun `tfsaz route setup` from the new installation to refresh the supervised service, then run `tfsaz route verify`.

```sh
tfsaz route setup
tfsaz route verify
```

### What setup installs

| Platform | What setup installs | What the operator needs |
| --- | --- | --- |
| macOS | Two on-demand user LaunchAgents. Launchd owns the IPv4 and IPv6 loopback sockets on port 443 and passes each socket to tfsaz. Setup adds hostname-scoped SSL trust in the login keychain and the alias hosts entries. | A logged-in desktop session for the agents and Keychain approval, plus administrator access to edit hosts. |
| Windows 11 | A per-user logon task on port 443, the exact leaf in the current user's trust store and hosts entries. | A desktop session for certificate confirmation and elevation for the hosts edit. |
| Ubuntu | A systemd user service, trusted-peer entries in existing Chromium NSS databases, hosts entries and a dedicated nftables table with a boot-time unit. | A systemd user session, sudo access, nftables and `certutil` from `libnss3-tools`. Start Chromium once so its NSS database exists. |

### Check what is installed

`tfsaz route verify` checks that routing works now: resolution to both loopback addresses and the redirect through the managed certificate. It does not check browser or OS trust, and it does not list the OS entries. To see them, look for these names. `ID` is the 16-hex installation ID that every entry of one configuration shares; the certificate's common name is the first alias.

| OS | Entry | Check |
| --- | --- | --- |
| macOS | LaunchAgents `tfsaz.route-redirect.ID.ipv4` and `.ipv6` in `~/Library/LaunchAgents` | `launchctl list \| grep tfsaz.route-redirect` |
| macOS | The leaf and its SSL trust in the login keychain | `security find-certificate -a -c tfsaz- ~/Library/Keychains/login.keychain-db` |
| Windows | Scheduled task `\tfsaz\route-redirect-ID` | `Get-ScheduledTask -TaskPath '\tfsaz\'` |
| Windows | The leaf in the current user's Root store | `Get-ChildItem Cert:\CurrentUser\Root \| Where-Object Subject -like '*tfsaz-*'` |
| Linux | User unit `tfsaz-route-redirect-ID.service` | `systemctl --user list-unit-files 'tfsaz-route-redirect-*'` |
| Linux | Boot unit `tfsaz-route-redirect-ID-nat.service`, `/etc/tfsaz-route-ID.nft`, nftables table `inet tfsaz_route_ID`, port reservation `/etc/tfsaz-browser-port-443.owner` | `systemctl list-unit-files 'tfsaz-route-redirect-*'`, `sudo nft list tables`, `ls /etc/tfsaz-*` |
| Linux | NSS entry `tfsaz-route-redirect-ID` in each Chromium database that existed at setup | `certutil -d "sql:$nssdb" -L` ([where `nssdb` is](browser-routing-manual.md#nss-database-on-ubuntu)) |
| All | Two hosts lines per alias, ending in `# tfsaz-route ID` | `grep tfsaz-route /etc/hosts`; on Windows `Select-String tfsaz-route "$env:SystemRoot\System32\drivers\etc\hosts"` |

### Certificate and recovery

Setup creates one 90-day certificate for the current set of exact registered aliases. It stores the certificate, private key and installation identity beside the selected configuration in `<config-file>.browser-route`. It reuses that identity on retry.

- If setup stops partway through, rerun it under the same account and configuration to finish, or run `tfsaz route restore` to undo the completed steps.
- A second copy of the public certificate in `~/.tfsaz/route-recovery` lets restore identify OS settings if the config directory is deleted.
- Browser sign-in is handled by the native collection, which needs its own trusted certificate.

### Requirements and side effects

- Use a configuration file in your private user directory.
- The redirector needs both loopback addresses and available port 443.
- On Ubuntu the port-forwarding rule matches loopback HTTPS connections opened by the installing user. It can also affect that user's other connections to local HTTPS servers on port 443. Restore the route before repurposing that port.
- The browser also needs to trust the native collection. Firefox profiles and clients with independent CA stores may require a separate trust entry.

### Check a link

Use `tfsaz route verify 'AZURE_STYLE_PR_URL'` to check a particular link. tfsaz checks local resolution, makes a HEAD request through each loopback address on port 443, verifies the exact managed certificate and compares the redirect with the configured destination.

Then open the printed URL in the browser you actually use and compare the address bar with the expected URL. It should show the real collection, including the query and fragment. This last step checks that browser's trust and proxy configuration.

### Refresh or remove

- Stop opening alias links before running `tfsaz route restore`, which removes the managed installation: the service, the trust entries, the hosts lines and, on Linux, the boot unit, nftables table and port reservation.
- Restore uses the saved identity and works after a checkout or collection profile has been removed, and after the configuration file is deleted. Run it with the same configuration path (`TFSAZ_CONFIG` or the default) as setup: the installation ID comes from that path, and under another path restore reports "No installation recorded" and changes nothing. Rerun restore if the OS cleanup is interrupted.
- Restore keeps files you added to `<config-file>.browser-route` and stops with a message; move them out and rerun it.
- On Windows, restore removes the leaf only from the current user's Root store. If a copy is also in the Local Machine store, restore stops and asks you to remove it from an elevated prompt.
- Restore leaves the directory `~/.tfsaz/route-recovery` with a small `.lock` file per configuration. Once `route restore` has succeeded for every configuration that used routing, delete `~/.tfsaz`.
- On Linux, run restore with the same `XDG_CONFIG_HOME` as setup, or the user unit stays behind.
- After adding or retiring aliases, or before the 90-day certificate expires, run restore followed by setup to refresh the certificate and hosts entries. Clones and worktrees sharing an existing alias can reuse the current certificate.
- Public DNS can resolve the alias hosts between those commands.

### Troubleshooting

**Certificate authority error in a T3 tab right after setup.** Run `tfsaz route verify` and try the link in a fresh tab ([observed on macOS](../verification/browser-routing.md#managed-installer-verification)). If the error persists, check which hostname the browser rejected. The alias leaf and the native collection certificate establish trust for two separate connections.

**Interrupted hosts edit on Windows.** On Windows, setup and restore edit the hosts file in place. During the edit, `hosts.tfsaz-journal` beside it records the original and intended contents. After an interruption, rerun the same command. If tfsaz reports a conflicting edit, it keeps both files. Review the journal, preserve the wanted hosts entries, then remove the journal and retry.

## What the redirector does and refuses

The redirector sends the browser to the real collection instead of serving the collection itself. The browser therefore signs in at the real server, in its own session. Why tfsaz redirects instead of proxying is explained in [Routing compared with a proxy](../internals/architecture.md#routing-compared-with-a-proxy).

| Aspect | Behavior |
| --- | --- |
| Listener | `127.0.0.1` and `::1` only, default port 443, TLS 1.2+ and HTTP/1.1. |
| Names served | Only exact generated aliases of active setups or profile aliases. Shared hosts (`dev.azure.com`, ordinary `*.visualstudio.com` organizations) and real collection hosts are never served. |
| Certificate | Must be an unexpired leaf (`CA:FALSE`, `extendedKeyUsage=serverAuth`) whose DNS names are all registered aliases. Wildcards, IP addresses, URIs, e-mail names and CA certificates are refused at startup. Supply a single leaf PEM in `--cert`. On macOS and Linux the key file must grant no group or other permissions. Mode `600` works. |
| TLS name | A handshake without SNI, or with any name outside the certificate, fails before a request is read. |
| Requests | `GET` and `HEAD` only. SNI and `Host` must name the same alias, with no port or the listening port. Requests with a body or an `Authorization` header are refused. |
| Paths | Only the pages `tfsaz route resolve` accepts: repository, PR, commit, work item and build. Git endpoints, `_apis` and other paths get `404`. |
| Response | `302 Found` with the real collection URL in `Location`, `Cache-Control: no-store` and `Referrer-Policy: no-referrer`. The browser carries the original `#fragment` to the destination. It never sends the fragment to the redirector, so only the path and query can be validated. |
| Data | It never proxies, forwards or logs requests and reads no PAT or keyring entry. Sign-in happens at the real collection in the browser's own session. |
| Stale setups | The config and repo registry are reread on every request. After the last binding for a checkout alias is removed, requests for that alias get `404` while the original listener is running. |

Live checks on all three platforms are recorded in [Browser routing verification](../verification/browser-routing.md).
