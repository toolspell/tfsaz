---
name: live-tests
description: Verify changes to system integration, routing, setup or restore, browser or link handling, or commands T3 calls end to end in disposable VMs.
---

# Live tests

Use live tests when a change needs evidence from a real client OS or stock T3 Code. For docs and pure logic covered by unit tests, use the normal developer checks. Run the VM suite on an authorized Apple Silicon Mac outside GitHub CI and `ci-local`. The VMs and Docker network depend on the host.

## Choose guests

Use the diff and the behavior it changes to choose platforms:

| Changed code | Guest |
| --- | --- |
| `*_darwin.go` | `macos` |
| `*_windows.go` | `windows` |
| `*_linux.go` | `ubuntu` |
| Shared code | Each guest whose behavior changes |

Run only the relevant cases. Read [Live tests](../../../docs/operations/live-tests.md) for host setup, the mock, guest commands, and troubleshooting.

## Run a case

For automated cases, use the [Go live-test harness](../../../docs/operations/live-tests.md#write-a-live-test). Run `up` to start this worktree's services and `down` after the suite to release the slot. The harness manages the test clones and reads this instance's ports and journal through the tool.

From the repository root, prefix each command below with `go run ./tools/live`. On a host with the base already built:

1. Run `up OS...` to start this worktree's services and one clone per selected guest. Take `NAME` from the `vms` array and any host ports you need from `ports`. Use `vm up OS` for an additional clone.
2. For T3 cases, run `vm t3 install NAME` and `vm t3 start NAME`. For browser cases, run `vm browser start NAME chrome` or use `edge` on Windows. Choose different guest ports when T3 and a browser share a clone. Follow the [Playwright steps](../../../docs/operations/live-tests.md#drive-t3-with-playwright) to forward the port and drive the UI.
3. Run the case with `vm run NAME [-json] [-gui] -- CMD...`. Use `-json` for scripted checks or `-gui` for commands that need the desktop session. Target the Azure DevOps Server mock.
4. Inspect traffic with `mock requests -vm NAME`. Capture UI evidence with `vm screenshot NAME FILE` when relevant.
5. Stop your forwards, then run `down` to delete this worktree's clones, remove its services and locally built images, and free its slot. To delete just this worktree's clones, use `clean`. To retain clones for a rerun, stop them with `vm stop NAME` and run `services down`. Repeat `up OS...` to resume its clones. Check `status` for orphaned slots and use `prune` to remove instances whose worktree paths are missing.

Follow the [runbook's agent instructions](../../../docs/operations/live-tests.md#for-agents) for retained clones, cleanup, and dated verification records.
