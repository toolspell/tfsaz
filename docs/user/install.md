# Install TFSaz

Install TFSaz on the machine running the T3 backend, whether you use the installed desktop app or connect to a remote T3 server. You do not need a T3 source checkout. For the shortest path, see [Getting started](getting-started.md).

## Build and install the shim

Build with Go 1.26.11 or newer, or use an installed TFSaz executable. The minimum is a patch release because Go ships standard library security fixes in patch releases.

**macOS and Linux.** From the TFSaz source directory, build TFSaz, create the two shims and enable them in the current shell.

```sh
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/tfsaz" .
"$HOME/.local/bin/tfsaz" shim install --dir "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"
```

**Windows.** Run these commands from PowerShell in the TFSaz source directory.

```powershell
$bin = Join-Path $env:LOCALAPPDATA 'tfsaz\bin'
New-Item -ItemType Directory -Force $bin | Out-Null
go build -o "$bin\tfsaz.exe" .
& "$bin\tfsaz.exe" shim install --dir $bin
$env:Path = "$bin;$env:Path"
```

The shim installer creates `az` and `git-remote-tfsaz` and refuses to overwrite a different executable. On Windows it falls back to executable copies when symbolic links are unavailable. `tfsaz shim uninstall --dir DIR` removes them again.

## Put the shim on T3's PATH

Both examples set PATH for the current shell only. Put the same directory first on the PATH used by T3 Desktop, the remote backend service or the job that launches Git. Fully quit and reopen the app or restart the service after changing its environment.

`tfsaz doctor` checks that the `az` and `git-remote-tfsaz` selected on its own PATH match the running TFSaz binary. If you also use Microsoft's Azure CLI, keep its installation in a separate directory and choose PATH order per process.

## Upgrade

After upgrading TFSaz, delete the two old **copies** from your dedicated shim directory and rerun `shim install`. `shim uninstall` leaves them in place, because they no longer match the new binary; it names each one it keeps. If you use managed browser routing and the upgrade moved or removed the installed binary, rerun `tfsaz route setup` as described in [Browser routing](browser-routing.md#managed-browser-routing).

## Uninstall

Undo the parts you set up, in this order, then remove TFSaz itself. Run each command with the configuration it was set up with.

| Step | What to do |
| --- | --- |
| 1. Browser routing | `tfsaz route restore`; the last one also removes `~/.tfsaz/route-recovery`. See [Refresh or remove](browser-routing.md#refresh-or-remove). |
| 2. Link handler and extension | Choose your usual default browser and remove the handler as described in [Desktop link handler](desktop-link-handler.md). Remove the Chromium extension from the browser profile and delete its export directory. |
| 3. Checkouts | `tfsaz repo restore PATH` for each entry in `tfsaz repo list`, or `tfsaz repo forget PATH` for one whose clone you deleted. |
| 4. Profiles and PATs | `tfsaz profile remove --profile ID` for each profile in `tfsaz profile list`. Then remove each entry that `profile list` still shows under "Keyring entries that no profile in this configuration uses", as described in [Find and remove leftover keyring entries](configure.md#find-and-remove-leftover-keyring-entries). The last removal also deletes `~/.tfsaz/keyring-entries.json` and, once empty, `~/.tfsaz`. |
| 5. Configuration | Delete the configuration file and the [files beside it](configure.md#files-beside-the-configuration). For the default location, delete the `tfsaz` folder under your OS user configuration directory (see [Configure](configure.md#configuration-file)). |
| 6. Shims and binary | `tfsaz shim uninstall --dir DIR` for the shim directory, then delete `tfsaz` (`tfsaz.exe` on Windows) and remove the directory from the PATH of T3 and your shell. |

`shim uninstall` removes only links to the running TFSaz and, on Windows, copies of it. It names every other file it leaves in place and then fails; delete such a file yourself if it is a copy from an older TFSaz. Check the result with `command -v az` on macOS and Linux or `Get-Command az` in PowerShell: it should find Microsoft's Azure CLI or nothing.

Next: [configure your collections and PATs](configure.md).
