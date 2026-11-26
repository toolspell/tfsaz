# Browser routing for registered aliases

Use `tfsaz route setup` to open a managed Azure-style link directly in a browser, including T3’s built-in Browser panel. The local service redirects the browser to the real collection. It serves exact `tfsaz-<24 hex>.visualstudio.com` names registered by `tfsaz init` or `tfsaz repo setup`, including a matching alias explicitly configured on a profile.

## Managed browser routing

Run the following on the machine that runs your browser. Its tfsaz configuration or local checkout registry must contain the exact alias used in the link and the real collection URL. The redirector needs no PAT. For the T3 web client's Browser panel, use the account that runs the T3 server. Use a tfsaz binary at a permanent path. If an upgrade moves or removes the installed binary, rerun `tfsaz route setup` from the new installation to refresh the supervised service, then run `tfsaz route verify`.

```sh
tfsaz route setup
tfsaz route verify
```

Setup creates one 90-day certificate for the current set of exact registered aliases. It stores the certificate, private key and installation identity beside the selected configuration in `<config-file>.browser-route`. It reuses that identity on retry. If setup stops partway through, rerun it under the same account and configuration to finish, or run `tfsaz route restore` to undo the completed steps. A second copy of the public certificate in `~/.tfsaz/route-recovery` lets restore identify OS settings if the config directory is deleted. Browser sign-in is handled by the native collection, which needs its own trusted certificate.

| Platform | What setup installs | What the operator needs |
| --- | --- | --- |
| macOS | Two on-demand user LaunchAgents. Launchd owns the IPv4 and IPv6 loopback sockets on port 443 and passes each socket to tfsaz. Setup adds hostname-scoped SSL trust in the login keychain and the alias hosts entries. | A logged-in desktop session for the agents and Keychain approval, plus administrator access to edit hosts. |
| Ubuntu | A systemd user service, trusted-peer entries in existing Chromium NSS databases, hosts entries and a dedicated nftables table with a boot-time unit. | A systemd user session, sudo access, nftables and `certutil` from `libnss3-tools`. Start Chromium once so its NSS database exists. |
| Windows 11 | A per-user logon task on port 443, the exact leaf in the current user’s trust store and hosts entries. | A desktop session for certificate confirmation and elevation for the hosts edit. |

Use a configuration file in your private user directory. The redirector needs both loopback addresses and available port 443. On Ubuntu the port-forwarding rule matches loopback HTTPS connections opened by the installing user. It can also affect that user’s other connections to local HTTPS servers on port 443. Restore the route before repurposing that port. The browser also needs to trust the native collection. Firefox profiles and clients with independent CA stores may require a separate trust entry.

Use `tfsaz route verify 'AZURE_STYLE_PR_URL'` to check a particular link. The CLI checks local resolution, makes a HEAD request through each loopback address on port 443, verifies the exact managed certificate and compares the redirect with the configured destination. Open the printed URL in the browser you actually use and compare the address bar with the expected URL. This last step checks that browser's trust and proxy configuration.

Stop opening alias links before running `tfsaz route restore`. Restore uses the saved identity and works after a checkout or collection profile has been removed. Rerun restore if the OS cleanup is interrupted. After changing the set of registered names or before the 90-day certificate expires, run restore followed by setup. Clones and worktrees sharing an existing alias can reuse the current certificate. Public DNS can resolve the synthetic names between those commands.

If a T3 tab reports a certificate authority error immediately after setup, run `tfsaz route verify` and try the link in a fresh tab. This succeeded on the test Mac. If the error persists, check which hostname the browser rejected. The alias leaf and the native collection certificate establish trust for two separate connections.

On Windows, setup and restore edit the hosts file in place. During the edit, `hosts.tfsaz-journal` beside it records the original and intended contents. After an interruption, rerun the same command. If tfsaz reports a conflicting edit, it keeps both files. Review the journal, preserve the wanted hosts entries, then remove the journal and retry.

## What it does and refuses

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

## Routing compared with a proxy

| Approach | Tradeoff |
| --- | --- |
| Redirect (this service) | The browser ends up on the real origin, so its address bar, cookies, sign-in and same-origin checks all belong to the real server. Needs a hosts entry, a running service, port 443 and local trust for one leaf. |
| Reverse proxy | Would serve the collection under the synthetic origin. tfsaz would have to carry session cookies or credentials, rewrite absolute links and reconcile the server's origin checks. tfsaz deliberately does not do this. |
| Desktop link handler | Needs no certificate or service but only sees links the OS hands off, not typed URLs or in-browser clicks. |
| Chromium extension | Per profile and Chromium only, with no TLS involved. Covers in-browser clicks in that profile. |
| PAC / SOCKS refusal | Blocks registered names for clients that consult the proxy setting but does not redirect them. |

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

For a supervised installation on any of the three platforms, use [managed browser routing](#managed-browser-routing). If you supervise a manual listener yourself, keep the executable, config, certificate and key at stable paths and run the service under the intended user account.

## Verify

Use an actual PR URL from the managed checkout. A repository-bound alias only accepts paths belonging to that project and repository. Set `pr_url` to the full HTTPS URL and run these commands from the directory containing your manual certificate.

```sh
tfsaz route resolve "$pr_url"
curl -4 --noproxy '*' --cacert alias.pem -sSI "$pr_url"
curl -6 --noproxy '*' --cacert alias.pem -sSI "$pr_url"
curl --noproxy '*' --cacert alias.pem -sSI -X POST "$pr_url"
```

Expect HTTP 302 on both address families and HTTP 405 for POST. The 302 Location should match the resolved URL up to the fragment. The browser carries the fragment itself. The curl probes explicitly trust the leaf. Open the same URL in the intended browser to check its normal trust and compare the full destination in its address bar.

## Remove

1. Stop using the alias in browsers and other clients. Once the hosts entry is removed, public DNS can resolve the name to Azure again. Stop the service and its supervisor entry.
2. Remove the alias lines from the hosts file.
3. Remove the port redirect if installed, including both address families.
4. Remove the certificate trust as listed in step 3.
5. Delete `alias.pem` and `alias-key.pem`.

## T3 desktop browser verification

In the installed T3 Nightly desktop app, a disposable Electron profile mapped one exact generated alias to the redirector’s high loopback port and trusted the corresponding leaf certificate for that origin. Opening the raw alias reached the real Azure DevOps Server PR 118 authentication page with the expected query and fragment. After the redirector stopped, a fresh navigation failed with `ERR_CONNECTION_REFUSED`. This test used a process-local hostname mapping.

The installed T3 desktop apps on Ubuntu ARM64 and Windows x64 also opened the exact live PR 118 URL, including its query and fragment. Both displayed the native server’s authentication-required page. Before the redirector started and after it stopped, fresh navigations failed on the alias with `ERR_CONNECTION_REFUSED`. These tests used a process-local hostname mapping and exact certificate approvals in disposable T3 browser profiles. They ran over SSH with RDP closed.

## macOS web client Browser panel verification

A separate test opened a PR link in the T3 web client's Browser panel backed by the installed Nightly server's sandboxed Chromium. The raw alias redirected to a loopback HTTPS fixture with the expected collection path, encoded query and fragment. The destination received neither an Authorization header nor a Referer. An unsupported API path stayed on the alias, and a new navigation after shutdown failed with `ERR_CONNECTION_REFUSED`.

The test used a scratch Chromium launcher to supply the hostname mapping and certificate-error exceptions scoped to two test public keys. It exercised the server-side navigation path. It did not establish production DNS or certificate trust for that runtime or authenticate to a live collection. Install the production routing on the server that runs Chromium. Opening the same URL in an external browser uses that browser's machine and trust settings instead.

## Machine-wide installation results

- **macOS.** With real IPv4 and IPv6 hosts entries, a temporary `pf` anchor translated loopback port 443 to the high-port service. Both address families returned the correct 302. The exact self-signed leaf was installed in the System keychain with administrator-domain SSL trust limited to the alias. A noninteractive SSH command could import the certificate, but setting its trust required interactive authorization. Merely seeing the certificate in Keychain Access did not establish trust. Apple's `security verify-cert -c alias.pem -p ssl -s "$alias"` verified the policy. Both the installed desktop Electron tab and T3's server-hosted Chromium tab reached the exact live PR authentication page with query and fragment, using normal DNS and browser trust. The desktop tab reported `ERR_CONNECTION_REFUSED` when the service stopped. The bundled `/usr/bin/curl` still reported TLS error 60 on this Mac unless given `--cacert alias.pem`. After removal of the trust policy, the macOS verifier rejected the leaf again. The test removed the certificate copies, hosts entries, temporary anchor and PF enable reference.
- **Ubuntu 26.04.** With real hosts entries, IPv4 and IPv6 NAT rules, and the exact leaf added to the system certificate bundle, curl received the correct 302 over both address families. Snap Chromium followed the redirect after the same leaf was trusted separately in its NSS database. The destination then required its own certificate trust. Stopping the listener produced connection refusal. Removing trust while it was running restored certificate rejection. The test restored the hosts file, NAT rules and trust entries.

- **Windows 11.** Both hosts entries resolved locally, and the service bound IPv4 and IPv6 loopback port 443 directly in an elevated session. With the exact alias leaf in the Local Machine store, Schannel curl, WinHTTP and .NET returned the expected 302. Headless Edge used its normal resolver and trust store, preserved the fragment and reached the native URL. The destination certificate was untrusted on this VM, so Edge stopped there. Stopping the redirector produced connection refusal. Removing trust and restarting restored certificate rejection. The test removed the leaf by thumbprint and verified the hosts file's original bytes, ACL and file ID.

An earlier isolated web-client test on **Ubuntu ARM64 and Windows x64** reached the exact live PR authentication page in the server-hosted Browser panel, including the query and fragment. Stopping the redirector produced `ERR_CONNECTION_REFUSED` on both. These isolated tests used a Chromium launcher with a hostname override and certificate-error exceptions limited to the two fixture public keys. Windows ran sandboxed. The Ubuntu run disabled the server browser sandbox for that test.

A subsequent Ubuntu test used the installed `t3 serve --mode web`, ordinary hostname resolution and Chromium's normal trust settings. The account running T3 trusted the exact alias and destination certificates as peers in its NSS database. The installed `t3 browser setup` command configured AppArmor. The renderer ran sandboxed with seccomp filtering. The Browser panel reached the real PR 118 authentication page with the expected query and fragment. With the redirector stopped and both hosts entries and NAT rules still in place, the browser reported `ERR_CONNECTION_REFUSED`. The test removed its AppArmor profile, hosts entries, NAT rules, private key and scratch browser profile.

On Windows, the installed `t3 serve` also reached PR 118 in its stock server-hosted browser with machine routing. With only the hosts entries present, the connection was refused. With the listener running and the alias certificate untrusted, the browser reported `ERR_CERT_AUTHORITY_INVALID`. After the exact alias leaf was trusted in Local Machine Root and the native server leaf in Local Machine Trusted People, the running browser reached the authentication page with the expected query and fragment. Stopping the service produced `ERR_CONNECTION_REFUSED`. The test restored the original hosts file and removed both test trust entries.

## Managed installer verification

On Ubuntu 26.04 ARM64, setup installed the systemd units, two hosts entries, trusted-peer certificate and IPv4/IPv6 nftables rules scoped to the installing UID. The verifier received the expected 302 on both addresses. Repeating setup kept one installation. Verification failed after stopping the service and passed after rerunning setup. A separate run confirmed that systemd restarted a killed service. Restore also succeeded after moving the installation directory aside and changing `XDG_DATA_HOME`. It removed the original NSS entry, hosts lines, units, nftables table, port reservation and recovery certificate. The hosts file matched its original bytes. This installer check used a fixture URL. The live T3 browser and sign-in-page results above were obtained separately.

On macOS, a live test loaded the generated LaunchAgents in the GUI session on port 443. Each served the expected HTTPS redirect and exact certificate on its own loopback address. Unloading the agents released the port. The test used temporary plists and pinned the leaf for the probe. A subsequent run of the managed setup installed the generated leaf in the login keychain. Its SHA-256 matched the served leaf, and macOS verified it for the exact alias. The first T3 preview navigation reported a certificate authority error. A fresh incognito tab then reached the native PR authentication page with its query and fragment intact. Restore released port 443, removed the hosts entries and made macOS reject the leaf again.

On Windows 11, setup launched the redirector at Medium integrity even from an elevated SSH session. The scheduled task ran as the logged-in user, with the Administrators group marked deny-only. With the exact test leaf temporarily provisioned in Local Machine Root, verification returned the expected redirects on both loopback addresses. Restore removed the task, listener and hosts entries. It identified the separately provisioned trust and completed on retry after the tester removed that certificate. Over SSH the ordinary per-user certificate import returned `ERROR_NOT_SUPPORTED`. A subsequent 1920 × 1080 RDP test ran setup at Medium integrity. Windows displayed the per-user certificate warning with the expected thumbprint and a UAC prompt for the hosts edit. After approval, both loopback probes passed and Schannel returned the expected PR redirect using normal trust. Restore displayed UAC and certificate-removal prompts. The test verified removal of the user trust entry, hosts entries, listener and task, then closed RDP.

The Windows task runs PowerShell in a headless console at normal priority. If the redirector exits with an error, the launcher waits ten seconds and starts it again. A successful exit ends the launcher. With RDP disconnected, a native test started the generated task, observed a failed action restart, stopped the task and checked that it stayed stopped past the next retry. A separate native test verified paths with spaces, an apostrophe and non-ASCII characters through the actual task action.

Windows hosts-file tests use a disposable file with the system hosts file’s owner, group and access rules. They compare the full security descriptor and NTFS file ID before and after repeated installs, restores and interrupted writes. An earlier installer replaced the VM’s actual hosts file and changed its NTFS file ID. Its contents and security descriptor were restored. The current writer keeps the original file in place.
