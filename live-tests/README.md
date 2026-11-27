# Live tests

The test bed and VM definitions for live tests. After setup, run `go run ./tools/live up [OS...]` in a worktree to start its instance. Run `go run ./tools/live down` to remove it. The worktrees on a host share VM bases and caches. The runbook is [Live tests](../docs/operations/live-tests.md).

Run `go run ./tools/live help` for the command list or `go run ./tools/live help vm run` for a command's synopsis and flags.
