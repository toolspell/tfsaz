# Live tests

Live tests check what TFSaz changes on a client machine: routing, trust, keyring, services, link handlers, browsers and T3. They run in disposable VMs against a mock of Azure DevOps Server in a local Docker test bed. The files are in [`live-tests/`](../../live-tests/), the tool in [`tools/live`](../../tools/live/). Ubuntu, macOS and Windows guests work. For the big picture, start with the [overview](live-tests-overview.md).

The mock does not replace the shared Azure DevOps Server testbed for checks against a real server: [#33](https://github.com/toolspell/tfsaz/issues/33) tracks its runbook and [#47](https://github.com/toolspell/tfsaz/issues/47) the repeat of its checks. Recorded results are in [`docs/verification/`](../verification/); the full test suite in each guest's desktop session is still open ([#48](https://github.com/toolspell/tfsaz/issues/48)).

## Set up a Mac

The host must be an Apple Silicon Mac with Homebrew, Go and Git. From the repository root:

```sh
go run ./tools/live setup ubuntu
go run ./tools/live setup macos
go run ./tools/live setup windows
```

`setup OS` installs what is missing, starts the test bed, builds the OS's base VM if it does not exist, and ends with `verify`. It skips steps that are done, so it is safe to rerun. The first Ubuntu run takes 15 to 45 minutes, mostly downloading the desktop, plus the 3 GB ISO download; the first macOS run takes about 25 minutes to pull the 29 GB image and 3 minutes for the rest; the first Windows run takes about 15 minutes once Microsoft's ISO is in the cache, plus the 880 MB virtio-win download.

| Step | Done by | Human part |
| --- | --- | --- |
| Tart (with softnet) and Packer | `doctor -install`, through Homebrew | none |
| Docker | `doctor -install` installs OrbStack | start OrbStack once and accept its setup |
| QEMU, for Windows | `doctor -install`, through Homebrew | none |
| Windows 11 ARM64 ISO | not the tool: Microsoft's download links expire | download it once from [Microsoft](https://www.microsoft.com/software-download/windows11arm64) (English, United States) into `cache/iso/` under the [state directory](live-tests-internals.md#state-outside-the-repository); `setup windows` names the file it expects |
| Homebrew, Go, Git | not the tool | install them first |
| Shorter DHCP leases for VMs | `doctor` only advises it | optional, needs `sudo` ([why](#host-requirements)) |

## Commands

```sh
go run ./tools/live up [OS...]         # services and one reusable clone per OS; prints JSON
go run ./tools/live down              # delete this instance's clones, stop its services and free its slot
go run ./tools/live setup OS          # install prerequisites, build the base if needed and verify
go run ./tools/live verify OS         # boot a throwaway clone and check the bed
go run ./tools/live vm up OS          # clone, boot and connect a VM; prints its details as JSON
go run ./tools/live vm ssh NAME [-- CMD...]     # interactive shell, or one command
go run ./tools/live vm run NAME [-json] [-timeout D] [-gui] -- CMD...   # see "Commands in a clone"
go run ./tools/live vm push NAME LOCAL REMOTE     # copy a file or folder into the clone
go run ./tools/live vm pull NAME REMOTE LOCAL     # copy a file or folder out of the clone
go run ./tools/live vm forward NAME PORT[:PORT]   # host 127.0.0.1:PORT to the clone's loopback port
go run ./tools/live vm screenshot NAME FILE       # PNG of the clone's desktop
go run ./tools/live vm vnc NAME                   # print a VNC URL for the clone
go run ./tools/live vm t3 install NAME            # install the T3 Code desktop app of the reference pin
go run ./tools/live vm t3 start NAME [-port P]    # start it for Playwright; see "Drive T3 with Playwright"
go run ./tools/live vm browser start NAME chrome|edge [-port P] [URL]
go run ./tools/live vm list -json
go run ./tools/live vm stop NAME      # shut it down, keep it for later
go run ./tools/live vm start NAME     # boot it again; prints its details as JSON
go run ./tools/live vm down NAME
go run ./tools/live services up       # services only
go run ./tools/live services down     # stop services and keep the slot for existing clones
go run ./tools/live clean             # delete this instance's clones; keep services and slot
go run ./tools/live prune             # remove instances whose worktree paths are missing
go run ./tools/live services status [-json]      # this instance's running services and host ports
go run ./tools/live status
```

`go run ./tools/live help` lists all commands. For a command's synopsis and flags, use `help CMD [SUB...]` or add `--help` or `-h` to the command path. Help works in CI and from outside the checkout when you run the built binary. Commands only prompt in a terminal. `setup` and `doctor -install` (also spelled `doctor -yes`) install without asking.

Usage errors print the command's synopsis on stderr and exit 2, before the tool touches any state. `vm ssh` passes on the remote exit code. `go run` reports any nonzero code as `exit status N` and exits 1. For the exact code, build the tool once with `go build -o bin/live ./tools/live` and run `bin/live`.

Live tests never run in GitHub CI or `ci-local`. When `CI` or `GITHUB_ACTIONS` is set, the tool allows only help, `status`, `services status`, `mock ca`, `mock requests`, `vm list`, `vm vnc`, `image key`, `image context` and plain `doctor`, which then checks the host without prompting or creating state.

`verify` checks from a new clone that `devops.mock.test` resolves to the mock, that the mock's REST API answers through the router, that internet traffic goes through the tunnel and works, that the router reaches the VM, and that the test user is logged in to the desktop. It prints one line per check, deletes the clone and fails if any check failed.

## Worktrees and instances

Run `up ubuntu macos windows` after building the shared bases. It starts this worktree's services and boots the requested guests in parallel, one clone per OS with the suffix `up`. Its JSON includes the instance ID, subnet, host ports and each guest's `vm.json`. Repeat `up` with the same OS arguments to reuse those clones, including restarting stopped ones. With no OS arguments, `up` starts services only. Use `vm up OS` to make an additional clone.

The instance ID is 12 hex digits derived from the absolute worktree path. To override it, set `TFSAZ_LIVE_INSTANCE` to 1 to 16 lowercase letters or digits and keep it set for that instance's commands. Use one shared `TFSAZ_LIVE_HOME` and the same local Docker engine for all worktrees on the host. A file lock protects the slot registry.

| Command | Clones | Services | Slot |
| --- | --- | --- | --- |
| `clean` | deleted, with their router peers | kept | kept |
| `services down` | kept | removed | kept |
| `down` | deleted | removed | released |

None of them removes the mock and router images: instances share them by input key ([Test bed images](live-tests-internals.md#test-bed-images)). [Remove everything](#remove-everything) says how to remove them.

To keep clones between sessions, stop them with `vm stop NAME` and run `services down`. Later, `up OS...` brings back the `up` clones. For other clones, from `vm up` or a kept harness run, run `services up` and then `vm start NAME`.

The mock runs in memory. When its container starts again, after `services down` or a crash, it starts from the seed data with an empty journal and generates a new CA and new SSH keys. Copy the CA and the SSH client files into clones again, and redo writes a test relied on.

Use `status` to find orphaned slots and `prune` to remove their clones, router state and Docker projects and release their slots. A slot is orphaned when its worktree path is missing. If Docker is stopped, `prune` frees the slot and removes local state and clones, and reports which Docker project remains. Follow [Remove everything](#remove-everything) after starting Docker. `prune` is disabled in CI.

Before upgrading an older bed, run its `clean` and `services down` from the old checkout. Keep the bases and caches. Remove the old `live-tests/.state` symlink if it is still there. For any remaining legacy clone, get its name from `vm list` and delete it with `vm down NAME`.

## Clones

`vm up OS` clones the base (an APFS copy, or a qcow2 overlay for Windows, so it takes little space), boots it headless, replaces the build key with a new key for this clone, sets its hostname and connects it to the router, in about 20 seconds for Ubuntu, 30 for macOS and a minute for Windows. A macOS clone boots into the logged-in GUI session of `admin`, an Ubuntu or Windows clone into the one of `tester`, and `vm up` and `vm start` wait until that session runs; a Windows clone restarts once for its computer name. It prints `vm.json`: name, NAT address (for Windows `127.0.0.1` and a port), address in the test bed, SSH key. `vm stop NAME` shuts a clone down from inside and keeps it with its key and router peer; when it cannot reach the clone it leaves it running, and `-force` stops it without a shutdown, which can lose unwritten changes. `vm start NAME` boots it again, prints the updated `vm.json` (the NAT address can change) and checks that it reaches the mock; the services must be up. `vm down NAME` deletes a clone and its router peer. Names have the form `tfsaz-live-ID-OS-SUFFIX`. Use `vm list` to find the clones owned by this instance.

Set the suffix with `vm up OS -name SUFFIX`. Use 1 to 20 lowercase letters, digits or dashes, starting with a letter or digit. The suffixes `base`, `build` and `up` are reserved. The default is six random hex digits.

Every clone's desktop is 1920x1080. Tart clones get it from `tart set NAME --display 1920x1080px` when `vm up` clones them; GNOME starts at 1280x720 and switches a few seconds after login, so an early screenshot of an Ubuntu clone can be smaller.

### Resources

| Guest | Clone RAM | Clone vCPUs |
| --- | --- | --- |
| Ubuntu | 4 GiB | 2 |
| macOS | 6 GiB | 2 |
| Windows | 6 GiB | 2 |

`vm up` sets the Tart sizes when it clones the base; QEMU gets its size at every boot. Base builds keep 4 vCPUs and 4 GiB (Ubuntu) or 8 GiB (macOS, Windows). Guest memory is fixed: Tart attaches no balloon, and QEMU on macOS does not reliably return memory a balloon frees, so memory a guest touched stays charged to the host until the VM stops. Stop clones you do not use.

To measure a clone, find its process (`qemu-system-aarch64` for Windows, `com.apple.Virtualization.VirtualMachine` for Tart) and sample it at idle and under load:

```sh
ps -axo pid,rss,pcpu,comm | grep -E 'Virtualization.VirtualMachine|qemu-system-aarch64'
top -l 10 -s 1 -pid PID -stats pid,command,cpu,mem,rsize
```

## Commands in a clone

Over SSH, `vm ssh` and `vm run` hand the command to the guest account's shell: bash on Ubuntu, the image's shell for `admin` on macOS (normally zsh), PowerShell 7 (`pwsh`) on Windows. With `-gui`, Ubuntu and macOS run it with `sh`. TFSaz must not rely on PowerShell 7, since users may have only Windows PowerShell 5.1 and cmd: run a step whose shell matters with `powershell` or `cmd /c`.

| Command | Effect |
| --- | --- |
| `vm run NAME [-json] [-timeout D] [-gui] -- CMD...` | Passes on CMD's exit code. Joins CMD's words with spaces into one script and runs it in the clone; quote the script yourself when argument boundaries matter. In SSH mode it passes on stdin. After `--`, flags like `--help` go to CMD. On Windows the script is sent encoded, so quotes survive, and exits with its own `exit N`, else 0 when the last statement succeeded, else 1 after a PowerShell error or when no native exit code is set, else the native command's exit code. End with `exit $LASTEXITCODE` to pass a native code after handled errors. |
| `-timeout D` | Default 10m. On timeout the tool prints a message and exits 124. |
| `-json` | Prints `{"exitCode","stdout","stderr","timedOut","seconds"}` and exits 0 whenever it got a result, also on timeout (`exitCode` 124, `timedOut` true) and when SSH exited with 255 (the command may not have run). When ssh cannot start at all, the tool fails without JSON. Scripts check `exitCode`, `timedOut` and `stderr`. |
| `-gui` | Runs CMD in the test user's desktop session, where keychains, link handlers, browsers and T3 behave as for a user at the machine. macOS: `sh` through `launchctl asuser` in the Aqua session of `admin`, with the login keychain unlocked and apps opening on screen. Windows: through a one-time scheduled task in the interactive session of `tester`, without elevation like the user's own programs; output and exit code go through files in `%TEMP%` that are removed afterwards, output written before a timeout is kept, and on timeout the task and the processes of its script are stopped. Ubuntu: `sh` with the environment the GNOME session of `tester` hands to its systemd user manager (display, desktop name, `XDG_DATA_DIRS` with the snaps' apps), so default apps apply, such as Firefox for links; SSH sessions already share its D-Bus and keyring. A command that starts an app in the background must redirect the app's output (`app >/dev/null 2>&1 &`), or the command does not end until the app does; macOS's `open` and Windows' `Start-Process` detach by themselves. |
| `vm push NAME LOCAL REMOTE`, `vm pull NAME REMOTE LOCAL` | Copy files and folders with scp and the clone's key. Windows paths look like `C:/Users/tester/file`. |
| `vm forward NAME PORT[:PORT]` | Forwards the host's 127.0.0.1:PORT to the clone's loopback port until stopped. Both ports must be 1 to 65535. For example, use it to reach a browser's or an app's debugging port from Playwright on the host. Run it in the background. |
| `vm screenshot NAME FILE` | Saves a PNG of the clone's desktop, taken from the hypervisor, so the guest needs no permission and nobody has to click anything. Windows: QEMU's QMP `screendump` of the virtio GPU. macOS and Ubuntu: the VNC server of Apple's Virtualization.framework, which Tart starts for every clone (`tart run --no-graphics --vnc-experimental`, which prints the address instead of opening a window). |
| `vm vnc NAME` | Prints `vnc://:PASSWORD@127.0.0.1:PORT` for a VNC client or an agent's VNC tool. The hypervisor serves it (Virtualization.framework for Tart, QEMU's own VNC server bound to the virtio GPU for Windows). Loopback only, a new random password per start; nothing runs in the guest for it. |
| `vm t3 install NAME` | Installs the stock T3 Code desktop app at the tag that `tools/syncrepos` pins for `.reference-apps/t3code`, as its user would, and prints the tag. Clones start without T3, so a test can also cover a machine without it. The arm64 installer comes from T3's GitHub release, is checked against the SHA-512 in the release's electron-builder manifest and is cached in the state directory under `cache/t3/TAG/`. macOS: the app in `/Applications`. Ubuntu: the `.deb` through apt. Windows: the NSIS installer, silent, per user. About 10 seconds on macOS, 30 on Ubuntu and a minute on Windows. |
| `vm t3 start NAME [-port P]` | Launches the installed T3 in the desktop session of the clone's test user with Chrome's debugging port P (1 to 65535, default 9222) on the clone's loopback, then waits up to a minute until T3 answers on that port and prints its URL. A T3 that already runs without the port keeps it from opening; quit T3 first. Another app's debugging port on the same number does not count; pick another port. |
| `vm browser start NAME BROWSER [-port P] [URL]` | Starts the browser in the test user's desktop session with a temporary profile per browser and port. Use `chrome` on any guest or `edge` on Windows. Opens URL if given, waits up to a minute for that browser to answer on port P (1 to 65535, default 9222) and prints its loopback URL in the clone. |

On Windows, the first start shows Windows Defender Firewall's prompt for T3, as on a user's machine. It is a system dialog in front of T3, not part of T3's page, so Playwright does not see it; a test that takes screenshots closes it first.

## Drive T3 with Playwright

T3 is an Electron app, so Playwright attaches to it over the Chrome DevTools Protocol, from the host through a forwarded port. Nothing in the clone is installed for it.

```sh
go run ./tools/live vm t3 install NAME
go run ./tools/live vm t3 start NAME                 # prints http://127.0.0.1:9222, the URL in the clone
go run ./tools/live vm forward NAME 19222:9222       # in the background, until the test ends
playwright-cli attach --cdp http://127.0.0.1:19222 --session NAME
playwright-cli --session NAME snapshot               # T3's page as an accessibility tree with refs
playwright-cli --session NAME click REF
playwright-cli --session NAME detach
```

Then stop the forward. A Playwright script attaches the same way, with `chromium.connectOverCDP('http://127.0.0.1:19222')`. Use a free host port per clone. `playwright-cli` keeps its files in `.playwright-cli/` in the current directory, so run it from a temporary directory, not the repository.

T3 checks its update feed and offers a newer release. Do not accept it: tests run the pinned release.

## Drive browsers

Run `vm browser start NAME chrome` (or `edge` on Windows) with a clone name from this instance's `up` output or `vm list`. Follow [Drive T3 with Playwright](#drive-t3-with-playwright) to forward the port and attach from the host. Give apps in the same clone different guest ports when they run together and use a free host port for each forward. Node.js and Playwright run on the host.

To verify a URL opened in the default browser, open a mock URL with `vm run NAME -gui` using `open` on macOS, `xdg-open` on Ubuntu or `Start-Process` on Windows. Check the path and user agent in `mock requests -vm NAME -json`. This also works with Safari and Firefox, which Playwright cannot attach to over CDP. The journal has no URL fragment; a mock web page shows the full URL, fragment included, in its `#location` element.

## Write a live test

Add a test alongside [the smoke cases](../../live-tests/harness/smoke_test.go) in `live-tests/harness`. Its `TestMain` builds `tools/live` once. Iterate over `harness.Platforms(t)` and call `t.Parallel()` in each OS subtest. Use `harness.Start(t)` in the subtest, then `h.Clone(t, osName)` to start a fresh VM. Tests in another package can call `harness.RunTests(m)` from their `TestMain`.

Use `vm.Run` for guest scripts and `vm.RunGUI` for scripts that need the desktop session; both give the guest command 2 minutes, and the tool call a minute more. Check the returned exit code and output. Use `vm.Push` and `vm.Pull` to transfer files and `vm.Screenshot` to save a PNG. Use `vm.InstallT3` and `vm.StartT3` (guest port 9222) for a host URL to T3's debugging port, or `vm.BrowserStart(t, "chrome", 9333, "about:blank")` for Chrome. Read published service ports from `h.Ports`, which comes from `services status -json`. Use `h.Requests(t, vm)` to inspect that clone's traffic; it filters by the clone's address, which a later clone can reuse, so assert on a marker unique to the case, as the smoke test does. `h.ResetJournal(t)` clears all entries for this instance, so use it with exclusive access to the instance.

With the base VM already built, run from the repository root.

```sh
go run ./tools/live up
TFSAZ_LIVE=1 TFSAZ_LIVE_OS=ubuntu go test -count=1 -run TestSmoke ./live-tests/harness
```

| Variable | Effect |
| --- | --- |
| `TFSAZ_LIVE=1` | Enables VM tests. They skip when `CI` or `GITHUB_ACTIONS` is set, even if empty. |
| `TFSAZ_LIVE_OS` | Comma-separated guests to run. The default is `ubuntu,macos,windows`. A guest left out is skipped. |
| `TFSAZ_LIVE_MAX_UBUNTU`, `TFSAZ_LIVE_MAX_MACOS`, `TFSAZ_LIVE_MAX_WINDOWS` | Concurrent clones per guest OS in one test process. Defaults are 2, 2 and 1. macOS cannot exceed 2. |
| `TFSAZ_LIVE_ARTIFACTS` | Directory for per-test artifacts, relative to the repository root or absolute. Defaults to `artifacts/ID/` under the tool's [state home](live-tests-internals.md#state-outside-the-repository). Artifacts survive `down`; delete a custom directory yourself. |
| `TFSAZ_LIVE_KEEP=1` | Stops and keeps every clone that started, also when its test failed, after saving the failure artifacts. When a clone does not stop cleanly, for example after a test turned off SSH, the harness tries a forced stop and reports an error if that fails too. Cleanup logs its name and the command to remove it. |

Run the harness and cleanup in the same worktree with the same `TFSAZ_LIVE_HOME` and `TFSAZ_LIVE_INSTANCE` settings as `up`. Use `go test -v` to see artifact paths and retained clone names on success. The harness stops CDP forwards, attempts to save a screenshot and request journal on failure, and deletes each clone by default. A passing test removes its empty artifact directory.

Use Go's `-timeout` to give longer runs and their cleanup time to finish. Ctrl-C or SIGTERM stops active `live` commands and runs the cleanups. After a hard kill, stop any remaining `vm forward` processes. Run `go run ./tools/live down` to remove the instance and release its slot, or `services down` if you are keeping stopped clones.

## For agents

The [live-tests skill](../../.agents/skills/live-tests/SKILL.md) says when to run live tests and which guests to choose. These rules apply to every live-test task:

- Target the mock. Use the shared Azure DevOps Server testbed only when the maintainer asks ([AGENTS.md](../../AGENTS.md#ways-to-hurt-yourself)).
- The mock's PAT (`MOCK_PAT`) and the keys and certificates it generates are public test values for a throwaway local test bed, not credentials. Any other credential, such as a real PAT for the shared testbed or a TFSaz profile in a VM, follows the [credential rules](../../AGENTS.md#credentials).
- Run `base build` and `setup` in the background and watch their output. Packer waits for SSH for up to 25 minutes on Ubuntu, 10 on macOS and 60 on Windows; a build stuck before SSH answers fails then. [Troubleshooting](#troubleshooting) has the known causes.
- When the Windows ISO is missing, ask the maintainer to download it; do not fetch it from another source.
- When a base command names clones in another worktree, coordinate with its owner before removing them. Leave bases and caches in place between tasks.
- Keep instance state and keys outside the repository.
- Ask the maintainer before the `sudo` step `doctor` advises.
- Keep a clone while its case or PR may still need it, for example for review fixes or a rerun. Stop it with `vm stop NAME` while you do not use it. Delete it with `vm down NAME` only once you are sure nothing needs it any more. Run `down` when you are done with an instance; [Worktrees and instances](#worktrees-and-instances) shows how to keep clones instead.
- Record dated live results in [`docs/verification/`](../verification/).
- The two running macOS VM limit spans worktrees and includes base builds. Stop or delete one before starting a third.
- Use `vm run -json` for scripted checks and `vm run -gui` for anything that needs the desktop session. Take a `vm screenshot` to see the screen instead of asking a human to look.
- Drive T3's UI through Playwright ([Drive T3 with Playwright](#drive-t3-with-playwright)), not by clicking screen coordinates over VNC. Stop your forwards when the test ends.
- Prefer the tools already in use (Tart, Packer, `curl`, `wg`, cloud-init, Homebrew, CoreDNS) over new code.

## Network

The VMs use Tart's NAT for SSH from the host (Windows uses QEMU's user-mode NAT with SSH forwarded from a loopback port). Their IPv4 traffic goes through a WireGuard tunnel to the router, except the NAT subnet and the tunnel's own endpoint, which stay on the NAT.

Each instance uses a Compose project named `tfsaz-live-ID` and a slot from 0 to 15. Slot N uses `172.30.(53+N).0/24`. The tool checks Docker networks, host routes with prefix lengths of 8 or more, and interface addresses before starting services. During allocation it skips slots with overlapping subnets or busy host ports. For a reserved slot, the tool reports the conflict and, for a busy port, prints an `lsof` command to identify its owner. Free the port and retry `up`.

| Address in the subnet | Service | Host port |
| --- | --- | --- |
| `.2` | WireGuard router and VM resolver | UDP `51820+N` on all interfaces, so NATed guests reach it |
| `.10` | `devops.mock.test` on HTTP 8080, HTTPS 443 and 8443, SSH 2222. [Text generator](#text-generator) at `llm.mock.test` on HTTP 80. | Loopback `8080+N`, `8443+N`, `2222+N` |
| `.53` | DNS sinkhole | |
| `.129` through `.254` | VM tunnel addresses; a new clone takes the first free one | |
| `.10:19090` | Mock journal. The router rejects connections to it from the tunnel. | Loopback `19090+N` |

Read the actual host ports from `up` JSON or `services status`. The `services status -json` output includes the instance ID, running service names and state home. It also includes the subnet and host ports when a slot is reserved. Guest service ports stay the same in every slot.

On Windows the tunnel routes `0.0.0.0/1` and `128.0.0.0/1` instead of `0.0.0.0/0`, which would turn on WireGuard for Windows' kill switch and cut off SSH from the host. Without the kill switch Windows would also ask QEMU's DNS server, so the tool sets the router's resolver on the network card too. The router answers ARP for the VM addresses, so containers reach a VM like any other host in the subnet. Traffic from a VM to anywhere else leaves through Docker's NAT, so VMs have internet access for now. Its key and peer list survive a restart; VMs reconnect within seconds.

For HTTPS tests, `go run ./tools/live mock ca` prints the mock CA certificate's path on the host. Clients need to trust this CA explicitly. Copy the certificate into the clone and pass its guest path to TFSaz's `--ca-file` or to `curl --cacert`. Use the hostname `devops.mock.test` for TLS verification. A restarted mock has a new CA.

## Request journal

```sh
go run ./tools/live mock requests [-vm NAME] [-json]
go run ./tools/live mock reset
```

The mock keeps the last 5,000 completed HTTP and HTTPS requests, text generator requests included, in memory. Each entry includes the time, client IP, method, scheme, host, path, query, status, user agent and authentication scheme. It omits authorization values and masks known credentials and common credential query parameters. The router only masquerades traffic destined outside the bed, so journal entries for a VM use the `bedAddress` recorded in its `vm.json`.

`mock requests` prints one line per request from this instance's mock. `-vm NAME` filters by that clone's `bedAddress`, which may also match requests of an earlier clone with the same address, and `-json` prints a JSON array. The host admin API uses the journal port from `up` or `services status`. It exposes `GET /requests` with optional `?ip=ADDRESS` and `DELETE /requests` to clear the journal. Resetting the journal leaves the mock's repositories and work items in place.

## Text generator

T3 generates PR text and commit messages through a model. The mock fakes an OpenAI-compatible one at `http://llm.mock.test/v1`, with one fixed reply, so no real model runs. The [mock's README](../../live-tests/devops-server-mock/README.md#text-generator) says when a test needs it, what it replies and how a test points T3 at it.

## DNS sinkhole

The guests' resolver is dnsmasq on the router. It sends `mock.test` names to the sinkhole and everything else to Docker's resolver, which reaches the internet, and logs every query: `docker logs tfsaz-live-ID-router-1`. To find alias lookups that TFSaz's hosts-file entries should have answered on the client, search that log:

```sh
docker logs tfsaz-live-ID-router-1 2>&1 | grep -E 'visualstudio\.com|dev\.azure\.com'
```

The `dns` service is CoreDNS with a Corefile generated from `live-tests/dns/Corefile`. It logs every query it gets: the router's `mock.test` lookups and the mock container's own lookups.

| Name | Answer |
| --- | --- |
| `devops.mock.test`, `llm.mock.test` | The instance's `.10` for A queries; no answer for other types |
| `*.visualstudio.com`, `dev.azure.com` | NXDOMAIN |
| Anything else | NXDOMAIN |

The other answers are NXDOMAIN rather than REFUSED because Docker's resolver retries a refusal for 10 seconds before it gives up. They are for offline runs, which do not exist yet: those will point the VMs at the sinkhole for every name. Test browsers keep their default settings, as users have them; offline, DNS-over-HTTPS providers are unreachable, so browsers fall back to the system resolver: the hosts file first, then the sinkhole. A browser case with DNS over HTTPS forced on, to find out whether an alias host leaks past TFSaz's hosts lines, is also still planned.

## Base VMs

A base build downloads only what the cache lacks: an ISO matching `iso.json` (`curl` resumes an interrupted download), or the macOS image pinned in `image.json` (Tart pulls it once). The Windows ISO is the exception: the tool only checks it. When Packer fails, the previous base and its key stay in place. An error after Packer succeeded, while the new base replaces the old one, can leave no base or a mismatched key; build again with `-force`. How each base is built: [internals](live-tests-internals.md#base-vms).

### Base commands

| Command | Effect |
| --- | --- |
| `base build OS [-force]` | Build from scratch. Use `-force` to replace an existing base. |
| `base update OS` | Boots the base, upgrades its packages (apt on Ubuntu, Homebrew on macOS), prepares it again; not on Windows |
| `base rm OS` | Deletes the base and its build key |

Base commands and clone creation share a lock per guest OS across worktrees. Base commands refuse while any worktree has clones of that OS, even if those clones are stopped.

## Host requirements

`doctor` checks Tart (2.0 or newer), Packer (1.11), QEMU (9.0), Docker with Compose (2.20), `curl`, `hdiutil`, `ssh` and `ssh-keygen`. It installs missing ones with Homebrew, reports older versions for you to upgrade, and tells apart a tool that is installed but not working, such as a stopped Docker engine. Homebrew refuses formulas from untrusted taps, so the tool trusts exactly `openai/tools/tart` and its dependency `openai/tools/softnet` before installing Tart.

Prebuilt test bed images need a Docker engine on `linux/arm64` (any Apple Silicon Mac) and GitHub CLI 2.102.0 or newer, signed in with `gh auth login`, which verifies their provenance. Without them `up` builds the images locally, which works offline but takes a few minutes on a cold Docker cache. `doctor` reports `gh` as `local builds only` then, and does not fail.

UI tests also need `playwright-cli` or Playwright on the host; `doctor` does not check them.

It also advises shortening macOS's DHCP leases for VMs from a day to ten minutes: every clone gets a new MAC address, and the pool of about 250 addresses runs out after that many clones in a day.

## Troubleshooting

In the commands below, set `state_dir="${TFSAZ_LIVE_HOME:-$HOME/.tfsaz-live}"` to use the effective state directory.

- A base build prints a VNC address and password; open it to watch the installer or, on macOS, the desktop.
- The Ubuntu system must ask DHCP with its MAC address (`dhcp-identifier: mac` in the autoinstall and clone network config); otherwise Tart finds no IP and Packer waits until its SSH timeout.
- Packer finds the Ubuntu installer's autoinstall prompt by OCR, which sometimes misreads it; when a build waits longer than 5 minutes after "Typing commands", stop it and build again. `PACKER_LOG=1` shows what the OCR reads.
- A running Ubuntu installer accepts the build key for the user `installer`; `ssh -i "$state_dir/keys/ubuntu-build/id_ed25519" installer@IP` shows its progress.
- A clone's Tart output is in `$state_dir/instances/ID/runs/NAME/tart.log`. When `vm up` deletes a failed clone, it tries to save its startup log under `failed-starts/` and reports the path or the archive error. It includes the last lines with the VNC address omitted.
- To see a clone's screen, use `vm screenshot NAME FILE`. A Windows build prints a VNC address without a password and has no clone name; to capture its screen, send QMP `screendump` to `$state_dir/build/windows/qmp.sock`, for example `{ echo '{"execute":"qmp_capabilities"}'; sleep 1; echo '{"execute":"screendump","arguments":{"filename":"/tmp/screen.png","format":"png"}}'; sleep 2; } | nc -U SOCKET`. A Windows build logs its first logon to `C:\Windows\Temp\tfsaz-setup.log`; a clone's QEMU output is in `$state_dir/instances/ID/runs/NAME/qemu.log`.
- A macOS clone's tunnel logs to `/var/log/tfsaz-live-wg0.log` in the VM; `sudo launchctl kickstart -k system/com.tfsaz.live.wg0` restarts it.
- When `vm down` cannot remove a VM from its hypervisor, it keeps the clone's directory and router peer; run `vm down NAME` again once the hypervisor works. When `down` fails because a Compose network still has containers, disconnect containers that are not part of the project and retry.
- HTTPS or SSH to the mock fails in a clone that worked before: the mock restarted with new keys. `mock ca` prints the new CA's path; the SSH client files are in the same directory. Copy them into the clone again.
- `vm t3 start` or `vm browser start` launches the app (up to 90 seconds), then fails after another minute when nothing answers on the port, or when the answer on `/json/version` comes from another app or has no debugger WebSocket. Check that the app runs and pick another port.
- On a Windows clone from a base built before the `NoNewAppAlert` policy, the app picker can appear when a link opens. Edge is listed under **Default app**; choose **Just once**.

## Remove everything

Use the same `TFSAZ_LIVE_HOME` as for setup. Set `state_dir="${TFSAZ_LIVE_HOME:-$HOME/.tfsaz-live}"` in your shell before running the cleanup commands.

| Change on the host | Undo |
| --- | --- |
| Per-instance clones, Docker containers and network, router and mock keys | `go run ./tools/live down` in each worktree, with its instance override if set. Use `go run ./tools/live prune` for deleted worktrees. |
| Test bed images: verified pulls `ghcr.io/toolspell/tfsaz-live-*`, local builds `tfsaz-live-local/*` and per-project builds from before the prebuilt images | Once all instances are down, see [below](#remove-the-test-bed-images). |
| Shared bases, caches, locks and harness artifacts | Once all instances are down, run `rm -rf -- "$state_dir"`. Delete a custom `TFSAZ_LIVE_ARTIFACTS` directory too. |
| Tart, softnet, Packer and QEMU from Homebrew | `brew uninstall tart softnet packer qemu` |
| OrbStack, if `doctor -install` installed it only for the test bed | `brew uninstall --cask orbstack` |
| Trusted formulas `openai/tools/tart`, `openai/tools/softnet` and `hashicorp/tap/packer` (Homebrew trusts the last one itself when installing it) | `brew untrust --formula openai/tools/tart openai/tools/softnet hashicorp/tap/packer` |
| Taps `openai/tools` and `hashicorp/tap` | `brew untap openai/tools hashicorp/tap` |
| Shorter DHCP leases, if you applied the advice | `sudo defaults delete /Library/Preferences/SystemConfiguration/com.apple.InternetSharing.default.plist bootpd` |

If `prune` reports a project it could not reach, start Docker and set `project` to the exact project it reported. List its containers with `docker container ls -aq --filter "label=com.docker.compose.project=$project"` and remove the returned IDs with `docker container rm -f ID...`. List its networks with `docker network ls -q --filter "label=com.docker.compose.project=$project"` and remove the returned IDs with `docker network rm ID...`. Its images are shared by all instances; remove them as [below](#remove-the-test-bed-images).

### Remove the test bed images

Once all instances are down, list the mock and router images with their IDs, including digest-only pulls (tag `<none>`) and the per-project builds from before the prebuilt images:

```sh
docker image ls --format '{{.Repository}}:{{.Tag}} {{.ID}}' \
  | grep -E '^(ghcr\.io/toolspell/tfsaz-live-|tfsaz-live-local/|tfsaz-live-[a-z0-9]+-(devops-server-mock|router):)'
```

Remove the listed IDs with `docker image rm -f ID...`. The next `up` pulls or builds them again.
