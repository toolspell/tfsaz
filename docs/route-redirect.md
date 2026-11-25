# Loopback redirect service for registered aliases

`tfsaz route redirect` is an opt-in personal setup. It catches browser navigations to the dedicated `tfsaz-<24 hex>.visualstudio.com` aliases that `tfsaz repo setup` generates. It answers with a redirect to the real collection page. Use it when a link reaches a browser directly instead of through the OS link handler. Examples are a URL typed or pasted into an address bar, a click in the browser version of T3, or T3's built-in browser.

```text
tfsaz route redirect --cert FILE --key FILE [--port PORT]
```

## What it does and refuses

| Aspect | Behavior |
| --- | --- |
| Listener | `127.0.0.1` and `::1` only, default port 443, TLS 1.2+ and HTTP/1.1. |
| Names served | Only exact generated aliases of active setups or profile aliases. Shared hosts (`dev.azure.com`, ordinary `*.visualstudio.com` organizations) and real collection hosts are never served. |
| Certificate | Must be an unexpired leaf (`CA:FALSE`, `extendedKeyUsage=serverAuth`) whose DNS names are all registered aliases. Wildcards, IP addresses, URIs, e-mail names and CA certificates are refused at startup. On macOS and Linux the key must be mode `600`. |
| TLS name | A handshake without SNI, or with any name outside the certificate, fails before a request is read. |
| Requests | `GET` and `HEAD` only. SNI and `Host` must name the same alias, with no port or the listening port. Requests with a body or an `Authorization` header are refused. |
| Paths | Only the pages `tfsaz route resolve` accepts: repository, PR, commit, work item and build. Git endpoints, `_apis` and other paths get `404`. |
| Response | `302 Found` with the real collection URL in `Location`, `Cache-Control: no-store` and `Referrer-Policy: no-referrer`. The browser carries the original `#fragment` to the destination. It never sends the fragment to the redirector, so only the path and query can be validated. |
| Data | It never proxies, forwards or logs requests and reads no PAT or keyring entry. Sign-in happens at the real collection in the browser's own session. |
| Stale setups | The config and repo registry are reread on every request. After `tfsaz repo restore`, the old alias gets `404` immediately. |

A new setup adds a new alias. Issue a new certificate for it and restart the service, because startup checks the certificate against the registered names.

## Routing compared with a proxy

| Approach | Tradeoff |
| --- | --- |
| Redirect (this service) | The browser ends up on the real origin, so its address bar, cookies, sign-in and same-origin checks all belong to the real server. Needs a hosts entry, a running service, port 443 and local trust for one leaf. |
| Reverse proxy | Would serve the collection under the synthetic origin. tfsaz would have to carry session cookies or credentials, rewrite absolute links and reconcile the server's origin checks. tfsaz deliberately does not do this. |
| Desktop link handler | Needs no certificate or service but only sees links the OS hands off, not typed URLs or in-browser clicks. |
| Chromium extension | Per profile and Chromium only, with no TLS involved. Covers in-browser clicks in that profile. |
| PAC / SOCKS refusal | Blocks registered names for clients that consult the proxy setting but does not redirect them. |

## Personal setup

The steps below require an active `tfsaz repo setup`. They change only what you run yourself. Record each change so you can remove it later.

### 1. Find the alias names

```sh
tfsaz route hosts | awk '$1 == "127.0.0.1" && $2 ~ /^tfsaz-[0-9a-f]{24}\.visualstudio\.com$/ {print $2}'
```

### 2. Create an exact leaf for those names

Run this in a private directory, here `~/.config/tfsaz-redirect`. Repeat `DNS:` entries comma-separated for several aliases. The certificate is self-signed but marked `CA:FALSE`, so trusting it vouches for the listed names only and cannot sign anything else.

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
| Firefox | Settings → Privacy & Security → Certificates → View Certificates → Servers → Import, or the `certutil` command against the profile directory | Delete the entry in the same dialog |

Set `nssdb` to the database the target browser uses. On Ubuntu 26.04, snap Chromium 158 used `$HOME/snap/chromium/current/.local/share/pki/nssdb` after its first launch. Other installations can use `$XDG_DATA_HOME/pki/nssdb`, `$HOME/.local/share/pki/nssdb` or the older `$HOME/.pki/nssdb`. The `libnss3-tools` package supplies `certutil`. An Ubuntu system trust entry made curl accept the alias, while this Chromium build also required the NSS entry.

For the T3 web client's server-hosted Browser panel, configure trust for the account running Chromium. T3 Desktop can also remember a verified certificate in its own browser profile. Compare the displayed fingerprint with the leaf you created.

For machine-wide macOS trust, the tested sequence was to add the leaf to the System keychain with `sudo security add-certificates -k /Library/Keychains/System.keychain alias.pem`, then run `security add-trusted-cert -d -r trustRoot -p ssl -s "$alias" alias.pem` in an interactive terminal and approve the policy. Verify it with `security verify-cert -c alias.pem -p ssl -s "$alias" -L`. For cleanup, run `security remove-trusted-cert -d alias.pem`, then remove only that fingerprint from each keychain containing it using `security delete-certificate -Z "$leaf_sha256" KEYCHAIN`. Removing the System keychain copy requires administrator privileges.

On Windows 11 the per-user `certutil -user -addstore Root alias.pem` failed with `ERROR_NOT_SUPPORTED` over SSH. Use an interactive desktop for the per-user confirmation. For a machine-wide install from an elevated session, `certutil -addstore Root alias.pem` adds the exact leaf to the Local Machine store. Remove it with `certutil -delstore Root LEAF_SHA1_THUMBPRINT` and confirm that thumbprint is gone. The destination Azure DevOps Server also needs a certificate the browser trusts.

### 4. Point the alias at loopback

Add only the alias lines from `tfsaz route hosts` to the hosts file as described in the README section **Local hostname catch-all**. Check with `tfsaz route verify-hosts`.

### 5. Run the service on port 443

| OS | How |
| --- | --- |
| Windows | `tfsaz route redirect --cert alias.pem --key alias-key.pem`. Pick another approach if IIS or another service already holds port 443. |
| Linux | Check the host’s low-port policy. If binding port 443 needs a capability, grant only that capability with `sudo setcap cap_net_bind_service=+ep "$(command -v tfsaz)"` (lost when the binary is replaced; undo with `sudo setcap -r`), or run on `--port 8443` behind a loopback-only NAT redirect such as `sudo iptables -t nat -A OUTPUT -o lo -p tcp -d 127.0.0.1 --dport 443 -j REDIRECT --to-ports 8443` plus the matching `ip6tables` rule for `::1` (remove them with `-D` instead of `-A`). |
| macOS | Non-root processes cannot bind `127.0.0.1:443` or `[::1]:443`. Run `tfsaz route redirect --cert alias.pem --key alias-key.pem --port 8443` and add a loopback port redirect in a private `pf` anchor, as shown below. |

The example below sends all port 443 connections for the two listed loopback addresses to tfsaz until you remove the rule. Check for other local HTTPS services before installing it. On macOS the rule lasts until removal or reboot.

```sh
printf '%s\n' \
  'rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 443 -> 127.0.0.1 port 8443' \
  'rdr pass on lo0 inet6 proto tcp from any to ::1 port 443 -> ::1 port 8443' \
  | sudo pfctl -a com.apple/tfsaz-redirect -f -
sudo pfctl -E    # prints a token; keep it for removal
```

Remove with `sudo pfctl -a com.apple/tfsaz-redirect -F all` and `sudo pfctl -X TOKEN`. The stock `/etc/pf.conf` evaluates `com.apple/*` anchors, so no edit to that file is needed.

For a permanent service, use the same supervision as described for `tfsaz route proxy` in the README (launchd LaunchAgent, systemd user service or Task Scheduler), with an absolute executable path and an explicit `TFSAZ_CONFIG`.

## Verify

```sh
tfsaz route verify-hosts
curl -sI "https://$alias/Project/_git/Repo/pullrequest/1" | grep -i -e '^HTTP' -e '^location'   # expect 302 and the real URL
curl -sI -X POST "https://$alias/Project/_git/Repo" | head -1                                      # expect 405
openssl s_client -connect 127.0.0.1:443 -servername dev.azure.com </dev/null                       # expect a handshake failure
```

If your `curl` does not use the OS trust store, add `--cacert alias.pem`. Then open a PR link from T3 in the browser and confirm the address bar shows the real collection URL, including any `#fragment`.

## Remove

1. Stop using the alias in browsers and other clients. Once the hosts entry is removed, public DNS can resolve the name to Azure again. Stop the service and its supervisor entry.
2. Remove the alias lines from the hosts file.
3. Remove the port redirect (`pf` anchor, `iptables` rule) or the `setcap` capability.
4. Remove the certificate trust as listed in step 3.
5. Delete `alias.pem` and `alias-key.pem`.

## macOS desktop browser verification

In the installed T3 Nightly desktop app, a disposable Electron profile mapped one exact generated alias to the redirector’s high loopback port and trusted the corresponding leaf certificate for that origin. Opening the raw alias reached the real Azure DevOps Server PR 118 authentication page with the expected query and fragment. After the redirector stopped, a fresh navigation failed with `ERR_CONNECTION_REFUSED`. This test used a process-local hostname mapping.

## macOS web client Browser panel verification

A separate test opened a PR link in the T3 web client's Browser panel backed by the installed Nightly server's sandboxed Chromium. The raw alias redirected to a loopback HTTPS fixture with the expected collection path, encoded query and fragment. The destination received neither an Authorization header nor a Referer. An unsupported API path stayed on the alias, and a new navigation after shutdown failed with `ERR_CONNECTION_REFUSED`.

The test used a scratch Chromium launcher to supply the hostname mapping and certificate-error exceptions scoped to two test public keys. It exercised the server-side navigation path. It did not establish production DNS or certificate trust for that runtime or authenticate to a live collection. Install the production routing on the server that runs Chromium. Opening the same URL in an external browser uses that browser's machine and trust settings instead.

## Machine-wide installation results

- **macOS.** With real IPv4 and IPv6 hosts entries, a temporary `pf` anchor translated loopback port 443 to the high-port service. Both address families returned the correct 302. The exact self-signed leaf was installed in the System keychain with administrator-domain SSL trust limited to the alias. A noninteractive SSH command could import the certificate, but setting its trust required interactive authorization. Merely seeing the certificate in Keychain Access did not establish trust. Apple's `security verify-cert -c alias.pem -p ssl -s "$alias"` verified the policy. Both the installed desktop Electron tab and T3's server-hosted Chromium tab reached the exact live PR authentication page with query and fragment, using normal DNS and browser trust. The desktop tab reported `ERR_CONNECTION_REFUSED` when the service stopped. The bundled `/usr/bin/curl` still reported TLS error 60 on this Mac unless given `--cacert alias.pem`. After removal of the trust policy, the macOS verifier rejected the leaf again. The test removed the certificate copies, hosts entries, temporary anchor and PF enable reference.
- **Ubuntu 26.04.** With real hosts entries, IPv4 and IPv6 NAT rules, and the exact leaf added to the system certificate bundle, curl received the correct 302 over both address families. Snap Chromium followed the redirect after the same leaf was trusted separately in its NSS database. The destination then required its own certificate trust. Stopping the listener produced connection refusal. Removing trust while it was running restored certificate rejection. The test restored the hosts file, NAT rules and trust entries.

- **Windows 11.** Both hosts entries resolved locally, and the service bound IPv4 and IPv6 loopback port 443 directly in an elevated session. With the exact alias leaf in the Local Machine store, Schannel curl, WinHTTP and .NET returned the expected 302. Headless Edge used its normal resolver and trust store, preserved the fragment and reached the native URL. The destination certificate was untrusted on this VM, so Edge stopped there. Stopping the redirector produced connection refusal. Removing trust and restarting restored certificate rejection. The test removed the leaf by thumbprint and verified the hosts file's original bytes, ACL and file ID.

A separate web-client test on **Ubuntu ARM64 and Windows x64** reached the exact live PR authentication page in the server-hosted Browser panel, including the query and fragment. Stopping the redirector produced `ERR_CONNECTION_REFUSED` on both. These isolated tests used a Chromium launcher with a hostname override and certificate-error exceptions limited to the two fixture public keys. Windows ran sandboxed. The Ubuntu run disabled the server browser sandbox for that test.
