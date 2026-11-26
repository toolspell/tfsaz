# Browser routing for registered aliases

Use `tfsaz route setup` to open a managed Azure-style link directly in a browser, including T3’s built-in Browser panel. The local service redirects the browser to the real collection, where browser sign-in takes place. It serves exact `tfsaz-<24 hex>.visualstudio.com` names registered by `tfsaz init` or `tfsaz repo setup`, including a matching alias explicitly configured on a profile. For links an app hands to the OS default browser, the [desktop link handler](desktop-link-handler.md) is an alternative that needs no administrator access.

## Managed browser routing

Configure the machine **where the browser runs**:

- For T3 Desktop and an external browser, this is the workstation.
- For the web client’s server-hosted Browser panel, it is the T3 server, under the account that runs the T3 server and launches Chromium.
- If the browser runs on another host, configure the same alias and native collection on that host as well. Its tfsaz configuration or local checkout registry must contain the exact alias used in the link and the real collection URL.

The redirector needs no PAT. Use a tfsaz binary at a permanent path. If an upgrade moves or removes the installed binary, rerun `tfsaz route setup` from the new installation to refresh the supervised service, then run `tfsaz route verify`.

```sh
tfsaz route setup
tfsaz route verify
```

### Certificate and recovery

Setup creates one 90-day certificate for the current set of exact registered aliases. It stores the certificate, private key and installation identity beside the selected configuration in `<config-file>.browser-route`. It reuses that identity on retry. If setup stops partway through, rerun it under the same account and configuration to finish, or run `tfsaz route restore` to undo the completed steps. A second copy of the public certificate in `~/.tfsaz/route-recovery` lets restore identify OS settings if the config directory is deleted. Browser sign-in is handled by the native collection, which needs its own trusted certificate.

### What setup installs

| Platform | What setup installs | What the operator needs |
| --- | --- | --- |
| macOS | Two on-demand user LaunchAgents. Launchd owns the IPv4 and IPv6 loopback sockets on port 443 and passes each socket to tfsaz. Setup adds hostname-scoped SSL trust in the login keychain and the alias hosts entries. | A logged-in desktop session for the agents and Keychain approval, plus administrator access to edit hosts. |
| Ubuntu | A systemd user service, trusted-peer entries in existing Chromium NSS databases, hosts entries and a dedicated nftables table with a boot-time unit. | A systemd user session, sudo access, nftables and `certutil` from `libnss3-tools`. Start Chromium once so its NSS database exists. |
| Windows 11 | A per-user logon task on port 443, the exact leaf in the current user’s trust store and hosts entries. | A desktop session for certificate confirmation and elevation for the hosts edit. |

### Requirements and side effects

Use a configuration file in your private user directory. The redirector needs both loopback addresses and available port 443. On Ubuntu the port-forwarding rule matches loopback HTTPS connections opened by the installing user. It can also affect that user’s other connections to local HTTPS servers on port 443. Restore the route before repurposing that port. The browser also needs to trust the native collection. Firefox profiles and clients with independent CA stores may require a separate trust entry.

### Check a link

Use `tfsaz route verify 'AZURE_STYLE_PR_URL'` to check a particular link. The CLI checks local resolution, makes a HEAD request through each loopback address on port 443, verifies the exact managed certificate and compares the redirect with the configured destination. Open the printed URL in the browser you actually use and compare the address bar with the expected URL. It should show the real collection, including the query and fragment. This last step checks that browser's trust and proxy configuration.

### Refresh or remove

Stop opening alias links before running `tfsaz route restore`, which removes the managed installation. Restore uses the saved identity and works after a checkout or collection profile has been removed. Rerun restore if the OS cleanup is interrupted. After adding or retiring aliases, or before the 90-day certificate expires, run restore followed by setup to refresh the certificate and hosts entries. Clones and worktrees sharing an existing alias can reuse the current certificate. Public DNS can resolve the synthetic names between those commands.

### Troubleshooting

If a T3 tab reports a certificate authority error immediately after setup, run `tfsaz route verify` and try the link in a fresh tab. This succeeded on the test Mac. If the error persists, check which hostname the browser rejected. The alias leaf and the native collection certificate establish trust for two separate connections.

On Windows, setup and restore edit the hosts file in place. During the edit, `hosts.tfsaz-journal` beside it records the original and intended contents. After an interruption, rerun the same command. If tfsaz reports a conflicting edit, it keeps both files. Review the journal, preserve the wanted hosts entries, then remove the journal and retry.

## What it does and refuses

Why tfsaz redirects instead of proxying is explained in [Routing compared with a proxy](../internals/architecture.md#routing-compared-with-a-proxy).

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

With manual installation, issue a new leaf and update hosts and trust when the set of names changes. Restart the listener with the matching certificate and key.

## Manual setup

Use this alternative when you intend to manage the listener, trust and hosts entries yourself. Start with a registered alias of the form shown below. Choose either the managed installation or the manual listener for port 443. Record each manual change for removal.

### 1. Find the alias names

```sh
tfsaz route hosts | awk '$1 == "127.0.0.1" && $2 ~ /^tfsaz-[0-9a-f]{24}\.visualstudio\.com$/ {print $2}'
```

### 2. Create an exact leaf for those names

Run this in a private directory, here `~/.config/tfsaz-redirect`. Repeat `DNS:` entries comma-separated for several aliases. Use a self-signed server leaf with exact DNS names, `CA:FALSE` and digital-signature usage. Keep its private key in the owner-only directory and out of shared backups. Windows installs the leaf as a trust anchor. Individual clients can apply different constraints to certificates in that store.

```sh
dir="$HOME/.config/tfsaz-redirect"; mkdir -p "$dir"; chmod 700 "$dir"; cd "$dir"
alias=tfsaz-0123456789abcdef01234567.visualstudio.com   # from step 1
(umask 077; openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 90 \
  -subj "/CN=$alias" -addext "subjectAltName=DNS:$alias" \
  -addext "basicConstraints=critical,CA:FALSE" -addext "keyUsage=critical,digitalSignature" \
  -addext "extendedKeyUsage=serverAuth" -keyout alias-key.pem -out alias.pem)
```

On Windows, Git for Windows ships `openssl.exe`. Keep the key under your user profile. Regenerate before the 90 days run out.

### 3. Trust only that leaf

Record its SHA-256 fingerprint before installation. On macOS, use `leaf_sha256=$(openssl x509 -in alias.pem -noout -fingerprint -sha256 | cut -d= -f2 | tr -d :)` for removal. Windows certutil identifies the individual certificate by its SHA-1 thumbprint, which you can read with `certutil -dump alias.pem`.

| OS | Add | Remove |
| --- | --- | --- |
| macOS | `security add-trusted-cert -r trustRoot -p ssl -s "$alias" -k ~/Library/Keychains/login.keychain-db alias.pem` (asks for your password; the trust is limited to SSL for that name) | `security remove-trusted-cert alias.pem` and `security delete-certificate -Z "$leaf_sha256" ~/Library/Keychains/login.keychain-db` |
| Windows | `certutil -user -addstore Root alias.pem` (per-user store; confirm the prompt) | `certutil -user -delstore Root LEAF_SHA1_THUMBPRINT` |
| Ubuntu Chrome/Chromium | `certutil -d "sql:$nssdb" -A -t "P,," -n tfsaz-redirect -i alias.pem` (`P` means trusted peer, not a CA) | `certutil -d "sql:$nssdb" -D -n tfsaz-redirect` |

Set `nssdb` to the database the target browser uses. On Ubuntu 26.04, snap Chromium 158 used `$HOME/snap/chromium/current/.local/share/pki/nssdb` after its first launch. Other installations can use `$XDG_DATA_HOME/pki/nssdb`, `$HOME/.local/share/pki/nssdb` or the older `$HOME/.pki/nssdb`. The `libnss3-tools` package supplies `certutil`. An Ubuntu system trust entry made curl accept the alias, while this Chromium build also required the NSS entry.

For the T3 web client's server-hosted Browser panel, configure trust for the account running Chromium. T3 Desktop can also remember a verified certificate in its own browser profile. Compare the displayed fingerprint with the leaf you created.

For machine-wide macOS trust, the tested sequence was to add the leaf to the System keychain with `sudo security add-certificates -k /Library/Keychains/System.keychain alias.pem`, then run `security add-trusted-cert -d -r trustRoot -p ssl -s "$alias" alias.pem` in an interactive terminal and approve the policy. Verify it with `security verify-cert -c alias.pem -p ssl -s "$alias" -L`. For cleanup, run `security remove-trusted-cert -d alias.pem`, then remove only that fingerprint from each keychain containing it using `security delete-certificate -Z "$leaf_sha256" KEYCHAIN`. Removing the System keychain copy requires administrator privileges.

On Windows 11 the per-user `certutil -user -addstore Root alias.pem` failed with `ERROR_NOT_SUPPORTED` over SSH. Use an interactive desktop for the per-user confirmation. For a machine-wide install from an elevated session, `certutil -addstore Root alias.pem` adds the exact leaf to the Local Machine store. Remove it with `certutil -delstore Root LEAF_SHA1_THUMBPRINT` and confirm that thumbprint is gone. The destination Azure DevOps Server also needs a certificate the browser trusts.

### 4. Point the alias at loopback

For each name on your certificate, copy its IPv4 and IPv6 lines from `tfsaz route hosts` to the system hosts file. That command can also print custom profile hostnames outside the redirector’s accepted name format. `tfsaz route verify-hosts` checks every hostname from its list. The curl checks below exercise the particular alias you installed.

### 5. Run the service on port 443

| OS | How |
| --- | --- |
| Windows | `tfsaz route redirect --cert alias.pem --key alias-key.pem`. Pick another approach if IIS or another service already holds port 443. |
| Linux | If the host permits unprivileged binding, run the listener directly on port 443. Otherwise run on a free high port such as `--port 8443` and forward loopback traffic. The manual test used `sudo iptables -t nat -A OUTPUT -o lo -p tcp -d 127.0.0.1 --dport 443 -j REDIRECT --to-ports 8443` plus the matching `ip6tables` rule for `::1` (remove them with `-D` instead of `-A`). |
| macOS | The direct listener on the test Mac required privilege on port 443. Managed setup uses launchd socket activation. For a manual installation, use a free high port. Run `tfsaz route redirect --cert alias.pem --key alias-key.pem --port 8443` and add a loopback port redirect in a private `pf` anchor, as shown below. |

The example below sends all port 443 connections for the two listed loopback addresses to tfsaz until you remove the rule. Check for other local HTTPS services before installing it. On macOS the rule lasts until removal or reboot.

```sh
printf '%s\n' \
  'rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 443 -> 127.0.0.1 port 8443' \
  'rdr pass on lo0 inet6 proto tcp from any to ::1 port 443 -> ::1 port 8443' \
  | sudo pfctl -a com.apple/tfsaz-redirect -f -
sudo pfctl -E    # prints a token; keep it for removal
```

Remove with `sudo pfctl -a com.apple/tfsaz-redirect -F all` and `sudo pfctl -X TOKEN`. The stock `/etc/pf.conf` evaluates `com.apple/*` anchors, so no edit to that file is needed.

For a supervised installation on any of the three platforms, use [managed browser routing](#managed-browser-routing). Its live checks are recorded in [Browser routing verification](../verification/browser-routing.md). If you supervise a manual listener yourself, keep the executable, config, certificate and key at stable paths and run the service under the intended user account.

## Verify the manual setup

Use an actual PR URL from the managed checkout. A repository-bound alias only accepts paths belonging to that project and repository. Set `pr_url` to the full HTTPS URL and run these commands from the directory containing your manual certificate.

```sh
tfsaz route resolve "$pr_url"
curl -4 --noproxy '*' --cacert alias.pem -sSI "$pr_url"
curl -6 --noproxy '*' --cacert alias.pem -sSI "$pr_url"
curl --noproxy '*' --cacert alias.pem -sSI -X POST "$pr_url"
```

Expect HTTP 302 on both address families and HTTP 405 for POST. The 302 Location should match the resolved URL up to the fragment. The browser carries the fragment itself. The curl probes explicitly trust the leaf. Open the same URL in the intended browser to check its normal trust and compare the full destination in its address bar.

## Remove the manual setup

1. Stop using the alias in browsers and other clients. Once the hosts entry is removed, public DNS can resolve the name to Azure again. Stop the service and its supervisor entry.
2. Remove the alias lines from the hosts file.
3. Remove the port redirect if installed, including both address families.
4. Remove the certificate trust as listed in step 3.
5. Delete `alias.pem` and `alias-key.pem`.
