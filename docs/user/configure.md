# Configure collections and PATs

TFSaz keeps an ordered list of collection profiles. The nonsecret configuration holds URLs, aliases and profile order. PATs live in the OS keyring.

## Create a profile

```sh
tfsaz init --collection 'https://devops.example.invalid/DefaultCollection' --id engineering
tfsaz profile list
```

- `init` verifies the PAT before saving it. It generates a stable Azure-style alias from the collection URL when you omit `--organization`.
- On a terminal it can prompt for the collection URL and hides PAT input. For a job, pass the URL as an argument and pipe the PAT on stdin from a secret provider.
- `--no-login` writes only the nonsecret configuration and works without access to the OS keyring.
- Add another collection with `tfsaz profile add --collection URL`. `profile list` prints profiles in discovery order and whether each has a stored PAT.
- Use `--ca-file PATH` for a private CA.
- For multiple collections, use distinct `*.visualstudio.com` hostnames so T3’s viewer lookup can distinguish them.

Use **Settings → Source control & issues → Rescan** to confirm discovery in the installed T3 app.

## Configuration file

Alternatively, create `tfsaz/config.json` under your OS user configuration directory, or set `TFSAZ_CONFIG` to the absolute path of your configuration file for both manual TFSaz runs and T3. For T3 Desktop launched from Finder, the default location avoids relying on a custom variable reaching its backend.

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
- Optional aliases are full collection URLs or Azure-style aliases that select the same profile.
- Every authenticated request uses the profile's `collectionUrl`. Redirects fail closed.
- TLS uses system trust plus the optional PEM CA file, whose path can be relative to the configuration file.

## Store, replace or remove a PAT

Interactive `init` already stores the PAT. For a hand-written profile, a profile created with `--no-login`, or a replacement PAT, run login as the OS user that runs T3. This example uses the organization in the JSON above.

```sh
tfsaz devops login --organization https://engineering-example.visualstudio.com
```

Enter the PAT at the hidden prompt, or pipe one line from a secret provider on stdin. Login verifies the PAT against the collection before saving it in the OS keyring under the stable profile ID. The backend is Keychain on macOS, Credential Manager on Windows, and Secret Service on Linux.

New profiles use `credentialVersion: 2`, which scopes the keyring service to the collection URL (`tfsaz/v2/` plus a hash of it). Older profiles without this field use the shared `tfsaz` service; the next login or logout moves such a profile to `credentialVersion: 2` and leaves the shared entry in place (see [leftover keyring entries](#find-and-remove-leftover-keyring-entries)). Preserve the existing credential version when editing a profile so it can still find its saved PAT. For the same reason, run `devops login` again after changing the `collectionUrl` of a version 2 profile.

`tfsaz profile logout --profile ID` removes the selected local credential and keeps the profile configured. Server-side token revocation is separate.

### Remove a profile

1. Run `tfsaz repo restore PATH` for each checkout of that collection in `tfsaz repo list`. `profile remove` refuses while a managed checkout uses the profile.
2. Run `tfsaz profile remove --profile ID`. It deletes the profile's PAT from the keyring and the profile from the configuration file.

### Find and remove leftover keyring entries

TFSaz records the name of every keyring entry it writes (service and profile ID, never the PAT) in `~/.tfsaz/keyring-entries.json`, outside the configuration directory, so the record survives deleting the configuration. It drops a name when it deletes the entry. When no name is left, it deletes the file, and `~/.tfsaz` once that is empty.

If you change a profile's ID, `collectionUrl` or `credentialVersion`, or delete the configuration, without `profile logout` first, its PAT stays in the keyring. `tfsaz profile list` shows such entries below the profiles, with their service:

```text
Keyring entries that no profile in this configuration uses:
engineering	tfsaz/v2/<hash>	PAT stored
```

Another configuration (another `TFSAZ_CONFIG`) may still use an entry in this list. Remove one you no longer need:

```sh
tfsaz profile remove --profile engineering --service 'tfsaz/v2/<hash>'
```

| Entry | Remove with |
| --- | --- |
| Listed by `profile list` | `--profile ID --service SERVICE`, as printed. |
| Written before TFSaz recorded its entries | `--profile ID --collection URL`, with the collection URL the PAT was saved for. TFSaz computes the service from it. |
| The legacy `tfsaz` service of an older profile | `--profile ID` and `--service` set to `tfsaz`. Several configurations can share this entry, so `profile logout`, `profile remove` and the login that moves a profile to `credentialVersion: 2` keep it, list it and print the command that deletes it. |

These forms refuse an entry that a profile in the current configuration still uses; use `profile logout` or `profile remove --profile ID` for that. `tfsaz doctor` checks each profile's PAT against its collection.

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
