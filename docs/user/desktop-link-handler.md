# Desktop link handler

Run `tfsaz route handler-export DESTINATION BROWSER` on the desktop where links open. The handler starts on demand, translates registered Azure web links, and passes ordinary HTTP and HTTPS links through to your chosen browser. Unsupported paths on registered aliases are refused. Browser sign-in takes place at the real collection. Use this option for links an app hands to the OS default browser. It requires no extension, background service or administrator access. For a raw alias opened inside T3’s Browser panel or typed into a browser, see [managed browser routing](browser-routing.md#managed-browser-routing).

Export using a permanent tfsaz installation. Choose a new export destination. The handler captures the paths to tfsaz and its config file and reads that config and the repo registry on each click. Restore your usual default browser before removing the handler. If you move tfsaz or its config, or choose a different target browser, export a new handler.

## macOS

```sh
mkdir -p "$HOME/Applications"
tfsaz route handler-export "$HOME/Applications/tfsaz Links.app" com.apple.Safari
```

For Firefox, use `org.mozilla.firefox` as the browser bundle ID. Try the handler with `open -a "$HOME/Applications/tfsaz Links.app" 'YOUR_AZURE_PR_URL'`. Select **tfsaz Links** in **System Settings → Desktop & Dock → Default web browser**. To remove it, select your usual browser and delete the exported app. The earlier `route mac-handler-export` command remains supported.

## Windows 11

From PowerShell, export a package for your installed browser and run its per-user installer. This example uses Edge. Windows expands percent signs in protocol commands, so choose tfsaz, browser and config paths without a literal `%`.

```powershell
$handler = Join-Path $env:LOCALAPPDATA 'tfsaz Links'
$browser = Join-Path ${env:ProgramFiles(x86)} 'Microsoft\Edge\Application\msedge.exe'
tfsaz route handler-export $handler $browser
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$handler\Install.ps1"
```

In **Settings → Apps → Default apps**, select **tfsaz Links** for **HTTP** and **HTTPS**. Windows requires the user to choose the defaults. To remove the handler, first select your usual browser for both protocols, then run `powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$handler\Uninstall.ps1"` and delete the exported directory. Uninstall the old registration before installing a replacement package. [Windows default apps](https://learn.microsoft.com/en-us/windows/apps/develop/windows-integration/default-apps-platform)

## Ubuntu desktop

Install the desktop entry under your user's applications directory. Supply the absolute path to a browser executable or launcher such as Firefox.

```sh
apps="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
mkdir -p "$apps"
tfsaz route handler-export "$apps/com.toolspell.tfsaz.links.desktop" "$(command -v firefox)"
update-desktop-database "$apps"
```

Try it first with `gio launch "$apps/com.toolspell.tfsaz.links.desktop" 'YOUR_AZURE_PR_URL'`. Record the current HTTP and HTTPS defaults, then select **tfsaz Links** for both.

```sh
xdg-mime query default x-scheme-handler/http
xdg-mime query default x-scheme-handler/https
gio mime x-scheme-handler/http com.toolspell.tfsaz.links.desktop
gio mime x-scheme-handler/https com.toolspell.tfsaz.links.desktop
```

To remove it, restore each recorded desktop ID with `gio mime x-scheme-handler/SCHEME PREVIOUS_ID`, or choose your browser in GNOME's **Default Apps**. Then remove the exported desktop file. [freedesktop default application associations](https://specifications.freedesktop.org/mime-apps/latest/default)
