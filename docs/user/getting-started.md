# Getting started

Run these steps on the machine running the T3 backend: the machine with T3 Desktop, or the T3 server if you use the web client. You need Go 1.26 or newer, the tfsaz source directory, an existing clone from your Azure DevOps Server collection, and a PAT for that collection.

## When you need tfsaz

You need tfsaz when your repositories live on an on-premises Azure DevOps Server and you want T3's pull request and work item features for them. Stock T3 only recognizes Azure DevOps Services (cloud) remotes and runs Microsoft's Azure CLI against the cloud. For repositories in Azure DevOps Services, use Microsoft's Azure CLI with the `azure-devops` extension instead.

What changes for you:

| Area | With tfsaz |
| --- | --- |
| `az` on the T3 backend | `az` is tfsaz. If you also use Microsoft's Azure CLI, keep it in a separate directory and choose PATH order per process. See [Install](install.md#put-the-shim-on-t3s-path). |
| Sign-in | A PAT per collection in the OS keyring instead of `az login`. If T3 asks you to run `az login` or `az devops login`, run `tfsaz devops login --organization URL`. See [Configure](configure.md). |
| Your checkout's `origin` | `repo setup` replaces it with an Azure-style URL on a generated `tfsaz-<24 hex>.visualstudio.com` host, which T3 recognizes. Git still reaches the real server over the original HTTPS or SSH transport. `repo restore` brings back the original URL. See [Set up a checkout](checkout-setup.md). |
| Links T3 opens | Links use the Azure-style host. Without [routing](routing.md), public DNS resolves that host to Azure, not to your server. |

## Quickstart

### macOS and Linux

From the tfsaz source directory:

```sh
mkdir -p "$HOME/.local/bin"
go build -o "$HOME/.local/bin/tfsaz" .
"$HOME/.local/bin/tfsaz" shim install --dir "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"
tfsaz init --collection 'https://devops.example.invalid/DefaultCollection'
tfsaz repo setup /path/to/checkout
tfsaz doctor /path/to/checkout
```

### Windows

From PowerShell in the tfsaz source directory:

```powershell
$bin = Join-Path $env:LOCALAPPDATA 'tfsaz\bin'
New-Item -ItemType Directory -Force $bin | Out-Null
go build -o "$bin\tfsaz.exe" .
& "$bin\tfsaz.exe" shim install --dir $bin
$env:Path = "$bin;$env:Path"
tfsaz init --collection 'https://devops.example.invalid/DefaultCollection'
tfsaz repo setup C:\path\to\checkout
tfsaz doctor C:\path\to\checkout
```

### Then, on every OS

`init` asks for the PAT at a hidden prompt. The `PATH` change above applies to the current shell only. Put the same directory first on the PATH of T3 Desktop or the T3 server and restart it. Then open the checkout in T3 and view an existing PR.

[Install](install.md) covers PATH and upgrades. [Configure](configure.md) covers PATs and multiple collections.

## Choose the setup for your environment

| Goal | Entry point |
| --- | --- |
| Connect an installed T3 app | Use `tfsaz shim install --dir DIR`, put DIR on the backend's PATH, and run `tfsaz init --collection URL`. See [Install](install.md) and [Configure](configure.md). |
| Use an existing clone | Run `tfsaz repo setup PATH`. New setups keep the original SSH or HTTPS transport. Use `tfsaz repo verify PATH` to check it and `tfsaz repo restore PATH` to recover the original URL offline. See [Set up a checkout](checkout-setup.md). |
| Clone a synthetic HTTPS URL in a shell or job | Start Git under `tfsaz route run --` and configure Git credentials for the native collection. This also works when no checkout exists yet. See [Route synthetic HTTPS URLs](routing.md). |
| Open managed links in a browser | Run `tfsaz route setup` and `tfsaz route verify` on the host running the browser. See [Browser routing](browser-routing.md). |

If `tfsaz route redirect` cannot bind port 443, use `--port` with a loopback port redirect as described in [Run the service on port 443](browser-routing-manual.md#5-run-the-service-on-port-443).
