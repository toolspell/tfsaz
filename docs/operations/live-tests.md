# Live tests

Live tests check what TFSaz changes on a client machine: routing, trust, keyring, services, link handlers, browsers and T3. They run in disposable VMs that reach a mock of Azure DevOps Server instead of the shared testbed. The files are in [`live-tests/`](../../live-tests/), the tool in [`tools/live`](../../tools/live/). Ubuntu, macOS and Windows guests work.

## Set up a Mac

The host must be an Apple Silicon Mac with Homebrew, Go and Git. From the repository root:

```sh
go run ./tools/live setup ubuntu
go run ./tools/live setup macos
go run ./tools/live setup windows
```

`setup OS` installs what is missing, starts the test bed, builds the OS's base VM if it does not exist, and ends with `verify`. It skips steps that are done, so it is safe to rerun. The first Ubuntu run takes about 10 minutes plus the 3 GB ISO download; the first macOS run takes about 25 minutes to pull the 29 GB image and 3 minutes for the rest; the first Windows run takes about 15 minutes once Microsoft's ISO is in the cache, plus the 880 MB virtio-win download.

| Step | Done by | Human part |
| --- | --- | --- |
| Tart (with softnet) and Packer | `doctor -install`, through Homebrew | none |
| Docker | `doctor -install` installs OrbStack | start OrbStack once and accept its setup |
| QEMU, for Windows | `doctor -install`, through Homebrew | none |
| Windows 11 ARM64 ISO | not the tool: Microsoft's download links expire | download it once from [Microsoft](https://www.microsoft.com/software-download/windows11arm64) (English, United States) into `~/.tfsaz-live/cache/iso/`; `setup windows` names the file it expects |
| Homebrew, Go, Git | not the tool | install them first |
| Shorter DHCP leases for VMs | `doctor` only advises it | optional, needs `sudo` ([why](#host-requirements)) |

## Commands

```sh
go run ./tools/live setup OS          # everything above, then verify; OS is ubuntu, macos or windows
go run ./tools/live verify OS         # boot a throwaway clone and check the bed
go run ./tools/live vm up OS          # clone, boot and connect a VM; prints its details as JSON
go run ./tools/live vm ssh NAME -- uname -a
go run ./tools/live vm list -json
go run ./tools/live vm stop NAME      # shut it down, keep it for later
go run ./tools/live vm start NAME     # boot it again; prints its details as JSON
go run ./tools/live vm down NAME
go run ./tools/live clean             # delete every clone
go run ./tools/live status
```

`go run ./tools/live help` lists all commands. None prompts unless it runs in a terminal; `setup` and `doctor -install` install without asking. `vm ssh` passes on the remote exit code, but `go run` turns any nonzero code into 1 and prints `exit status N`. For the exact code, build the tool once with `go build -o bin/live ./tools/live` and run `bin/live`.

`verify` checks from a new clone that `devops.mock.test` resolves to the mock, that the mock's REST API answers through the router, that internet traffic goes through the tunnel and works, that the router reaches the VM, and on macOS and Windows that the test user is logged in to the GUI. It prints one line per check, deletes the clone and fails if any check failed.

## For agents

- Target the mock. Use the shared Azure DevOps Server testbed only when the maintainer asks ([AGENTS.md](../../AGENTS.md#ways-to-hurt-yourself)).
- Run `base build` and `setup` in the background and watch their output. Do not wait past Packer's SSH timeout (25 minutes, 60 for Windows); [Troubleshooting](#troubleshooting) has the known causes.
- When the Windows ISO is missing, ask the maintainer to download it; do not fetch it from another source.
- When a base command refuses because clones exist, remove the clones; do not work around it. Leave the base VM and `~/.tfsaz-live/cache` in place between tasks.
- Never commit anything from `live-tests/.state` or copy keys into the repository.
- Ask the maintainer before the `sudo` step `doctor` advises.
- Keep a clone while its case or PR may still need it, for example for review fixes or a rerun. Stop it with `vm stop NAME` while you do not use it and start it again with `vm start NAME`. Delete it with `vm down NAME` only once you are sure nothing needs it any more.
- Nothing runs when nothing needs it: before you hand back, stop the clones you keep, delete the others, and run `services down`. The base VM stays stopped.
- Record dated live results in [`docs/verification/`](../verification/).
- macOS runs at most two macOS VMs at a time, base builds included; `vm up` refuses a third. Stop or delete one first.
- Prefer the tools already in use (Tart, Packer, `curl`, `wg`, cloud-init, Homebrew, CoreDNS) over new code.

## Layout

| Path | Contents |
| --- | --- |
| `live-tests/compose.yaml` | The test bed: [mock](../../live-tests/devops-server-mock/README.md), DNS sinkhole, VM router |
| `live-tests/devops-server-mock/` | The Azure DevOps Server mock |
| `live-tests/dns/Corefile` | The DNS sinkhole |
| `live-tests/router/` | The WireGuard router that connects VMs |
| `live-tests/vms/ubuntu/` | The Ubuntu base VM: ISO pin, autoinstall config, Packer template, base preparation |
| `live-tests/vms/macos/` | The macOS base VM: image pin, Packer template, provisioning, base preparation |
| `live-tests/vms/windows/` | The Windows base VM: ISO pins, answer file, first-logon script, Packer template, provisioning, base preparation |
| `tools/live/` | The tool |

A new guest OS gets a `live-tests/vms/<os>/` directory, an entry in the tool's OS list in `tools/live/env.go`, its guest commands in `tools/live/guest.go`, and its build inputs in `buildInputs` in `tools/live/base.go`. A new hypervisor implements the `provider` interface in `tools/live/provider.go`, as Tart (`tart.go`) and QEMU (`qemu.go`) do.

## State outside the repository

The tool keeps everything large or secret in `~/.tfsaz-live` (or `$TFSAZ_LIVE_HOME`) and links it as `live-tests/.state`, which Git ignores.

| Path | Contents |
| --- | --- |
| `tart/` | Tart's home for these VMs only (`TART_HOME`), so they never mix with other Tart VMs; its `cache/` holds the pulled macOS image |
| `qemu/` | QEMU's VMs, one directory each: `disk.qcow2` (a clone's is an overlay on the base's), UEFI variables, its last SSH port, and while it runs its PID and QMP socket |
| `cache/iso/` | Installer ISOs, checked against the SHA-256 in `vms/<os>/iso.json` (and `virtio.json` for Windows) |
| `packer/` | Packer plugins and cache |
| `keys/<os>-base/` | The key of the last base build; for Windows also the password of `tester` |
| `runs/<name>/` | Per clone: its own SSH key, known_hosts, Tart or QEMU log, `vm.json` |
| `router/` | The router's WireGuard key and one file per connected VM |

Only `base build` downloads, and only what the cache lacks: an ISO matching `iso.json` (`curl` resumes an interrupted download), or the macOS image pinned in `image.json` (Tart pulls it once). The Windows ISO is the exception: the tool only checks it. The Windows build also downloads the virtio-win ISO once (about 880 MB) and, inside the VM, OpenSSH Server and WireGuard. Deleting the directory removes everything; `setup` recreates it.

## Base VMs

### Ubuntu

`base build ubuntu` checks the ISO, writes the autoinstall config with a new build key to a seed ISO, and runs Packer with the Tart plugin. The installer runs unattended; Packer answers its one autoinstall prompt. The last step, `prepare-base.sh`, removes what must differ per clone (SSH host keys, machine ID, interface-specific network config) and turns off Ubuntu's automatic updates, so clones do not run `apt` during tests. The build goes into `tfsaz-ubuntu-build` and replaces `tfsaz-ubuntu-base` only when it succeeded. It takes about 8 minutes and 6 GB.

The base has one user, `tester`, with passwordless sudo, no password and no other login than the build key.

### macOS

`base build macos` starts from Cirrus Labs' vanilla macOS 27 image, pinned by digest in `image.json`: macOS after Setup Assistant, with the user `admin` (password `admin`) logged in automatically, Remote Login on, passwordless sudo and Gatekeeper off. Tart pulls it once (about 29 GB). `provision.sh` makes the build key the only SSH login (the image's public password stops working over SSH), installs Homebrew with the Command Line Tools and `wireguard-tools`, adds the launchd daemon `com.tfsaz.live.wg0` that starts the tunnel on boot, and turns off automatic updates. `prepare-base.sh` deletes the SSH host keys, which sshd recreates per clone. With the image in Tart's cache, the build takes about 3 minutes and `base update macos` about 2.5 minutes.

The guest needs a host with at least its macOS version, and macOS runs at most two macOS VMs at a time; the tool checks both. A newer macOS comes with a new pin in `image.json` and `base build macos -force`.

### Windows

`base build windows` checks Microsoft's Windows 11 ARM64 ISO and the virtio-win ISO, writes `Autounattend.xml` with a new password for `tester` and `setup.ps1` with a new build key to a seed ISO, and runs Packer with the QEMU plugin. QEMU runs the VM with Apple's Hypervisor.framework and UEFI firmware from Homebrew's QEMU. The `virt` machine has no IDE or SATA, so the three ISOs are USB drives, and the disk is NVMe, for which Windows has an inbox driver. Setup runs unattended: it skips the TPM, Secure Boot and online account checks, installs Windows 11 Pro with the generic install key (not activated), and logs `tester` in automatically. At first logon, `setup.ps1` installs the virtio network driver (NetKVM) from the virtio-win ISO and OpenSSH Server with the build key as the only login. `provision.ps1` installs PowerShell 7 and WireGuard for Windows (each pinned by hash and checked for its publisher's signature) and turns off automatic updates and sleep. `prepare-base.ps1` deletes the SSH host keys, which sshd recreates per clone, makes PowerShell 7 the SSH shell and shuts the VM down. The build takes about 13 minutes and 16 GB.

Windows has no update in place: `base update windows` refuses. A newer Windows comes with a new ISO pin in `iso.json` and `base build windows -force`.

### Commands

| Command | Effect |
| --- | --- |
| `base build OS` | First build; `-force` rebuilds from scratch |
| `base update OS` | Boots the base, upgrades its packages (apt on Ubuntu, Homebrew on macOS), prepares it again; not on Windows |
| `base rm OS` | Deletes the base and its build key |

All three refuse while clones of the base exist.

## Clones

`vm up OS` clones the base (an APFS copy, or a qcow2 overlay for Windows, so it takes little space), boots it headless, replaces the build key with a new key for this clone, sets its hostname and connects it to the router, in under 10 seconds for Ubuntu, about 30 for macOS and about a minute for Windows. A macOS clone boots into the logged-in GUI session of `admin`, a Windows clone into the one of `tester`; a Windows clone restarts once for its computer name. It prints `vm.json`: name, NAT address (for Windows `127.0.0.1` and a port), address in the test bed, SSH key. Commands over `vm ssh` run in sh on Ubuntu and macOS and in PowerShell 7 (`pwsh`) on Windows. TFSaz must not rely on PowerShell 7, since users may have only Windows PowerShell 5.1 and cmd: run a step whose shell matters with `powershell` or `cmd /c`. `vm stop NAME` shuts a clone down from inside and keeps it with its key and router peer; when it cannot reach the clone it leaves it running, and `-force` stops it without a shutdown, which can lose unwritten changes. `vm start NAME` boots it again, prints the updated `vm.json` (the NAT address can change) and checks that it reaches the mock. `vm down NAME` and `clean` stop and delete clones and remove their router peers. Stopped clones count as clones: base commands refuse while any exist.

## Network

The VMs use Tart's NAT for SSH from the host (Windows: QEMU's user-mode NAT, with SSH forwarded from a loopback port), and a WireGuard tunnel to the router for everything else:

| Address | What |
| --- | --- |
| `172.30.53.2` | Router: WireGuard on UDP 51820 (published on the host), resolver for the VMs |
| `172.30.53.10` | Mock, `devops.mock.test`, with HTTPS on 443 and 8443, HTTP on 8080, and SSH on 2222. These ports are also published on the host's loopback. |
| `172.30.53.53` | DNS sinkhole |
| `172.30.53.129` and up | VMs, one address each |
| `127.0.0.1:19090` | Mock journal on the host. The router blocks direct access from VMs. |

On Windows the tunnel routes `0.0.0.0/1` and `128.0.0.0/1` instead of `0.0.0.0/0`, which would turn on WireGuard for Windows' kill switch and cut off SSH from the host. Without the kill switch Windows would also ask QEMU's DNS server, so the tool sets the router's resolver on the network card too. The router answers ARP for the VM addresses, so containers reach a VM like any other host in the subnet. Traffic from a VM to anywhere else leaves through Docker's NAT, so VMs have internet access for now. The router's resolver sends `mock.test` names to the sinkhole and everything else to Docker's resolver. Its key and peer list survive a restart; VMs reconnect within seconds.

For HTTPS tests, `go run ./tools/live mock ca` prints the mock CA certificate's path on the host. Clients need to trust this CA explicitly. Copy the certificate into the clone and pass its guest path to TFSaz's `--ca-file` or to `curl --cacert`. Use the hostname `devops.mock.test` for TLS verification.

## Request journal

```sh
go run ./tools/live mock requests [-vm NAME] [-json]
go run ./tools/live mock reset
```

The mock keeps the last 5,000 completed HTTP and HTTPS requests in memory. Each entry includes the time, client IP, method, scheme, host, path, query, status, user agent and authentication scheme. It omits authorization values and masks known credentials and common credential query parameters. The router only masquerades traffic destined outside the bed, so journal entries for a VM use the `bedAddress` recorded in its `vm.json`.

`mock requests` prints one line per request. Use `-vm NAME` to read only that clone's requests and `-json` to get a JSON array. The host admin API exposes `GET /requests` with optional `?ip=ADDRESS` and `DELETE /requests` to clear the journal. Resetting the journal leaves the mock's repositories and work items in place.

## DNS sinkhole

The `dns` service (CoreDNS, `live-tests/dns/Corefile`) logs every query with the client's address.

| Name | Answer | Meaning |
| --- | --- | --- |
| `devops.mock.test` | `172.30.53.10` | The mock |
| `*.visualstudio.com`, `dev.azure.com` | NXDOMAIN | TFSaz's hosts-file entries must answer these on the client. A query that reaches the sinkhole is a leak. |
| Anything else | NXDOMAIN | Offline test runs |

```sh
docker compose -f live-tests/compose.yaml logs dns | grep -E 'visualstudio\.com|dev\.azure\.com'
```

The other answers are NXDOMAIN rather than REFUSED because Docker's resolver retries a refusal for 10 seconds before it gives up. While VMs have internet access, only `mock.test` names reach the sinkhole; offline runs will point the VMs at it for every name.

Test browsers keep their default settings, as users have them. Offline, DNS-over-HTTPS (secure DNS) providers are unreachable, so browsers fall back to the system resolver: the hosts file first, then the sinkhole. A later browser test case runs once with DNS over HTTPS forced on, to find out whether an alias host leaks past TFSaz's hosts lines.

## Host requirements

`doctor` checks Tart, Packer, QEMU, Docker with Compose, `curl`, `hdiutil`, `ssh` and `ssh-keygen`. It installs missing ones with Homebrew and tells apart a tool that is installed but not working, such as a stopped Docker engine. Homebrew refuses formulas from untrusted taps, so the tool trusts exactly `openai/tools/tart` and its dependency `openai/tools/softnet` before installing Tart.

It also advises shortening macOS's DHCP leases for VMs from a day to ten minutes: every clone gets a new MAC address, and the pool of about 250 addresses runs out after that many clones in a day.

## Troubleshooting

- A base build prints a VNC address and password; open it to watch the installer or, on macOS, the desktop.
- The Ubuntu system must ask DHCP with its MAC address (`dhcp-identifier: mac` in the autoinstall and clone network config); otherwise Tart finds no IP and Packer waits until its 25-minute SSH timeout.
- A clone's Tart output is in `~/.tfsaz-live/runs/NAME/tart.log`. `vm up` deletes a clone that fails to come up.
- A Windows build prints a VNC address without a password; a Windows clone shows nothing on the host. To capture either screen, send QMP `screendump` to `~/.tfsaz-live/build/windows/qmp.sock` or `~/.tfsaz-live/qemu/NAME/qmp.sock`, for example `{ echo '{"execute":"qmp_capabilities"}'; sleep 1; echo '{"execute":"screendump","arguments":{"filename":"/tmp/screen.png","format":"png"}}'; sleep 2; } | nc -U SOCKET`. A Windows build logs its first logon to `C:\Windows\Temp\tfsaz-setup.log`; a clone's QEMU output is in `~/.tfsaz-live/runs/NAME/qemu.log`.
- A macOS clone's tunnel logs to `/var/log/tfsaz-live-wg0.log` in the VM; `sudo launchctl kickstart -k system/com.tfsaz.live.wg0` restarts it.

## Remove everything

| Change on the host | Undo |
| --- | --- |
| Clones, base VMs, ISOs, keys, router state in `~/.tfsaz-live` | `go run ./tools/live clean`, `go run ./tools/live services down`, then `rm -rf ~/.tfsaz-live live-tests/.state` |
| The mock's CA and SSH files | After stopping services, run `rm -f live-tests/devops-server-mock/keys/{ca.pem,client_ed25519,client_ed25519.pub,known_hosts}` |
| Docker images and network `tfsaz-live_bed` | `docker compose -f live-tests/compose.yaml down --rmi local` |
| Tart, softnet, Packer and QEMU from Homebrew | `brew uninstall tart softnet packer qemu` |
| Trusted formulas `openai/tools/tart`, `openai/tools/softnet` and `hashicorp/tap/packer` (Homebrew trusts the last one itself when installing it) | `brew untrust --formula openai/tools/tart openai/tools/softnet hashicorp/tap/packer` |
| Taps `openai/tools` and `hashicorp/tap` | `brew untap openai/tools hashicorp/tap` |
| Shorter DHCP leases, if you applied the advice | `sudo defaults delete /Library/Preferences/SystemConfiguration/com.apple.InternetSharing.default.plist bootpd` |
