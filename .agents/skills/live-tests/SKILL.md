---
name: live-tests
description: Set up, run, verify or clean up the live-test VMs and their test bed (go run ./tools/live), or change live-tests/ or tools/live.
---

# Live-test VMs

Live tests check TFSaz on a disposable client machine: Tart VMs on an Apple Silicon Mac that reach a Docker mock of Azure DevOps Server through a WireGuard router. The runbook is [Live tests](../../../docs/operations/live-tests.md); read it before changing the bed or a VM definition.

## Use the bed

1. `go run ./tools/live setup ubuntu` brings the Mac to a working bed and ends with `verify`. It is safe to rerun; it skips done steps.
2. `go run ./tools/live vm up ubuntu` prints the clone's details as JSON: `name`, `ip` (NAT address for SSH), `bedAddress`, `sshKey`. Run commands with `go run ./tools/live vm ssh NAME -- CMD`.
3. `go run ./tools/live vm down NAME` when done, `go run ./tools/live clean` before you hand back. Leave the base VM; it takes 8 minutes to rebuild.

## Rules

- Never point live tests at the shared Azure DevOps Server testbed unless the maintainer asks; the mock is the default target ([AGENTS.md](../../../AGENTS.md#ways-to-hurt-yourself)).
- A base build takes about 8 minutes. Run it in the background and watch its output; Packer gives up after 25 minutes without SSH. Do not wait longer: the runbook's [Troubleshooting](../../../docs/operations/live-tests.md#troubleshooting) has the known causes.
- `base build`, `base update` and `base rm` refuse while clones exist. Do not work around that; remove the clones.
- Only `base build` downloads the 3 GB ISO, and only when the cache has no copy matching `live-tests/vms/<os>/iso.json`. Do not delete `~/.tfsaz-live/cache`.
- Large and secret files live in `~/.tfsaz-live`, linked as the gitignored `live-tests/.state`. Never commit them, never copy keys into the repository.
- `go run` turns a remote exit code into 1; build the tool with `go build -o bin/live ./tools/live` when the code matters.
- The only step that needs `sudo` is the optional DHCP lease advice from `doctor`. Ask the maintainer before you run it.
- Prefer existing tools over new code: Tart, Packer, `curl`, `wg`, cloud-init and CoreDNS already do most of the work.
- Windows (UTM) and macOS (Tart) are planned: add `live-tests/vms/<os>/` and an entry in the OS list in `tools/live/env.go`.
- Record dated live results in `docs/verification/`, not in the runbook.
