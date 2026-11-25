# tfsaz

`tfsaz` lets stock T3 Code read pull requests from on-premises Azure DevOps Server. It runs as `az` on the T3 **server process's** PATH and talks directly to the configured collection over HTTPS. The CLI implements discovery, authenticated viewer, repository details, PR show/list, and paginated PR policy reads.

## Build and configure

Build with Go 1.26 or newer. On macOS or Linux run `go build -o tfsaz .` and place a symlink named `az` in a directory early on T3's PATH. On Windows build `tfsaz.exe` and place a link or copy named `az.exe` on that PATH. Restart the T3 server with the updated environment. Keep the normal Azure CLI on a separate PATH if you use both.

Create `tfsaz/config.json` under your OS user configuration directory, or set `TFSAZ_CONFIG` to the absolute path of your configuration file for both the CLI and T3. The default directories are `~/Library/Application Support` on macOS, `%AppData%` on Windows, and `$XDG_CONFIG_HOME` or `~/.config` on Linux.

```json
{
  "profiles": [
    {
      "id": "engineering",
      "organization": "https://engineering-example.visualstudio.com",
      "collectionUrl": "https://devops.example.invalid/DefaultCollection"
    },
    {
      "id": "platform",
      "organization": "https://dev.azure.com/platform-example",
      "collectionUrl": "https://server.example.invalid:8443/tfs/Second Collection",
      "aliases": ["https://vpn.example.invalid/tfs/Second Collection"],
      "caFile": "private-ca.pem"
    }
  ]
}
```

Give each collection a unique, stable ID. Use its full collection URL, including any port and virtual directory. Optional aliases are full collection or Azure-style organization URLs that select the same profile. Every authenticated request uses the profile's `collectionUrl`. Redirects fail closed. TLS uses system trust plus the optional PEM CA file, whose path can be relative to the configuration file.

Store the PAT once per profile, as the OS user that runs T3. Log in to the first example profile with the following command.

```sh
tfsaz devops login --organization https://engineering-example.visualstudio.com
```

Enter the PAT at the hidden prompt, or pipe one line from a secret provider on stdin. Login verifies the PAT against the collection before saving it in the OS keyring under service `tfsaz` and the profile ID. A rejected replacement preserves the previous credential. The PR reads require code read access. Resolving a login through the identities endpoint can also require identity read access.

## Checkout and commands

Give the checkout a Git remote T3 recognizes, such as `https://engineering-example.visualstudio.com/MyProject/_git/MyRepo`. An SSH remote such as `git@ssh.dev.azure.com:v3/platform-example/MyProject/MyRepo` selects the modern organization in the example. Arrange Git transport to the real server separately. PR web links retain the Azure-style identity T3 uses for matching.

| T3 command | Behavior |
| --- | --- |
| `az --version` | Prints the tfsaz version before configuration or authentication. |
| `az account show --query user.name -o tsv` | Prints the first verified login in profile order. Fails if none verify. |
| `az account show --query user -o json` | Reads the checkout's authenticated user from collection connection data. |
| `az repos show` | Returns repository details, including clone URLs and default branch. |
| `az repos pr show --id N` | Returns the PR with its Azure-style web link. |
| `az repos pr list` | Returns a JSON array with T3's repository, branch, status, creator/reviewer and paging filters. |
| `az repos pr policy list --id N --top 100 --skip 0` | Returns one array of raw evaluations. T3 advances the offset and computes the check state. |

An explicit `--organization` or `--org` selects the profile even in a checkout belonging to another collection. Repository and viewer commands fail if the selected credential fails. Outside a recognized checkout, supply the organization and any required project and repository. Diffs, work items and write operations are outside the current command set.

## Verification

Run `go test ./...` for the offline suite. It uses temporary Git repositories, synthetic HTTPS responses and an in-memory credential store.

The opt-in integration test builds the actual executable and runs T3's existing Azure adapter against it. It requires Git, Node with TypeScript support (tested with Node 26), a T3 source tree with its lockfile dependencies installed, and a usable OS keyring. It creates a random keyring entry for its loopback server and removes the entry at the end.

```sh
TFSAZ_T3_ROOT=/absolute/path/to/t3code go test -count=1 -run TestStockT3Adapter -v ./internal/az
```

At T3 revision `c478fde9fe`, the offline adapter test exercises viewer, default branch, authored PR list, link identity and two pages of policies. Two successive summary reads observe a queued fixture policy becoming approved. Automatic refresh in the stock UI, two distinct users in “my PRs,” and the target server's live responses still need testbed verification.
