---
name: live-tests
description: Verify changes to system integration, routing, setup or restore, browser or link handling, or commands T3 calls end to end in disposable VMs. Also for work on the live-test tooling.
---

# Live tests

Use live tests when a change needs evidence from a real client OS or stock T3 Code. For docs and pure logic covered by unit tests, use the normal developer checks. The [overview](../../../docs/operations/live-tests-overview.md) shows how the VMs, the test bed and the tool fit together.

## Choose guests

Use the diff and the behavior it changes to choose platforms:

| Changed code | Guest |
| --- | --- |
| `*_darwin.go` | `macos` |
| `*_windows.go` | `windows` |
| `*_linux.go` | `ubuntu` |
| Shared code | Each guest whose behavior changes |

Run only the relevant cases.

## Run a case

- An automated case goes into the [Go live-test harness](../../../docs/operations/live-tests.md#write-a-live-test).
- A manual case follows the [runbook](../../../docs/operations/live-tests.md): `up OS...`, then T3 or a browser through [Playwright](../../../docs/operations/live-tests.md#drive-t3-with-playwright), commands with `vm run`, evidence from `mock requests` and `vm screenshot`.
- Follow the [rules for agents](../../../docs/operations/live-tests.md#for-agents) on credentials, kept clones, cleanup and dated records.

## Change the tooling

Before you change `live-tests/` or `tools/live`, read the [internals](../../../docs/operations/live-tests-internals.md). The mock is a Go module of its own; `go run ./tools/dev check` and `ready` cover it.
