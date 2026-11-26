---
title: Platform Parity
model: gpt-6-1-sol
reasoning: low
input: incremental
tools:
  - browse_code
  - modify_pr
include:
  - "**/*.go"
  - "docs/user/**/*.md"
exclude:
  - "**/*_test.go"
  - ".reference-apps/**"
  - "tools/**"
requires:
  - Check
maxBudgetPerRun: 2
maxBudgetPerPR: 10
conclusion: neutral
---

# Platform parity review

TFSaz supports macOS, Windows and Linux at feature parity. The rule is `### Platform parity` in `AGENTS.md`; read it first and hold the changed lines to it.

Look for a feature or a user doc step this pull request adds or changes on fewer than all three platforms: an OS-specific file (`_darwin.go`, `_linux.go`, `_windows.go`, `_unix.go`, `_other.go`) or a `runtime.GOOS` branch without a counterpart for each other platform, or a user guide step for one or two of them. Each platform it misses needs explicit "not supported on this platform" handling (a clear error and a nonzero exit, never a silent no-op or a crash) and an entry under `## Platforms` in `docs/backlog.md`, both in this pull request. Use `browse_code` to check that the counterpart or the entry exists before you report it missing.

A platform-specific implementation (launchd, systemd, Task Scheduler) is fine. Untouched older code is not a finding.

Post each finding as a brief inline comment on the smallest relevant range: name the missing platform and whether the handling, the backlog entry, or both are missing.

When there are no findings, make the entire final response exactly `All clear` on one line with nothing else.
