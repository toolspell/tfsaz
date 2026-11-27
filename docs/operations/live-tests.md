# Live tests

Live tests check what TFSaz changes on a client machine: routing, trust, keyring, services, link handlers, browsers and T3. They run in disposable VMs that reach a mock of Azure DevOps Server instead of the shared testbed. The files are in [`live-tests/`](../../live-tests/), the tool in [`tools/live`](../../tools/live/). Ubuntu works; Windows (UTM) and macOS (Tart) are planned.

## Set up a Mac

The host must be an Apple Silicon Mac with Homebrew, Go and Git. From the repository root:

```sh
go run ./tools/live setup ubuntu
```

`setup` installs what is missing, starts the test bed, builds the Ubuntu base VM if it does not exist, and ends with `verify`. It skips steps that are done, so it is safe to rerun. The first run takes about 10 minutes plus the 3 GB ISO download.

| Step | Done by | Human part |
| --- | --- | --- |
| Tart (with softnet) and Packer | `doctor -install`, through Homebrew | none |
| Docker | `doctor -install` installs OrbStack | start OrbStack once and accept its setup |
| Homebrew, Go, Git | not the tool | install them first |
| Shorter DHCP leases for VMs | `doctor` only advises it | optional, needs `sudo` ([why](#host-requirements)) |

## Commands

```sh
go run ./tools/live setup ubuntu      # everything above, then verify
go run ./tools/live verify ubuntu     # boot a throwaway clone and check the bed
go run ./tools/live vm up ubuntu      # clone, boot and connect a VM; prints its details as JSON
go run ./tools/live vm ssh NAME -- uname -a
go run ./tools/live vm list -json
go run ./tools/live vm down NAME
go run ./tools/live clean             # delete every clone
go run ./tools/live status
```

`go run ./tools/live help` lists all commands. None prompts unless it runs in a terminal; `setup` and `doctor -install` install without asking. `vm ssh` passes on the remote exit code, but `go run` turns any nonzero code into 1 and prints `exit status N`. For the exact code, build the tool once with `go build -o bin/live ./tools/live` and run `bin/live`.

`verify` checks from a new clone that `devops.mock.test` resolves to the mock, that the mock's REST API answers through the router, that internet traffic goes through the tunnel and works, and that the router reaches the VM. It prints one line per check, deletes the clone and fails if any check failed.

## For agents

- Target the mock. Use the shared Azure DevOps Server testbed only when the maintainer asks ([AGENTS.md](../../AGENTS.md#ways-to-hurt-yourself)).
- Run `base build` and `setup` in the background and watch their output. Do not wait past Packer's 25-minute SSH timeout; [Troubleshooting](#troubleshooting) has the known causes.
- When a base command refuses because clones exist, remove the clones; do not work around it. Leave the base VM and `~/.tfsaz-live/cache` in place between tasks.
- Never commit anything from `live-tests/.state` or copy keys into the repository.
- Ask the maintainer before the `sudo` step `doctor` advises.
- Delete a clone with `vm down NAME` as soon as its case is done and you will not need it again, not only at the end. Nothing keeps running when nothing needs it: run `clean` and `services down` before you hand back; the base VM stays stopped.
- Record dated live results in [`docs/verification/`](../verification/).
- Prefer the tools already in use (Tart, Packer, `curl`, `wg`, cloud-init, CoreDNS) over new code.

## Layout

| Path | Contents |
| --- | --- |
| `live-tests/compose.yaml` | The test bed: [mock](../../live-tests/devops-server-mock/README.md), DNS sinkhole, VM router |
| `live-tests/devops-server-mock/` | The Azure DevOps Server mock |
| `live-tests/dns/Corefile` | The DNS sinkhole |
| `live-tests/router/` | The WireGuard router that connects VMs |
| `live-tests/vms/ubuntu/` | The Ubuntu base VM: ISO pin, autoinstall config, Packer template, base preparation |
| `tools/live/` | The tool |

A new guest OS gets a `live-tests/vms/<os>/` directory and an entry in the tool's OS list in `tools/live/env.go`; until then the tool refuses it with "not set up yet".

## State outside the repository

The tool keeps everything large or secret in `~/.tfsaz-live` (or `$TFSAZ_LIVE_HOME`) and links it as `live-tests/.state`, which Git ignores.

| Path | Contents |
| --- | --- |
| `tart/` | Tart's home for these VMs only (`TART_HOME`), so they never mix with other Tart VMs |
| `cache/iso/` | Downloaded installer ISOs, checked against the SHA-256 in `vms/<os>/iso.json` |
| `packer/` | Packer plugins and cache |
| `keys/<os>-base/` | The key of the last base build |
| `runs/<name>/` | Per clone: its own SSH key, known_hosts, Tart log, `vm.json` |
| `router/` | The router's WireGuard key and one file per connected VM |

Only `base build` downloads an ISO, and only when the cache has none matching `iso.json`; `curl` resumes an interrupted download. Deleting the directory removes everything; `setup` recreates it.

## Base VMs

`base build ubuntu` checks the ISO, writes the autoinstall config with a new build key to a seed ISO, and runs Packer with the Tart plugin. The installer runs unattended; Packer answers its one autoinstall prompt. The last step, `prepare-base.sh`, removes what must differ per clone (SSH host keys, machine ID, interface-specific network config) and turns off Ubuntu's automatic updates, so clones do not run `apt` during tests. The build goes into `tfsaz-ubuntu-build` and replaces `tfsaz-ubuntu-base` only when it succeeded. It takes about 8 minutes and 6 GB.

| Command | Effect |
| --- | --- |
| `base build ubuntu` | First build; `-force` rebuilds from scratch |
| `base update ubuntu` | Boots the base, upgrades its packages, prepares it again |
| `base rm ubuntu` | Deletes the base and its build key |

All three refuse while clones of the base exist. The base has one user, `tester`, with passwordless sudo, no password and no other login than the build key.

## Clones

`vm up ubuntu` clones the base (an APFS copy, so it takes little space), boots it headless, replaces the build key with a new key for this clone, sets its hostname and connects it to the router, in under 10 seconds. It prints `vm.json`: name, NAT address, address in the test bed, SSH key. `vm down NAME` and `clean` stop and delete clones and remove their router peers.

## Network

The VMs use Tart's NAT for SSH from the host, and a WireGuard tunnel to the router for everything else:

| Address | What |
| --- | --- |
| `172.30.53.2` | Router: WireGuard on UDP 51820 (published on the host), resolver for the VMs |
| `172.30.53.10` | Mock, `devops.mock.test`; ports 8080 and 2222 also published on the host's loopback |
| `172.30.53.53` | DNS sinkhole |
| `172.30.53.129` and up | VMs, one address each |

The router answers ARP for the VM addresses, so containers reach a VM like any other host in the subnet. Traffic from a VM to anywhere else leaves through Docker's NAT, so VMs have internet access for now. The router's resolver sends `mock.test` names to the sinkhole and everything else to Docker's resolver. Its key and peer list survive a restart; VMs reconnect within seconds.

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

`doctor` checks Tart, Packer, Docker with Compose, `curl`, `hdiutil`, `ssh` and `ssh-keygen`. It installs missing ones with Homebrew and tells apart a tool that is installed but not working, such as a stopped Docker engine. Homebrew refuses formulas from untrusted taps, so the tool trusts exactly `openai/tools/tart` and its dependency `openai/tools/softnet` before installing Tart.

It also advises shortening macOS's DHCP leases for VMs from a day to ten minutes: every clone gets a new MAC address, and the pool of about 250 addresses runs out after that many clones in a day.

## Troubleshooting

- A base build prints a VNC address and password; open it to watch the installer.
- The installed system must ask DHCP with its MAC address (`dhcp-identifier: mac` in the autoinstall and clone network config); otherwise Tart finds no IP and Packer waits until its 25-minute SSH timeout.
- A clone's Tart output is in `~/.tfsaz-live/runs/NAME/tart.log`. `vm up` deletes a clone that fails to come up.

## Remove everything

| Change on the host | Undo |
| --- | --- |
| Clones, base VMs, ISOs, keys, router state in `~/.tfsaz-live` | `go run ./tools/live clean`, `go run ./tools/live services down`, then `rm -rf ~/.tfsaz-live live-tests/.state` |
| Docker images and network `tfsaz-live_bed` | `docker compose -f live-tests/compose.yaml down --rmi local` |
| Tart, softnet and Packer from Homebrew | `brew uninstall tart softnet packer` |
| Trusted formulas `openai/tools/tart`, `openai/tools/softnet` and `hashicorp/tap/packer` (Homebrew trusts the last one itself when installing it) | `brew untrust --formula openai/tools/tart openai/tools/softnet hashicorp/tap/packer` |
| Taps `openai/tools` and `hashicorp/tap` | `brew untap openai/tools hashicorp/tap` |
| Shorter DHCP leases, if you applied the advice | `sudo defaults delete /Library/Preferences/SystemConfiguration/com.apple.InternetSharing.default.plist bootpd` |
