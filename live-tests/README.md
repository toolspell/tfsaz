# Live tests

The test bed and VM definitions for live tests. After setup, run `go run ./tools/live up [OS...]` in a worktree to start its instance. Run `go run ./tools/live clean` to delete its clones, or `go run ./tools/live down` to also remove its services and locally built images and release its slot. Use `go run ./tools/live prune` for instances whose worktree paths are missing. The worktrees on a host share VM bases and caches. The runbook is [Live tests](../docs/operations/live-tests.md).

Run `go run ./tools/live help` for the command list or `go run ./tools/live help vm run` for a command's synopsis and flags.
