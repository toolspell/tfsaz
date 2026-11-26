# Manual browser routing

Use this alternative to [managed browser routing](browser-routing.md#managed-browser-routing) when you intend to manage the listener, trust and hosts entries yourself. The listener behaves as described in [What the redirector does and refuses](browser-routing.md#what-the-redirector-does-and-refuses).

- Start with a registered alias of the form shown in step 1.
- Choose either the managed installation or the manual listener for port 443, not both.
- Record each manual change for removal.
- When the set of names changes, issue a new leaf and update hosts and trust. Restart the listener with the matching certificate and key.

## 1. Find the alias names

```sh
tfsaz route hosts | awk '$1 == "127.0.0.1" && $2 ~ /^tfsaz-[0-9a-f]{24}\.visualstudio\.com$/ {print $2}'
```

## 2. Create an exact leaf for those names

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

## 3. Trust only that leaf

Record its SHA-256 fingerprint before installation. On macOS, use `leaf_sha256=$(openssl x509 -in alias.pem -noout -fingerprint -sha256 | cut -d= -f2 | tr -d :)` for removal. Windows certutil identifies the individual certificate by its SHA-1 thumbprint, which you can read with `certutil -dump alias.pem`.

| OS | Add | Remove |
| --- | --- | --- |
| macOS | `security add-trusted-cert -r trustRoot -p ssl -s "$alias" -k ~/Library/Keychains/login.keychain-db alias.pem` (asks for your password; the trust is limited to SSL for that name) | `security remove-trusted-cert alias.pem` and `security delete-certificate -Z "$leaf_sha256" ~/Library/Keychains/login.keychain-db` |
| Windows | `certutil -user -addstore Root alias.pem` (per-user store; confirm the prompt) | `certutil -user -delstore Root LEAF_SHA1_THUMBPRINT` |
| Ubuntu Chrome/Chromium | `certutil -d "sql:$nssdb" -A -t "P,," -n tfsaz-redirect -i alias.pem` (`P` means trusted peer, not a CA) | `certutil -d "sql:$nssdb" -D -n tfsaz-redirect` |

For the T3 web client's server-hosted Browser panel, configure trust for the account running Chromium. T3 Desktop can also remember a verified certificate in its own browser profile. Compare the displayed fingerprint with the leaf you created.

### Machine-wide trust on macOS

The tested sequence was to add the leaf to the System keychain with `sudo security add-certificates -k /Library/Keychains/System.keychain alias.pem`, then run `security add-trusted-cert -d -r trustRoot -p ssl -s "$alias" alias.pem` in an interactive terminal and approve the policy. Verify it with `security verify-cert -c alias.pem -p ssl -s "$alias" -L`.

For cleanup, run `security remove-trusted-cert -d alias.pem`, then remove only that fingerprint from each keychain containing it using `security delete-certificate -Z "$leaf_sha256" KEYCHAIN`. Removing the System keychain copy requires administrator privileges.

### Trust on Windows 11

The per-user `certutil -user -addstore Root alias.pem` failed with `ERROR_NOT_SUPPORTED` over SSH. Use an interactive desktop for the per-user confirmation.

For a machine-wide install from an elevated session, `certutil -addstore Root alias.pem` adds the exact leaf to the Local Machine store. Remove it with `certutil -delstore Root LEAF_SHA1_THUMBPRINT` and confirm that thumbprint is gone. The destination Azure DevOps Server also needs a certificate the browser trusts.

### NSS database on Ubuntu

Set `nssdb` to the database the target browser uses. On Ubuntu 26.04, snap Chromium 158 used `$HOME/snap/chromium/current/.local/share/pki/nssdb` after its first launch. Other installations can use `$XDG_DATA_HOME/pki/nssdb`, `$HOME/.local/share/pki/nssdb` or the older `$HOME/.pki/nssdb`. The `libnss3-tools` package supplies `certutil`. An Ubuntu system trust entry made curl accept the alias, while this Chromium build also required the NSS entry.

## 4. Point the alias at loopback

For each name on your certificate, copy its IPv4 and IPv6 lines from `tfsaz route hosts` to the system hosts file. That command can also print custom profile hostnames outside the redirector's accepted name format. `tfsaz route verify-hosts` checks every hostname from its list. The curl checks in [Verify the manual setup](#verify-the-manual-setup) exercise the particular alias you installed.

## 5. Run the service on port 443

| OS | How |
| --- | --- |
| macOS | The direct listener on the test Mac required privilege on port 443. Managed setup uses launchd socket activation. For a manual installation, use a free high port. Run `tfsaz route redirect --cert alias.pem --key alias-key.pem --port 8443` and add a loopback port redirect in a private `pf` anchor, as shown below. |
| Windows | `tfsaz route redirect --cert alias.pem --key alias-key.pem`. Pick another approach if IIS or another service already holds port 443. |
| Linux | If the host permits unprivileged binding, run the listener directly on port 443. Otherwise run on a free high port such as `--port 8443` and forward loopback traffic. The manual test used `sudo iptables -t nat -A OUTPUT -o lo -p tcp -d 127.0.0.1 --dport 443 -j REDIRECT --to-ports 8443` plus the matching `ip6tables` rule for `::1` (remove them with `-D` instead of `-A`). |

The macOS example below sends all port 443 connections for the two listed loopback addresses to TFSaz until you remove the rule. Check for other local HTTPS services before installing it. On macOS the rule lasts until removal or reboot.

```sh
printf '%s\n' \
  'rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 443 -> 127.0.0.1 port 8443' \
  'rdr pass on lo0 inet6 proto tcp from any to ::1 port 443 -> ::1 port 8443' \
  | sudo pfctl -a com.apple/tfsaz-redirect -f -
sudo pfctl -E    # prints a token; keep it for removal
```

Remove with `sudo pfctl -a com.apple/tfsaz-redirect -F all` and `sudo pfctl -X TOKEN`. The stock `/etc/pf.conf` evaluates `com.apple/*` anchors, so no edit to that file is needed.

For a supervised installation on any of the three platforms, use [managed browser routing](browser-routing.md#managed-browser-routing). If you supervise a manual listener yourself, keep the executable, config, certificate and key at stable paths and run the service under the intended user account.

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
