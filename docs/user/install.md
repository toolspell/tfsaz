# Install tfsaz

Install tfsaz on the machine running the T3 backend, whether you use the installed desktop app or connect to a remote T3 server. A T3 source checkout is needed only for the optional stock-adapter integration test.

Build with Go 1.26 or newer, or use an installed tfsaz executable. From the tfsaz source directory on macOS or Ubuntu, build the CLI, install the two helper names and enable them in the current shell.

```sh
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/tfsaz" .
"$HOME/.local/bin/tfsaz" shim install --dir "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"
```

On Windows, run these commands from PowerShell in the tfsaz source directory.

```powershell
$bin = Join-Path $env:LOCALAPPDATA 'tfsaz\bin'
New-Item -ItemType Directory -Force $bin | Out-Null
go build -o "$bin\tfsaz.exe" .
& "$bin\tfsaz.exe" shim install --dir $bin
$env:Path = "$bin;$env:Path"
```

Both examples set PATH for the current shell. Put the same directory first on the PATH used by T3 Desktop, the remote backend service or the job that launches Git. Fully quit and reopen the app or restart the service after changing its environment. `tfsaz doctor` checks that the `az` selected on its own PATH matches the running tfsaz binary. If you also use Microsoft’s Azure CLI, keep its installation in a separate directory and choose PATH order per process.

The shim installer creates `az` and `git-remote-tfsaz` and refuses to overwrite a different executable. On Windows it falls back to executable copies when symbolic links are unavailable. After upgrading tfsaz, remove the two old **copies** from your dedicated shim directory and rerun `shim install`.
