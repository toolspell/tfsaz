# Architecture

TFSaz is a standalone Go CLI that lets an unmodified T3 Code use Azure DevOps Server. T3's invocation of `az` is the compatibility contract; see [T3 compatibility](t3-compatibility.md). The backend is the on-premises REST API, called directly; how TFSaz resolves its routes and which API version it targets is in [REST API](rest-api.md).

## Authentication and routing rules

These rules hold for every command. Change them only deliberately, and update [SECURITY.md](../../SECURITY.md), which lists the resulting boundaries, in the same change.

### Profiles

- Keep an ordered list of collection profiles. Each has a stable ID, an Azure-style alias, and its full, real HTTPS collection URL.
- Preserve collection path prefixes, ports, and names containing spaces.
- Treat LAN and Tailscale addresses as aliases for a profile only when explicitly configured.
- Support the OS trust store and a profile-specific CA for private TLS.

### PAT storage

- Use `github.com/zalando/go-keyring` for PAT storage under a service name and stable profile ID.
- Keep URLs, aliases and profile order in nonsecret configuration.
- Provide secret input by hidden prompt or stdin.
- Keep PATs out of command arguments, logs, fixtures and configuration files.
- Report a locked or unavailable credential store to the caller.

### Identity and collection selection

- The global discovery probe reports the first verified PAT user's login in configured order. Exit nonzero if none can be verified.
- For repository commands or viewer lookups in a known checkout, select the requested collection and use only its PAT. A failed credential in one collection must never select another collection's identity.
- Preserve the distinction between rejected credentials and an unreachable server in diagnostics.
- Read the authenticated identity from the collection itself.

### Sending credentials

- Map the recognized Azure-style alias to its real collection before sending credentials.
- Send a PAT only to the configured collection and guard redirects accordingly: they fail closed.
- Return PR web links under the same alias host that T3 used to select the checkout.

## Design decisions

### Azure-style aliases instead of native URLs

Stock T3 enables its Azure provider only for a remote on `dev.azure.com` or `*.visualstudio.com`. The server's native HTTPS or SSH URL alone does not enable it. TFSaz therefore gives T3 an Azure-style alias and keeps the native URL for everything that talks to the server.

- T3 groups some viewer reads by host. Several collections need distinct recognized hostnames, or "my PRs" mixes them up.
- Each project/repository pair gets its own stable hostname. A repository-bound alias accepts only paths of that project and repository.
- Every code path that sends a credential or opens a connection must map the alias to the real collection first. Without a local hosts entry, public DNS resolves the alias hosts to Azure.

### Git keeps the native transport

A managed checkout keeps the synthetic origin for T3 and sets `remote.origin.vcs=tfsaz`, so Git runs `git-remote-tfsaz`. The helper reads the bound native target from the checkout's common Git configuration, not from the TFSaz config.

- Plain Git must keep working with T3 closed and `TFSAZ_CONFIG` unset. Do not make the helper depend on the TFSaz config or the keyring.
- Storing the target in the common Git directory makes linked worktrees, including ones created later by Git or T3, share one mapping.
- Git authenticates with its own HTTPS or SSH credentials. Git does not receive the profile's PAT.

### Credential service per collection

New profiles (`credentialVersion: 2`) store their PAT under a keyring service derived from a hash of the collection URL, because a profile ID alone may be reused in another config file. Older profiles use the plain `tfsaz` service. Changing a version 2 profile's `collectionUrl` therefore orphans its stored PAT.

### Routing compared with a proxy

Browser routing redirects to the real collection instead of proxying it. The [user guide](../user/browser-routing.md) describes how to set it up.

| Approach | Tradeoff |
| --- | --- |
| Redirect (this service) | The browser ends up on the real origin, so its address bar, cookies, sign-in and same-origin checks all belong to the real server. Needs a hosts entry, a running service, port 443 and local trust for one leaf. |
| Reverse proxy | Would serve the collection under the synthetic origin. TFSaz would have to carry session cookies or credentials, rewrite absolute links and reconcile the server's origin checks. TFSaz deliberately does not do this. |
| Desktop link handler | Needs no certificate or service but only sees links the OS hands off, not typed URLs or in-browser clicks. |
| Chromium extension | Per profile and Chromium only, with no TLS involved. Covers in-browser clicks in that profile. |
| PAC / SOCKS refusal | Blocks registered names for clients that consult the proxy setting but does not redirect them. |

The redirector rereads the config and repo registry on every request, so a restored setup stops redirecting at once without a listener restart.

### System edits must be recoverable

`route setup`, `route restore`, `repo setup` and `repo restore` change state outside TFSaz (hosts file, trust stores, services, Git config). Each is journaled or keeps a recovery record, and rerunning the same command after an interruption finishes or undoes it. The Windows hosts file is edited in place: an earlier installer replaced the file and changed its NTFS file ID, so the writer now keeps the original file, its security descriptor and file ID.
