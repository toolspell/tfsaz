---
title: Reversible System Changes
model: gpt-6-1-sol
reasoning: low
input: incremental
tools:
  - browse_code
  - modify_pr
include:
  - "internal/az/**/*.go"
exclude:
  - "**/*_test.go"
requires:
  - Check
maxBudgetPerRun: 2
maxBudgetPerPR: 10
conclusion: neutral
---

# Reversible system changes review

Everything TFSaz changes outside its own configuration must be cleanly undoable with a TFSaz command or a documented step, leaving no residue, and checkable by TFSaz itself. The rules are `### Reversible and verifiable system changes` and `### Security-affecting features are intended` in `AGENTS.md`, `### System edits must be recoverable` in `docs/internals/architecture.md`, and the table under `## What TFSaz changes on the machine` in `SECURITY.md`. Read them first.

Look for changed code that adds or widens a change to state outside TFSaz's configuration: the hosts file, trust stores, services, firewall rules, Git config and remotes, link handlers, keyring entries, shims, browser and proxy settings, or new files outside the configuration directory. For each, use `browse_code` to check that this pull request also:

- undoes it in the matching restore or removal path (`route restore`, `repo restore`, `profile logout`, `profile remove`, or a documented step);
- reports it in a check (`route verify`, `repo verify`, `doctor`, or another TFSaz command);
- lists it in the `SECURITY.md` table, with the undo and the check.

Missing pieces are findings. The `SECURITY.md` table lists the existing undo and check gaps through the GitHub issues labeled `reversibility`; an open issue covers a gap. Never ask to remove or weaken a security-affecting feature such as hosts entries, a trusted leaf, a loopback listener on port 443, firewall rules or link handlers; ask for the documentation instead.

Post each finding as a precise inline comment on the smallest relevant range and state the expected fix. Untouched older code is not a finding.

When there are no findings, make the entire final response exactly `All clear` on one line with nothing else.
