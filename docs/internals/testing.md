# Testing

Rules for writing tests, and for asking for them in review. This page is their one home; AGENTS.md links here under Verify. How to run the suites: [Development](../operations/development.md).

## What to test

- A bug fix starts with a test that fails before the fix, for the reason the bug names. Then the fix. Put the test in an existing case where one fits.
- Extend before you add. Most tests are tables: a `[]struct` of cases run with `t.Run(tc.name, ...)` (for example `catalog_test.go`, `client_test.go`, `boards_write_test.go`). When a table covers the behavior, add a row or an assertion there. Write a new `Test` function only when no table fits.
- A command T3 calls follows the adapter's output shapes and errors. [Adding a command](t3-compatibility.md#adding-a-command) lists what to cover.

## Isolation

Every test runs against temporary state, never the developer's configuration, keyring, system files or clones. [Ways to hurt yourself](../../AGENTS.md#ways-to-hurt-yourself) explains what each one would damage.

| Real state | In tests | Example |
| --- | --- | --- |
| Configuration | `t.Setenv("TFSAZ_CONFIG", ...)` with an absolute path under `t.TempDir()` | `cli_test.go` |
| Keyring | `app{store: &memoryStore{...}}`; never `systemStore` | `api_call_test.go`, `cli_test.go` |
| Keyring entry list `~/.tfsaz/keyring-entries.json` | `app{keyringList: testKeyringList(t)}`; without it, commands that write or delete a PAT fail | `profiles_test.go` |
| Keyring of a built binary | Build it with `-tags tfsaztest`: `credentials_testbinary.go` swaps in a store that is always empty and refuses writes, and no keyring entry list | `setup_manager_test.go`, `https_git_test.go` |
| System commands of `route setup` and `route restore` | Replace `runRouteSystemCommand` and restore it in `t.Cleanup` | `route_system_test.go` |
| Home directory | `t.Setenv` of `HOME` and `USERPROFILE` to a temp directory | `route_managed_test.go` |
| Git configuration | `GIT_CONFIG_GLOBAL` to a temp file or `os.DevNull`, `GIT_CONFIG_NOSYSTEM=1`, clones under `t.TempDir()` | `setup_manager_test.go`, `setup_ssh_test.go` |
| Azure DevOps Server | Loopback `httptest` TLS servers; names such as `example.invalid` and `192.0.2.1` | `client_test.go`, `profiles_test.go` |

- `t.Setenv` and package-level hooks such as `runRouteSystemCommand` rule out `t.Parallel`. The suite has no parallel tests; keep it that way unless a test touches neither.
- The one test that uses the real keyring is `TestStockT3Adapter`, with a random entry it removes, together with its name in the real keyring entry list. Its binary is built without the `tfsaztest` tag.

## Waiting

- Wait on the event that marks the milestone: a channel the code under test signals (the status channel in `route_check_test.go`, the request channel in `https_git_test.go`), a goroutine's done channel (`route_cli_test.go`), process exit, or `ctx.Done()`.
- Bound the wait with a context deadline or a `select` on `time.After`. The bound is a ceiling for a hung test, not the wait itself.
- No `time.Sleep` and no poll loops. Polls that could still wait on an event have an issue, such as [#21](https://github.com/toolspell/tfsaz/issues/21). A wait on OS timing that has no event carries a comment saying why.

## Platforms

- OS-specific tests use the file suffixes `_darwin_test.go`, `_linux_test.go` and `_windows_test.go`, named after the file they test. A test for several systems uses a build tag, such as `//go:build darwin || linux` in `route_system_unix_test.go`.
- CI runs the suite with the race detector on macOS, Windows and Ubuntu. A platform test you cannot run locally still runs there.
- Parity: when you add a test for one system's integration, check the other two. A missing counterpart is a gap to report or open a `platform:<os>` issue for.
- A test that needs an external tool skips with a reason when it is missing, such as `t.Skip("git unavailable")`.

## Fixtures and live tests

- Strip identities, internal URLs and secrets from fixtures in `internal/az/testdata/` and from test code.
- Keep offline fixture results separate from live results. Dated records go to [`docs/verification/`](../verification/), one file per kind.
- Live tests stay opt-in. A test that changes real OS state, needs a real client or needs the live-test VMs skips unless its variable is set, and the variable is listed in [Development](../operations/development.md#opt-in-live-system-tests).
- Write automated tests that need a whole machine with the [Go live-test harness](../operations/live-tests.md#write-a-live-test) and run them in disposable VMs against the Azure DevOps Server mock.
- When a live test needs a PAT, request it through T3's secret input. Use a disposable credential for revocation tests. The one exception is the live-test mock: its PAT (`MOCK_PAT`) and the keys and certificates it generates are public test values, not credentials.
- After a live or manual run, return the machine or session to a clean state as soon as the state is no longer needed ([AGENTS.md rule](../../AGENTS.md#reversible-and-verifiable-system-changes)). [Return to a clean state](../operations/development.md#return-to-a-clean-state) lists the undo and check per kind of change.
