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

### Files beside the configuration

These files and directories sit next to the configuration file, named after it. Two more live in `~/.tfsaz`, outside the configuration directory, so they survive deleting it: `keyring-entries.json` ([leftover keyring entries](#find-and-remove-leftover-keyring-entries)) and `route-recovery` ([browser routing](browser-routing.md#refresh-or-remove)).

| Name | What it holds | Written by |
| --- | --- | --- |
| `<config>.browser-route` | The browser-routing certificate, its private key and the installation identity | `route setup` |
| `<config>.cache` | The resource catalog of each collection, as the server sent it; no credentials | Commands that resolve a REST route through the catalog; `profile remove` deletes a collection's entry |
| `<config>.lock` | Serializes concurrent configuration edits | Commands that change the configuration or the keyring |
| `<config>.repo-setups.json` | The managed checkouts | `repo setup` |
| `<config>.repo-setups.json.lock` | Serializes concurrent checkout setup and restore | Commands that change the managed checkouts |
| `<config>.run` | Temporary Git helper directories of a running `route run` | `route run` |

## Store, replace or remove a PAT

Interactive `init` already stores the PAT. For a hand-written profile, a profile created with `--no-login`, or a replacement PAT, run login as the OS user that runs T3. This example uses the organization in the JSON above.

```sh
tfsaz devops login --organization https://engineering-example.visualstudio.com
```

Enter the PAT at the hidden prompt, or pipe one line from a secret provider on stdin. Login verifies the PAT against the collection before saving it in the OS keyring under the stable profile ID. If the collection offers no API 7.1, login still saves the PAT and prints a warning; see [Diagnose](diagnose.md#common-problems). The OS keyring must be available in that session; see [OS keyring requirements](#os-keyring-requirements).

New profiles use `credentialVersion: 2`, which scopes the keyring service to the collection URL (`tfsaz/v2/` plus a hash of it). Older profiles without this field use the shared `tfsaz` service; the next login or logout moves such a profile to `credentialVersion: 2` and leaves the shared entry in place (see [leftover keyring entries](#find-and-remove-leftover-keyring-entries)). Preserve the existing credential version when editing a profile so it can still find its saved PAT. For the same reason, run `devops login` again after changing the `collectionUrl` of a version 2 profile.

`tfsaz profile logout --profile ID` removes the selected local credential and keeps the profile configured. Server-side token revocation is separate.

### OS keyring requirements

Login saves the PAT in the keyring of the OS user who runs it, and the T3 backend reads it from there. Run both as the same OS user, in sessions where that user's keyring is available.

| OS | Keyring | What it needs |
| --- | --- | --- |
| macOS | Keychain | The login keychain, which macOS unlocks when you log in. Nothing to set up. |
| Windows | Credential Manager | A password-backed logon with the user profile loaded, such as an interactive desktop session. Credential Manager can reject writes from an SSH logon; run login from an interactive desktop session as that user instead. |
| Linux | Secret Service | A Secret Service provider, such as GNOME Keyring, on the session's D-Bus with an unlocked default collection. A desktop login may come without a default persistent collection; create one, for example in Passwords and Keys (Seahorse), and make it the default. A session without a desktop, such as SSH or a T3 server started by a service manager, needs a provider started and unlocked in that session. |

When the keyring is missing or locked:

- `init`, `devops login`, `profile logout` and `profile remove` stop with `credential store locked or unavailable for profile ID`. `devops login`, `profile logout` and `profile remove` leave the configuration and the keyring unchanged. For a new profile, `init` has already saved the profile configuration and says the login can be retried. Fix the session and run the same command again. `init --no-login` still writes the profile without touching the keyring.
- Commands T3 runs fail with the same message. If the keyring is reachable but holds no PAT for the profile, for example because login ran as another user or in another session's keyring, they report `profile ID has no PAT; use az devops login --organization URL`.
- `tfsaz doctor` run from a directory outside Git checks the PAT of every profile; in a managed checkout it checks only that checkout's profile. Run it in the same environment as T3. See [Diagnose](diagnose.md#run-doctor).

The setups tested so far are recorded in [Windows 11 and Ubuntu verification](../verification/live-t3-2026-11.md#windows-11-x64-and-ubuntu-2604-arm64).

### Remove a profile

1. Run `tfsaz repo restore PATH` for each checkout of that collection in `tfsaz repo list`, or `tfsaz repo forget PATH` for one whose clone you deleted. `profile remove` refuses while a managed checkout uses the profile.
2. Run `tfsaz profile remove --profile ID`. It deletes the profile's PAT from the keyring, the profile from the configuration file and its collection's catalog cache from `<config-file>.cache`.

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

These forms refuse an entry that a profile in the current configuration still uses; use `profile logout` or `profile remove --profile ID` for that. `tfsaz doctor` checks each profile's PAT and API version against its collection.

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
