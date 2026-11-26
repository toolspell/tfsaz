# Route synthetic HTTPS URLs

A managed checkout gives T3 an Azure-style URL with a synthetic hostname. Choose how each client reaches the real collection.

| Caller | How to use it |
| --- | --- |
| Browser, including T3’s Browser panel | Run `tfsaz route setup` and `tfsaz route verify` on the machine where the browser runs. See [Browser routing](browser-routing.md). |
| Desktop handoffs where you cannot change the hosts file | The per-user [desktop link handler](desktop-link-handler.md) can open links using the selected system browser. |
| Human or script | `tfsaz route explain URL` shows the action, collection profile and destination. For a one-off link, `tfsaz route resolve URL` prints the real URL for a shell client. Check its exit status before using the output. Supply the client's normal authentication for that destination. |
| Optional in-browser routing for Chromium | Run `tfsaz route browser-export NEW_DIRECTORY`. In the intended Chromium profile, enable developer mode and load the unpacked directory. Supported repository, PR, commit, work-item and build links navigate to the real origin, where browser sign-in applies. The extension blocks other requests to the registered alias spellings it recognizes. Use `tfsaz route browser-verify DIRECTORY` to compare the files with the current configuration. After a profile change or repo restore, remove the old extension and load a fresh export. Removing the extension disables browser routing for that profile. |
| Git or stock T3 server | Use `tfsaz route run -- git clone URL`, `tfsaz route run -- t3 serve`, or launch an entire shell with `tfsaz route run -- zsh`. Processes in that tree inherit a private Git HTTPS helper. The original Git helper contacts the real collection and uses Git's TLS and credential settings. Git retains the synthetic origin for T3 detection. A process that discards the inherited Git environment can bypass the helper. Closing the launched process ends the override. A separately started service needs its own launch configuration. |
| Unattended job | Run the job under `tfsaz route run --` with Git credentials configured for the real collection under the job's OS account. The launcher disables Git terminal and Git Credential Manager interaction when stdin is not a terminal. Choose a non-interactive credential helper for the job. Git does not automatically receive the CLI's Keychain PAT. |

## Optional hosts-file, connection refusal and proxy settings

Use these manual settings to keep registered aliases from reaching their public names.

### Local hostname catch-all

`tfsaz route hosts` prints IPv4 and IPv6 loopback entries for the registered dedicated aliases. Review the output, then add those lines as a distinct block in the system hosts file if you want machine-wide resolution. On macOS and Ubuntu this is `/etc/hosts`. On Windows it is `%SystemRoot%\System32\drivers\etc\hosts`. Editing the file requires the appropriate OS privileges. Remove that block to undo it. Regenerate it when the aliases change. `tfsaz route verify-hosts` checks the current system resolver for each name.

`tfsaz route sink` explicitly closes TCP connections arriving on port 443 on both loopback addresses, without reading or recording the payload. An unused loopback port already refuses connections when the listener is stopped. The listener needs both addresses and an available port. Use `--port N` for a different listener port.

Hostname mapping covers ordinary local DNS lookups for all ports. A client using a remote proxy, private DNS resolver or direct IP can bypass it. Browser profiles on other machines, VMs and containers need their own configuration. None of these checks certifies machine-wide egress enforcement.

### PAC/SOCKS catch-all

`tfsaz route proxy --port 1080` serves `http://127.0.0.1:1080/proxy.pac` and refuses SOCKS5 at that port. For registered hosts, the PAC lists SOCKS5 first and an HTTP proxy on the same loopback port second. The HTTP entry supports clients such as WinHTTP that ignore SOCKS5. The service refuses HTTP proxy requests and HTTPS CONNECT tunnels locally. Unregistered hosts get DIRECT.

| OS | Where to enter the PAC URL |
| --- | --- |
| macOS | The active network service’s **Details → Proxies → Automatic proxy configuration** |
| Windows 11 | **Settings → Network & internet → Proxy → Use setup script** |
| Ubuntu GNOME | **Network → Network Proxy → Automatic** |

Record and restore the prior proxy configuration when enabling or disabling it. A standalone PAC replaces the previous PAC for that network service. Where an organization requires an existing proxy, integrate the exact-host cases into its existing policy. `tfsaz route pac` prints the rules for inspection or independent hosting. Restart the proxy or redistribute the PAC after the alias list changes. [Apple proxy settings](https://support.apple.com/guide/mac-help/mchlp2591/mac) · [Windows proxy settings](https://support.microsoft.com/en-us/windows/experience/connectivity-networking/use-a-proxy-server-in-windows) · [GNOME proxy settings](https://help.gnome.org/gnome-help/net-proxy.html)

An OS proxy setting applies to clients that consult it. For example, curl can explicitly select SOCKS with remote name resolution using `--proxy socks5h://127.0.0.1:1080`. The SOCKS5 refusal happens during the handshake. An HTTP proxy client supplies its destination in the request; the service sends a refusal without forwarding or logging it. In the macOS Chromium test, a cold start with an unavailable PAC URL attempted a direct connection. Use the hostname mapping alongside PAC when this failure mode matters. A SOCKS tunnel to the on-prem server alone would still leave the client checking TLS for the synthetic hostname and sending the cloud-form path. [Chromium proxy behavior](https://chromium.googlesource.com/chromium/src/+/HEAD/net/docs/proxy.md)

### Shared `dev.azure.com` hostname

The hostname and PAC commands omit the shared `dev.azure.com` hostname by default. Use `--include-dev-azure` only on a machine or network service where catching **every** organization on that hostname is intended. DNS and SOCKS cannot select an organization by HTTPS path. The Chromium rules can match the configured organization path before TLS. The extension and Git helper translate supported traffic without TLS interception. SSH still needs its separate repository-path translation. Raw REST clients can use `az rest` with an explicit collection.

## Node clients and TLS

**Verify a standalone Node client.** Run `tfsaz route verify-node` under the account and environment that launch the client. It runs the `node` on that PATH, checks the certificate chain and hostname for every configured real collection, and exits nonzero if any handshake fails. It sends no HTTP request or PAT. The profile’s `caFile` configures the Go CLI. To trust the same PEM in Node, set `NODE_EXTRA_CA_CERTS` to its absolute path in the client’s launch environment. The check applies to that Node runtime. An installed desktop app can bundle a different runtime, and GUI launches and background jobs can inherit different settings from a terminal. Ordinary T3 Azure CLI calls through tfsaz need neither this check nor a separate Node installation.

**TLS for Node processes.** The loopback redirect uses TLS for the alias first. Following its redirect opens a second TLS connection to the collection. The Git helper connects directly to the collection. A DNS override or a SOCKS tunnel alone leaves the synthetic hostname as the TLS identity. A server certificate must match the hostname in the URL (or the IP address for an IP-literal URL) and chain to a trusted certificate. On the test Mac, Node 26.13.1 rejected the LAN listener with `DEPTH_ZERO_SELF_SIGNED_CERT` using its bundled roots. Launching Node with `NODE_USE_SYSTEM_CA=1` completed TLS and received the server’s expected unauthenticated HTTP 401. On supported Node versions this setting adds the system roots. Alternatively, set `NODE_EXTRA_CA_CERTS=/absolute/path/to/approved-ca.pem` before launching the Node app. Apply this to the process making the HTTPS request. T3’s Azure CLI calls launch the Go binary, which has its own system and per-profile CA support. [Node TLS environment options](https://nodejs.org/api/cli.html#node_use_system_ca1) · [TLS service identity](https://www.rfc-editor.org/rfc/rfc9525.html)

## Long-running processes

The hosts-file block and installed browser rules persist while tfsaz is stopped. Git starts its transport helper per operation. To run the local PAC service continuously, supervise `tfsaz route proxy`:

| OS | Supervisor |
| --- | --- |
| macOS | A per-user launchd LaunchAgent with an absolute executable path, an explicit `TFSAZ_CONFIG`, and `KeepAlive`. |
| Ubuntu | A systemd user service running the same command with `Restart=on-failure`. |
| Windows | A Task Scheduler logon trigger for the current user with the restart-on-failure setting, a permanent executable path and a user configuration accessible to the task. |

Restarting the process restores PAC availability. The hostname mapping provides a separate fallback during the restart for clients resolving locally. A service launched before user login needs its own account, environment and supervision. [Apple launchd jobs](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html) · [systemd restart settings](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html) · [Windows Task Scheduler restarts](https://learn.microsoft.com/en-us/windows/win32/taskschd/taskschedulerschema-restartonfailure-settingstype-element)
