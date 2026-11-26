---
title: Compatibility Contract
model: gpt-6-1-sol
reasoning: low
input: incremental
tools:
  - browse_code
  - modify_pr
include:
  - "main.go"
  - "internal/az/**/*.go"
exclude:
  - "**/*_test.go"
requires:
  - Check
maxBudgetPerRun: 2
maxBudgetPerPR: 10
conclusion: neutral
---

# Compatibility contract review

TFSaz works only while stock T3 Code cannot tell it apart from Microsoft's Azure CLI with the `azure-devops` extension. The rules are `### Compatibility contract` in `AGENTS.md`, `docs/internals/rest-api.md`, and `## Adding a command` in `docs/internals/t3-compatibility.md`. Read them first and hold the changed lines to them. The T3 and Azure CLI sources are not in the repository; judge only against these docs, the tests and the code.

Findings:

- A change to the arguments, JSON output, stderr text or exit code of a command T3 calls (the commands in the table under `## Commands T3 calls` in `docs/user/az-compatibility.md`) without a test in this pull request that pins the new behavior.
- A change to how Azure-style aliases or synthetic URLs (`tfsaz-<24 hex>.visualstudio.com`) are formed, so that they might no longer match the hosts and URL shapes T3 recognizes (`## Why stock T3 cannot reach Azure DevOps Server` in `docs/internals/t3-compatibility.md`).
- A new REST call that builds an `_apis/...` path instead of resolving it through the catalog, without a comment at the call site that gives a reason.
- A request that sends an `api-version` other than `7.1` or a `7.1-preview.N` variant, outside the exceptions `docs/internals/rest-api.md` lists.
- A change to a command T3 calls that leaves `docs/user/az-compatibility.md` or `docs/user/t3-features.md` wrong.

A finding never asks to change T3 Code, Microsoft's Azure CLI or the extension; TFSaz solves the problem from outside. Untouched older code is not a finding.

Post each finding as a precise inline comment on the smallest relevant range and state the expected fix. When a change touches a command T3 calls, add one comment asking the author to state in the PR whether `TestStockT3Adapter` ran.

When there are no findings, make the entire final response exactly `All clear` on one line with nothing else.
