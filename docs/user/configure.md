# Configure collections and PATs

tfsaz keeps an ordered list of collection profiles. The nonsecret configuration holds URLs, aliases and profile order. PATs live in the OS keyring.

## Create a profile

```sh
tfsaz init --collection 'https://devops.example.invalid/DefaultCollection' --id engineering
tfsaz profile list
```

- `init` verifies the PAT before saving it. It generates a stable Azure-style alias from the collection URL when you omit `--organization`.
- On a terminal it can prompt for the collection URL and hides PAT input. For a job, pass the URL as an argument and pipe the PAT on stdin from a secret provider.
- `--no-login` writes only the nonsecret configuration and works without access to the OS keyring.
- Add another collection with `tfsaz profile add --collection URL`. `profile list` prints profiles in discovery order.
- Use `--ca-file PATH` for a private CA.
- For multiple collections, use distinct `*.visualstudio.com` hostnames so T3’s viewer lookup can distinguish them.

Use **Settings → Source control & issues → Rescan** to confirm discovery in the installed T3 app.

## Configuration file

Alternatively, create `tfsaz/config.json` under your OS user configuration directory, or set `TFSAZ_CONFIG` to the absolute path of your configuration file for both the CLI and T3. For T3 Desktop launched from Finder, the default location avoids relying on a custom variable reaching its backend.

| OS | Default configuration directory |
| --- | --- |
| macOS | `~/Library/Application Support` |
| Windows | `%AppData%` |
| Linux | `$XDG_CONFIG_HOME` or `~/.config` |

```json
{
  "profiles": [
    {
      "id": "engineering",
      "credentialVersion": 2,
      "organization": "https://engineering-example.visualstudio.com",
      "collectionUrl": "https://devops.example.invalid/DefaultCollection"
    },
    {
      "id": "platform",
      "credentialVersion": 2,
      "organization": "https://platform-example.visualstudio.com",
      "collectionUrl": "https://server.example.invalid:8443/tfs/Second Collection",
      "aliases": ["https://vpn.example.invalid/tfs/Second Collection"],
      "caFile": "private-ca.pem"
    }
  ]
}
```

- Give each collection a unique, stable ID.
- Use its full collection URL, including any port and virtual directory.
- Optional aliases are full collection or Azure-style organization URLs that select the same profile.
- Every authenticated request uses the profile's `collectionUrl`. Redirects fail closed.
- TLS uses system trust plus the optional PEM CA file, whose path can be relative to the configuration file.

## Store, replace or remove a PAT

Interactive `init` already stores the PAT. For a hand-written profile, a profile created with `--no-login`, or a replacement PAT, run login as the OS user that runs T3. This example uses the organization in the JSON above.

```sh
tfsaz devops login --organization https://engineering-example.visualstudio.com
```

Enter the PAT at the hidden prompt, or pipe one line from a secret provider on stdin. Login verifies the PAT against the collection before saving it in the OS keyring under the stable profile ID. The backend is Keychain on macOS, Credential Manager on Windows, and Secret Service on Linux.

New profiles use `credentialVersion: 2`, which scopes the keyring service to the collection URL. Older profiles without this field use the original `tfsaz` service. Preserve the existing credential version when editing a profile so it can still find its saved PAT. For the same reason, run `devops login` again after changing the `collectionUrl` of a version 2 profile.

`tfsaz profile logout --profile ID` removes the selected local credential and keeps the profile configured. Server-side token revocation is separate.

## PAT scopes

| Task | Required access |
| --- | --- |
| Read PRs | Code (Read) |
| Read work items | Work Items (Read) |
| Edit PRs or work items | Code (Read & write) or Work Items (Read & write), as appropriate |
| Create a repository | Code management scope and the project’s Create repository permission |
| List assignee candidates | Project and team read access |
| Resolve a login through the identities endpoint | Can also require identity read access |

Next: [set up a checkout](checkout-setup.md).
