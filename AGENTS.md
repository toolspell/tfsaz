# tfsaz

Build a standalone Go CLI for macOS, Windows and Linux that lets an unmodified T3 Code use Azure DevOps Server. Ship it under its own name. Treat T3's invocation of `az` as the compatibility contract. The initial backend is the on-premises REST API directly. Azure cloud sign-in and extension installation are outside the product scope.

## References

- Use the sibling `../t3code` checkout as the source and documentation reference. The initial investigation used commit `c478fde9fe`. Check the actual adapter and its tests when implementing a command.
- Start with `packages/source-control-azure-devops/src/shared/definition.ts` for remote and PR URL recognition, `src/server/AzureDevOpsCli.ts` in that package for argument rewriting, and `src/server/AzureDevOpsPullRequestCli.ts` and `AzureDevOpsPullRequestProvider.ts` for the JSON and exit behavior T3 consumes. The source-control user guide is `docs/user/source-control.md`.
- Local sibling checkouts `../azure-cli` and `../azure-devops-cli-extension` are behavioral references. The extension's `azure-devops/azext_devops/dev/common/identities.py` illustrates the collection connection-data lookup.
- Keep changes for this product in this checkout.

## Authentication and routing

- Keep an ordered list of collection profiles. Each has a stable ID, an Azure-style organization alias, and its full, real HTTPS collection URL. Preserve collection path prefixes, ports, and names containing spaces. Treat LAN and Tailscale addresses as aliases for a profile only when explicitly configured. Support the OS trust store and a profile-specific CA for private TLS.
- Use `github.com/zalando/go-keyring` for PAT storage under a service name and stable profile ID. Keep URLs, aliases and profile order in nonsecret configuration. Provide secret input by hidden prompt or stdin. Keep PATs out of command arguments, logs, fixtures and configuration files. Report a locked or unavailable credential store to the caller.
- The global discovery probe reports the first verified PAT user's login in configured order. Exit nonzero if none can be verified. For repository commands or viewer lookups in a known checkout, select the requested collection and use only its PAT. Preserve the distinction between rejected credentials and an unreachable server in diagnostics. Read the authenticated identity from the collection itself.
- Map the recognized Azure-style organization to its real collection before sending credentials. Send a PAT only to the configured collection and guard redirects accordingly. Return PR web links under the same Azure-style repository identity that T3 used to select the checkout.

## First proof

1. Match T3's `az --version`, `az account show --query user.name -o tsv`, and JSON `az account show --query user` probes. Cover T3's explicit organization flags as well as checkout-derived scope.
2. Implement `az repos pr show` and paginated `az repos pr policy list`. Add repository or list reads that the real flow calls. Match the adapter's output shapes and errors using fixtures stripped of identities, internal URLs and secrets.
3. On the testbed, verify two collections with different users, including a collection URL without a virtual directory. Prove that a failed credential in one cannot select the other's identity. Observe a real policy transition on the same linked PR in stock T3 without an agent issuing a refresh command. T3 groups some viewer reads by host, so verify “my PRs” with distinct recognized host aliases before choosing a permanent alias scheme.
4. Separate offline fixture results from live results. When a test needs a PAT, request it through T3's secret input. Use a disposable test credential for revocation. Later features such as diffs and work items should follow the actual CLI calls and API versions supported by the target server.

## Agent workflow

Use Codex Astra at standard speed for architecture, ambiguous behavior and integration decisions. Use Codex Sol for bounded implementation and tests. Start with low effort for mechanical work or medium for debugging. A limited trial of Sol Fast is welcome. Keep Astra on the default tier and Opus Fast off.

Use Claude Code Opus through the T3 bridge for focused review of meaningful code changes. Medium effort is a reasonable default. Give it the diff, the behavior under review, and the verification already performed. Batch related work so reviews and speculative parallel agents do not spend either subscription on duplicate work.
