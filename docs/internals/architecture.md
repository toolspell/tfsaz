# Architecture

## Authentication and routing

- Keep an ordered list of collection profiles. Each has a stable ID, an Azure-style organization alias, and its full, real HTTPS collection URL. Preserve collection path prefixes, ports, and names containing spaces. Treat LAN and Tailscale addresses as aliases for a profile only when explicitly configured. Support the OS trust store and a profile-specific CA for private TLS.
- Use `github.com/zalando/go-keyring` for PAT storage under a service name and stable profile ID. Keep URLs, aliases and profile order in nonsecret configuration. Provide secret input by hidden prompt or stdin. Keep PATs out of command arguments, logs, fixtures and configuration files. Report a locked or unavailable credential store to the caller.
- The global discovery probe reports the first verified PAT user's login in configured order. Exit nonzero if none can be verified. For repository commands or viewer lookups in a known checkout, select the requested collection and use only its PAT. Preserve the distinction between rejected credentials and an unreachable server in diagnostics. Read the authenticated identity from the collection itself.
- Map the recognized Azure-style organization to its real collection before sending credentials. Send a PAT only to the configured collection and guard redirects accordingly. Return PR web links under the same Azure-style repository identity that T3 used to select the checkout.

## Routing compared with a proxy

| Approach | Tradeoff |
| --- | --- |
| Redirect (this service) | The browser ends up on the real origin, so its address bar, cookies, sign-in and same-origin checks all belong to the real server. Needs a hosts entry, a running service, port 443 and local trust for one leaf. |
| Reverse proxy | Would serve the collection under the synthetic origin. tfsaz would have to carry session cookies or credentials, rewrite absolute links and reconcile the server's origin checks. tfsaz deliberately does not do this. |
| Desktop link handler | Needs no certificate or service but only sees links the OS hands off, not typed URLs or in-browser clicks. |
| Chromium extension | Per profile and Chromium only, with no TLS involved. Covers in-browser clicks in that profile. |
| PAC / SOCKS refusal | Blocks registered names for clients that consult the proxy setting but does not redirect them. |
