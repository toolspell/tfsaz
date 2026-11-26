Use Macroscope's default approvability criteria.

Additionally, any pull request that changes product defaults is not auto-approvable and requires human review.

Any pull request that adds or broadens a directive that disables or suppresses a lint, vet,
vulnerability, secret-scanning, or other static-analysis finding is not auto-approvable and
requires human review. This includes `//nolint` and `//lint:ignore` comments, exclusions in
`.golangci.yml`, and allowlist entries in `.gitleaks.toml`.

Any pull request that changes one of these is not auto-approvable and requires human review:

- The enforcement points `SECURITY.md` lists under "What TFSaz protects": where a PAT is
  stored, which collection it is sent to, redirects, TLS, identity selection, and trace output
  (`internal/az/{credentials,client,rest_url,api_call,cli,repo_commands,trace}.go`), and the Git
  helpers that must never read the keyring (`internal/az/{preserve_git,https_git}.go`).
- Code that changes state outside TFSaz's configuration, which `SECURITY.md` lists under "What
  TFSaz changes on the machine" and the "Reversible and verifiable system changes" rule in
  `AGENTS.md` governs: `internal/az/{setup_*,preserve_*,route_*,https_*,link_*,shim*}.go` and
  `internal/az/browser_target.go`.
- The commands stock T3 Code calls (`internal/az/{repos,pr_writes,write,workitems,boards_write,teams}.go`)
  and the program-name dispatch in `main.go`. T3's invocation of `az` is the compatibility
  contract; see `docs/internals/t3-compatibility.md`.
- The reference pins in `tools/syncrepos/main.go`. The `t3code` pin is the T3 revision the
  compatibility contract is checked against.
