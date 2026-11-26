package az

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const desktopHandlerExample = `route handler-export "$env:LOCALAPPDATA\TFSaz Links" "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe"`

func exportDesktopLinkHandler(ctx context.Context, dest, browserExecutable string) error {
	executable, config, err := linkHandlerPaths(browserExecutable)
	if err != nil {
		return err
	}
	command, err := windowsHandlerCommand(executable, config, browserExecutable)
	if err != nil {
		return err
	}
	if dest == "" {
		return errors.New("specify a new link handler package directory")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(dest, 0755); err != nil {
		return fmt.Errorf("create link handler package: %w", err)
	}
	var written []string
	finished := false
	defer func() {
		if !finished {
			for _, path := range written {
				_ = os.Remove(path)
			}
			_ = os.Remove(dest)
		}
	}()

	// Each package owns its own registration. Re-exporting for another
	// browser or config cannot overwrite the entry selected in Settings.
	var nonce [12]byte
	rand.Read(nonce[:])
	id := "TfsazLinks." + hex.EncodeToString(nonce[:])
	for _, script := range []struct {
		filename string
		remove   bool
	}{{"Install.ps1", false}, {"Uninstall.ps1", true}} {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dest, script.filename)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return fmt.Errorf("create link handler script: %w", err)
		}
		written = append(written, path)
		_, err = file.WriteString(windowsHandlerScript(id, command, script.remove))
		closeErr := file.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return fmt.Errorf("write link handler script: %w", err)
		}
	}
	finished = true
	return nil
}

func windowsHandlerCommand(executable, config, browser string) (string, error) {
	for _, path := range []string{executable, config, browser} {
		// The shell expands percent tokens in association commands, including
		// substitutions such as %1 and %*. A literal path may not contain them.
		// A file path also cannot end in a separator, which would escape its
		// closing quote under the Windows command line rules.
		if !absoluteLinkPath(path) ||
			strings.ContainsAny(path, "%\"") || strings.HasSuffix(path, `\`) || strings.HasSuffix(path, "/") {
			return "", errors.New("handler executable, config and browser must be absolute file paths without percent signs or quotes")
		}
	}
	return fmt.Sprintf(`"%s" route handler-open "%s" "%s" "%%1"`, executable, config, browser), nil
}

func windowsHandlerScript(id, command string, remove bool) string {
	action := "Install"
	if remove {
		action = "Uninstall"
	}
	// Keep paths out of PowerShell syntax, including its Unicode delimiters.
	return fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$action = '%s'
$id = '%s'
$command = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s'))
`, action, id, base64.StdEncoding.EncodeToString([]byte(command))) + windowsHandlerRegistryScript
}

const windowsHandlerRegistryScript = `
Add-Type -Namespace TfsazLinks -Name Shell -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("shell32.dll")]
public static extern void SHChangeNotify(int eventId, uint flags, System.IntPtr item1, System.IntPtr item2);
'@

$root = [Microsoft.Win32.Registry]::CurrentUser
$class = "Software\Classes\$id"
$app = "Software\Toolspell\$id"
$capabilities = "$app\Capabilities"
$label = 'TFSaz Links'
$entries = @(
    @{ Path = $class; Name = ''; Value = $label }
    @{ Path = $class; Name = 'URL Protocol'; Value = '' }
    @{ Path = "$class\Application"; Name = 'ApplicationName'; Value = $label }
    @{ Path = "$class\shell\open\command"; Name = ''; Value = $command }
    @{ Path = $capabilities; Name = 'ApplicationName'; Value = $label }
    @{ Path = $capabilities; Name = 'ApplicationDescription'; Value = 'Open registered Azure DevOps links through TFSaz' }
    @{ Path = "$capabilities\URLAssociations"; Name = 'http'; Value = $id }
    @{ Path = "$capabilities\URLAssociations"; Name = 'https'; Value = $id }
    @{ Path = 'Software\RegisteredApplications'; Name = $label; Value = $capabilities }
)
$privateKeys = @("$capabilities\URLAssociations", $capabilities, $app, "$class\shell\open\command", "$class\shell\open", "$class\shell", "$class\Application", $class)

function Test-ExpectedValue($key, $entry) {
    return ($key.GetValueKind($entry.Name) -eq [Microsoft.Win32.RegistryValueKind]::String -and
        $key.GetValue($entry.Name) -ceq $entry.Value)
}

function Remove-RegistrationValues($items) {
    foreach ($entry in $items) {
        $key = $root.OpenSubKey($entry.Path, $true)
        if ($null -eq $key) { continue }
        try {
            if ($key.GetValueNames() -notcontains $entry.Name) { continue }
            if (Test-ExpectedValue $key $entry) {
                $key.DeleteValue($entry.Name, $false)
            } else {
                Write-Warning "Preserved changed registry value at HKCU\$($entry.Path) ($($entry.Name))."
            }
        } finally { $key.Dispose() }
    }
    foreach ($path in $privateKeys) {
        $key = $root.OpenSubKey($path)
        if ($null -eq $key) { continue }
        try {
            $empty = $key.ValueCount -eq 0 -and $key.SubKeyCount -eq 0
        } finally { $key.Dispose() }
        if ($empty) { $root.DeleteSubKey($path, $false) }
    }
}

function Install-Registration {
    if (Test-Path -LiteralPath "Registry::HKEY_LOCAL_MACHINE\$class") {
        throw "A machine registration already uses $id."
    }
    $added = [System.Collections.Generic.List[object]]::new()
    try {
        foreach ($entry in $entries) {
            $key = $root.CreateSubKey($entry.Path)
            try {
                if ($key.GetValueNames() -contains $entry.Name) {
                    if (-not (Test-ExpectedValue $key $entry)) {
                        throw "Registry value already exists at HKCU\$($entry.Path) ($($entry.Name))."
                    }
                } else {
                    $added.Add($entry)
                    $key.SetValue($entry.Name, $entry.Value, [Microsoft.Win32.RegistryValueKind]::String)
                }
            } finally { $key.Dispose() }
        }
    } catch {
        Remove-RegistrationValues $added
        throw
    }
    Write-Host 'Registered TFSaz Links. In Settings > Apps > Default apps, choose it for HTTP and HTTPS.'
}

# Serialize install and uninstall of this application for the same account.
$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$mutex = [Threading.Mutex]::new($false, "Global\Toolspell.Tfsaz.Links.$sid")
try {
    try {
        $locked = $mutex.WaitOne([TimeSpan]::FromSeconds(10))
    } catch [Threading.AbandonedMutexException] {
        $locked = $true
    }
    if (-not $locked) { throw 'Another TFSaz Links registration is in progress.' }
    try {
        if ($action -eq 'Install') {
            Install-Registration
        } else {
            Remove-RegistrationValues $entries
            Write-Host 'Removed this package registration for TFSaz Links.'
        }
        [TfsazLinks.Shell]::SHChangeNotify(0x08000000, 0, [IntPtr]::Zero, [IntPtr]::Zero)
    } finally { $mutex.ReleaseMutex() }
} finally { $mutex.Dispose() }
`
