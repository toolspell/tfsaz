# Development

## Verification

Run `go vet ./...` and `go test -count=1 ./...` for the native suite. Tests use temporary Git repositories, loopback HTTP/TLS servers and a test credential store. Install Git and OpenSSH for transport tests, Node for the TLS probe, and GIO on Linux for desktop dispatch. On Windows use Git for Windows with its bundled shell. The [native test workflow](../../.github/workflows/test.yml) runs this suite on macOS, Windows and Ubuntu. All three jobs enable Go’s race detector. To run it locally, use `go test -race -count=1 ./...`. Windows x64 needs a compatible MinGW-w64 C compiler on PATH. [Go race detector requirements](https://go.dev/doc/articles/race_detector#Requirements)

The opt-in integration test builds the actual executable and runs T3's existing Azure adapter against it. It requires Git, Node with TypeScript support (tested with Node 26), a T3 source tree with its lockfile dependencies installed, and a usable OS keyring. It creates a random keyring entry for its loopback server and removes the entry at the end.

```sh
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

In PowerShell, set `$env:TFSAZ_T3_ROOT = 'C:\path\to\t3code'`, then run the same `go test` command. Use a logon with a loaded user profile and an accessible Credential Manager. For unattended Linux runs, start an unlocked Secret Service collection on the test session's D-Bus. The fixture uses its own random keyring entry.
