# Set up a checkout for stock T3

Use an existing clone whose `origin` points to a configured Azure DevOps Server collection. Install the binary at its intended permanent path before running setup. Restore the checkout before moving its Git directory or the installed helper.

```sh
tfsaz repo setup /path/to/checkout
tfsaz repo show /path/to/checkout
tfsaz doctor /path/to/checkout
```

After `doctor` verifies the checkout and PAT, open the checkout in T3 and view an existing PR. To open its synthetic URLs inside the browser, set up [browser routing](browser-routing.md). For other clients, see [Route synthetic HTTPS URLs](routing.md).

## How the transport is preserved

**New setups keep the checkout’s existing Git transport on macOS, Windows and Linux.** T3 sees the synthetic origin. Git sees `remote.origin.vcs=tfsaz` and invokes `git-remote-tfsaz` for operations on that remote. The helper reads the bound native target from the checkout’s common Git configuration.

- An HTTPS origin continues using Git’s HTTPS credentials and trust settings.
- An SSH origin continues using the native SSH endpoint and Git’s SSH settings.
- Ordinary Git commands in an already-managed checkout need the helper on PATH and can run with T3 closed and `TFSAZ_CONFIG` unset.
- Clients using a separate Git implementation must support configured remote helpers.

## What setup changes and restore undoes

Setup writes only to the clone's shared Git configuration (used by all its worktrees) and to tfsaz's checkout registry, `<config-file>.repo-setups.json`.

| Mode | Git settings setup writes | Other files |
| --- | --- | --- |
| Both | `remote.origin.url` becomes the synthetic URL | A registry record |
| Preserved transport (default) | `remote.origin.vcs=tfsaz`, `remote.origin.tfsazBinding` (the native target) | None |
| Managed SSH | `core.sshCommand`, which runs tfsaz | `tfsaz-<hash>.ssh.conf` in the Git directory |

`tfsaz repo restore PATH` puts back the original URL, unsets the other settings, deletes the `.ssh.conf` file and removes the registry record. It leaves an empty `tfsaz-repo-setup.lock` in the Git directory, which you can delete. `repo show PATH` and `git config --get-regexp '^remote\.origin\.'` show the current state.

Restore needs the clone itself. If you deleted or moved a clone without restoring it, `repo list` keeps its record and `repo verify` keeps failing for it; tfsaz has no command to drop that record yet. If restore reports that the managed Git settings have drifted, compare `repo show PATH` with the table above and fix the settings by hand.

## Manage checkouts

- **Inspect or verify.** `tfsaz repo show PATH` displays the saved origin, native Git target, transport and profile offline. `repo verify PATH` also checks current configuration and live read access. Use `tfsaz repo list` for the registry and `repo verify` to check all managed checkouts. Setup refuses conflicting fetch/push destinations. Git credentials for the native collection are separate from the PAT used for CLI API calls.
- **Select the mode.** Use `--preserve-transport` to select the default per-remote helper explicitly. `--managed-ssh` selects the wrapper explicitly on all platforms, and `--ssh-url URL` selects it with a native SSH endpoint override for an HTTPS origin. The wrapper manages the checkout’s `core.sshCommand` and checks public-key access. Use `--profile ID` if the native SSH URL could identify more than one collection.
- **Restore or change modes.** Running setup again on an already-managed checkout retains its recorded mode and rejects a conflicting mode flag. To switch modes, restore the checkout and run setup for the desired mode. Both modes journal setup and restore and check for drift. Recover from interruption by rerunning the operation. Restore recovers the saved origin offline.
- **Linked worktrees.** Run setup once per clone from any of its worktrees. Repositories in the same collection reuse one profile and PAT. Each project/repository pair gets its own stable hostname, shared by clones using the same profile and collection. Existing worktrees and ones created later by Git or T3 share the origin mapping. Restore from any surviving worktree to return all of them to the original remote. If the worktree used for setup is removed or moved with Git, `repo list` and `repo verify` can locate another checkout registered under the same Git common directory.
- **Fresh clones.** A per-remote setting takes effect after the checkout exists. To clone directly from a synthetic HTTPS URL, use `tfsaz route run -- git clone URL`. You can also clone from the native server with plain Git, then run `repo setup`.

Before setup, confirm that a native `git ls-remote` works with the Git and SSH client you intend to use. On Windows, the live SSH tests passed with the OpenSSH bundled with Git for Windows ([record](../verification/live-t3-2026-11.md#windows-11-x64-and-ubuntu-2604-arm64)).

If something fails, see [Diagnose a CLI or checkout](diagnose.md).
