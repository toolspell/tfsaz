# Live tests internals

How the live-test bases are built, where the files are and what the tool keeps on the host. Read it before you change `live-tests/` or `tools/live`. The [overview](live-tests-overview.md) has the big picture, the [runbook](live-tests.md) the commands.

## Base VMs

### Ubuntu

`base build ubuntu` checks the ISO, writes the autoinstall config with a new build key to a seed ISO, and runs Packer with the Tart plugin. The installer runs unattended and installs the server system; Packer answers its one autoinstall prompt. `provision.sh` then installs the desktop (`ubuntu-desktop-minimal`) on the booted system, where snapd runs (in the installer, its snaps such as Firefox never finish), logs `tester` in to GNOME at boot, gives `tester` a login keyring without a password, which stays unlocked as after a password login, removes the welcome wizard, turns off screen lock and blanking, and adds `secret-tool` for tests that check Secret Service entries. The last step, `prepare-base.sh`, removes what must differ per clone (SSH host keys, machine ID, interface-specific network config) and turns off Ubuntu's automatic updates, so clones do not run `apt` during tests. The build goes into `tfsaz-ubuntu-build` and replaces `tfsaz-ubuntu-base` only when it succeeded. It takes 15 to 45 minutes, depending on the Ubuntu archive's speed, and 9 GB.

The base has one user, `tester`, with passwordless sudo, no password and no other login than the build key. Firefox stays GNOME's default browser. Chrome comes from Google's apt repository with its signing key scoped to that repository.

### macOS

`base build macos` starts from Cirrus Labs' vanilla macOS 27 image, pinned by digest in `image.json`: macOS after Setup Assistant, with the user `admin` (password `admin`) logged in automatically, Remote Login on, passwordless sudo and Gatekeeper off. Tart pulls it once (about 29 GB). `provision.sh` makes the build key the only SSH login (the image's public password stops working over SSH), installs Homebrew with the Command Line Tools and `wireguard-tools`, adds the launchd daemon `com.tfsaz.live.wg0` that starts the tunnel on boot, and turns off automatic updates. `prepare-base.sh` deletes the SSH host keys, which sshd recreates per clone. With the image in Tart's cache, the build takes about 3 minutes and `base update macos` about 2.5 minutes.

The guest needs a host with at least its macOS version, and macOS runs at most two macOS VMs at a time; the tool checks both. A newer macOS comes with a new pin in `image.json` and `base build macos -force`.

Safari stays the default browser. Homebrew installs Chrome. Provisioning verifies Google's code signature before clearing Chrome's first-launch quarantine.

### Windows

`base build windows` checks Microsoft's Windows 11 ARM64 ISO and the virtio-win ISO (downloaded once, about 880 MB), writes `Autounattend.xml` with a new password for `tester` and `setup.ps1` with a new build key to a seed ISO, and runs Packer with the QEMU plugin. QEMU runs the VM with Apple's Hypervisor.framework and UEFI firmware from Homebrew's QEMU. The `virt` machine has no IDE or SATA, so the three ISOs are USB drives, and the disk is NVMe, for which Windows has an inbox driver. The VM has two display adapters: `ramfb`, which Windows' boot manager needs (its linear framebuffer tops out at 1024x768), and a virtio GPU at 1920x1080 for the desktop. Setup runs unattended: it skips the TPM, Secure Boot and online account checks, installs Windows 11 Pro with the generic install key (not activated), and logs `tester` in automatically. At first logon, `setup.ps1` installs the virtio network driver (NetKVM) and the virtio GPU driver (`viogpudo`) from the virtio-win ISO, shows only the GPU's display (Windows would keep `ramfb` at 800x600 as its main display, where new windows open), and installs OpenSSH Server with the build key as the only login. `provision.ps1` downloads and installs PowerShell 7 and WireGuard for Windows in the guest (each pinned by hash and checked for its publisher's signature) and turns off automatic updates and sleep. `prepare-base.ps1` deletes the SSH host keys, which sshd recreates per clone, makes PowerShell 7 the SSH shell and shuts the VM down. The build takes about 13 minutes and 16 GB.

Windows has no update in place: `base update windows` refuses. A newer Windows comes with a new ISO pin in `iso.json` and `base build windows -force`.

Edge stays the default browser. Provisioning verifies Google's Authenticode signature before installing Chrome from its ARM64 enterprise MSI. The `NoNewAppAlert` policy suppresses the app picker when a user opens a link after Chrome is installed.

## Layout

| Path | Contents |
| --- | --- |
| `live-tests/compose.yaml` | The test bed: [mock](../../live-tests/devops-server-mock/README.md), DNS sinkhole, VM router |
| `live-tests/devops-server-mock/` | The Azure DevOps Server mock, a Go module of its own |
| `live-tests/dns/Corefile` | The DNS sinkhole |
| `live-tests/router/` | The WireGuard router that connects VMs |
| `live-tests/vms/<os>/` | A base VM: ISO or image pins, unattended install config, Packer template, provisioning, base preparation |
| `live-tests/harness/` | The Go harness and the smoke test |
| `tools/live/` | The tool |

A new guest OS gets a `live-tests/vms/<os>/` directory, an entry in the tool's OS list in `tools/live/env.go`, its guest commands in `tools/live/guest.go`, and its build inputs in `buildInputs` in `tools/live/base.go`. A new hypervisor implements the `provider` interface in `tools/live/provider.go`, as Tart (`tart.go`) and QEMU (`qemu.go`) do.

## Test bed images

The mock and router images are identified by an input key: SHA-256 over their build parameters and the files of their build context, the explicit list in `tools/live/images.go` (test files are not part of it). `image key IMAGE` prints the key and `image context IMAGE DIR` stages that context; local builds and CI build only from the staged directory, so Docker's ignore rules never decide what goes in. Base images and CoreDNS are pinned by digest and Dependabot bumps them; to refresh the packages without a base bump, change `ARG REFRESH` in the Dockerfile. Either changes the key.

Only `up`, `services up` and `setup` resolve images; every other command uses the defaults in `compose.yaml` and never hashes or contacts a registry. Resolving, per image:

1. On a `linux/arm64` engine without another `DOCKER_DEFAULT_PLATFORM`, a verified digest cached for the key in `images/` (see below) is used as is, and pulled again by digest if Docker lost it. This needs no network when the image is present.
2. Otherwise, with a `linux/arm64` engine and a signed-in `gh` 2.102.0 or newer, the tool pulls `ghcr.io/toolspell/tfsaz-live-IMAGE:in-KEY` and accepts the digest the pull reports only if `gh attestation verify --hostname github.com` confirms provenance from `.github/workflows/testbed-images.yml` in `toolspell/tfsaz` on `refs/heads/main` (pinned to github.com, so `GH_HOST` cannot redirect it), the image is `linux/arm64` and its label `org.toolspell.tfsaz-live.key` equals the computed key. A rejected image keeps its tag but is never run: the tool runs a prebuilt image only by its verified digest (and a local build by its image ID), never by tag, and removing the tag could delete an image another instance just verified. A `DOCKER_DEFAULT_PLATFORM` other than `linux/arm64` counts as not eligible, since Compose would pick another variant.
3. Otherwise it builds `tfsaz-live-local/IMAGE:in-KEY` from the staged context, or reuses that tag. Local builds are never treated as verified.

Compose then gets the exact reference (`ghcr.io/...@sha256:DIGEST` or the ID `docker build --iidfile` wrote) with `pull_policy: never` and starts with `--no-build`. Every other Compose call passes the defaults explicitly, so an exported `TFSAZ_LIVE_MOCK_IMAGE` or `TFSAZ_LIVE_ROUTER_IMAGE` never reaches it. Teardown keeps the images, since other instances share them.

The digest cache is as trusted as the rest of the state directory, which also holds the clones' SSH keys and the router keys: keep `TFSAZ_LIVE_HOME` private to your account. Local builds are trusted as far as the Docker engine is: whoever can retag local images controls Docker anyway.

## State outside the repository

The tool stores state outside the worktree in `~/.tfsaz-live` (or the absolute path in `TFSAZ_LIVE_HOME`). `setup` recreates it when needed.

| Path | Contents |
| --- | --- |
| `tart/` | Tart's home for these VMs only (`TART_HOME`), so they never mix with other Tart VMs; its `cache/` holds the pulled macOS image |
| `qemu/` | Shared QEMU base images and one directory per named clone: `disk.qcow2` (a clone's is an overlay on the base's), UEFI variables, its last SSH port, and while it runs its PID, QMP socket and `vnc` (VNC address with password, mode 600) |
| `cache/iso/`, `cache/t3/` | Installer ISOs, checked against the SHA-256 in `vms/<os>/iso.json` (and `virtio.json` for Windows); T3 installers |
| `packer/` | Packer plugins and cache |
| `keys/<os>-base/` | The key of the last base build; for Windows also the password of `tester` |
| `images/IMAGE/linux-arm64/KEY` | Verified digest of a prebuilt test bed image for that input key |
| `slots.json`, `*.lock` | Shared slot registry and locks for allocating slots, changing bases and starting macOS VMs |
| `instances/ID/runs/NAME/` | Clone SSH key, known_hosts, `vm.json` and VM log. `tart.log` holds the VNC address with password (mode 600). |
| `instances/ID/failed-starts/` | Last startup log per failed clone. `down` and `prune` remove these logs. |
| `instances/ID/router/`, `instances/ID/mock-keys/`, `instances/ID/Corefile` | Instance WireGuard keys and peers, mock CA and SSH keys, generated sinkhole config |
| `instance-locks/ID.lock`, `qemu/start.lock` | Locks for instance lifecycle changes and QEMU starts |
| `artifacts/ID/` | Harness artifacts |
