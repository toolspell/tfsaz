//go:build windows

package az

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Windows: a per-user Task Scheduler logon task runs the redirect service
// directly on loopback port 443 (Winsock needs no privilege for it), hidden
// through conhost --headless. The leaf goes into the user's Root store, which
// shows a confirmation dialog on the interactive desktop. Only the hosts edit
// runs elevated, as a fixed PowerShell script behind one UAC prompt.

const routeSystemSystemTrust = true

const routeHostsCRLF = true

func routeHostsPath() string {
	return filepath.Join(windowsRoot(), "System32", "drivers", "etc", "hosts")
}

// Use the kernel-reported system directory for every process used to install
// trust or request elevation. Never resolve these through a caller's PATH.
var winSystemDir, winSystemDirErr = windows.GetSystemDirectory()

func windowsRoot() string {
	return filepath.Dir(winSystemDir)
}

func windowsPowerShell() string {
	return filepath.Join(windowsRoot(), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

func routeSystemOwnedByUser(os.FileInfo) error { return nil }

func windowsTaskName(id string) string { return `\tfsaz\route-redirect-` + id }

func routeSystemLogHint(p routeSystemPlan) string {
	return "see " + filepath.Join(p.dir, "redirect.log")
}

func routeSystemInstallOS(ctx context.Context, p routeSystemPlan) error {
	if winSystemDirErr != nil {
		return winSystemDirErr
	}
	if err := checkWindowsPaths(p); err != nil {
		return err
	}
	hosts, err := readRouteHosts()
	if err != nil {
		return err
	}
	if err := routeHostsConflict(hosts, p.id, p.aliases); err != nil {
		return err
	}
	if err := installWindowsTask(ctx, p); err != nil {
		return err
	}
	if err := waitRouteSystemService(ctx, p, 15*time.Second); err != nil {
		if busy := checkRouteSystem443(ctx, p); busy != nil {
			return busy
		}
		return err
	}
	if err := installWindowsTrust(ctx, p); err != nil {
		return err
	}
	if editRouteHosts(hosts, p.id, p.aliases, true) == hosts {
		return nil
	}
	return runWindowsElevated(ctx, windowsHostsScript(p.id, p.aliases, true))
}

func routeSystemRestoreOS(ctx context.Context, p routeSystemPlan) error {
	if winSystemDirErr != nil {
		return winSystemDirErr
	}
	var errs []error
	if err := removeWindowsTask(ctx, p); err != nil {
		errs = append(errs, err)
	}
	hosts, err := readRouteHosts()
	if err != nil {
		errs = append(errs, err)
	} else if routeHostsHasMarker(hosts, p.id) {
		if err := runWindowsElevated(ctx, windowsHostsScript(p.id, nil, false)); err != nil {
			errs = append(errs, err)
		}
	}
	if err := removeWindowsTrust(ctx, p.leafSHA256); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// cmd.exe would interpret these characters inside the task's command line.
func checkWindowsPaths(p routeSystemPlan) error {
	for _, s := range []string{p.exe, p.configFile, p.certFile, p.keyFile, p.dir} {
		if strings.ContainsAny(s, "\"%^&|<>!\r\n") {
			return fmt.Errorf("routing path %s contains a character that the Task Scheduler command line cannot carry", s)
		}
	}
	return nil
}

// windowsTaskArguments is the conhost command line of the logon task.
// conhost --headless splits its arguments and quotes them again with
// backslash escapes, which cmd.exe does not understand, so a quoted cmd line
// with spaces in a path arrives garbled. Only the base64 of -EncodedCommand
// passes through unchanged. That script sets TFSAZ_CONFIG and hands cmd its
// command line verbatim through ProcessStartInfo.Arguments.
func windowsTaskArguments(p routeSystemPlan) string {
	cmd := filepath.Join(windowsRoot(), "System32", "cmd.exe")
	log := filepath.Join(p.dir, "redirect.log")
	inner := fmt.Sprintf(`/d /s /c ""%s" route redirect --cert "%s" --key "%s" --port 443 >> "%s" 2>&1"`,
		p.exe, p.certFile, p.keyFile, log)
	script := `$env:TFSAZ_CONFIG = ` + psQuote(p.configFile) + `
$s = New-Object Diagnostics.ProcessStartInfo
$s.FileName = ` + psQuote(cmd) + `
$s.Arguments = ` + psQuote(inner) + `
$s.UseShellExecute = $false
$c = [Diagnostics.Process]::Start($s)
$c.WaitForExit()
exit $c.ExitCode
`
	return `--headless "` + windowsPowerShell() + `" -NoLogo -NoProfile -NonInteractive -EncodedCommand ` + powerShellEncode(script)
}

func windowsTaskXML(p routeSystemPlan, sid string) string {
	args := windowsTaskArguments(p)
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>tfsaz browser routing redirect (` + p.id + `)</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + xmlEscape(sid) + `</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + xmlEscape(sid) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
    <Hidden>true</Hidden>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + xmlEscape(filepath.Join(windowsRoot(), "System32", "conhost.exe")) + `</Command><Arguments>` + xmlEscape(args) + `</Arguments></Exec></Actions>
</Task>
`
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

func utf16File(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2, 2+2*len(units))
	binary.LittleEndian.PutUint16(out, 0xfeff)
	for _, u := range units {
		out = binary.LittleEndian.AppendUint16(out, u)
	}
	return out
}

func installWindowsTask(ctx context.Context, p routeSystemPlan) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	xmlFile := filepath.Join(p.dir, "route-task.xml")
	if err := os.WriteFile(xmlFile, utf16File(windowsTaskXML(p, u.Uid)), 0o600); err != nil {
		return err
	}
	defer os.Remove(xmlFile)
	if err := stopWindowsTask(ctx, p); err != nil {
		return err
	}
	if _, err := runRouteSystemCommand(ctx, nil, filepath.Join(winSystemDir, "schtasks.exe"), "/Create", "/TN", windowsTaskName(p.id), "/XML", xmlFile, "/F"); err != nil {
		return fmt.Errorf("register the logon task: %w", err)
	}
	if _, err := runRouteSystemCommand(ctx, nil, filepath.Join(winSystemDir, "schtasks.exe"), "/Run", "/TN", windowsTaskName(p.id)); err != nil {
		return fmt.Errorf("start the logon task: %w", err)
	}
	return nil
}

// stopWindowsTask ends the task and any redirect process still serving this
// setup's certificate, then waits until loopback port 443 no longer shows it.
func stopWindowsTask(ctx context.Context, p routeSystemPlan) error {
	if _, err := runRouteSystemCommand(ctx, nil, filepath.Join(winSystemDir, "schtasks.exe"), "/Query", "/TN", windowsTaskName(p.id)); err == nil {
		runRouteSystemCommand(ctx, nil, filepath.Join(winSystemDir, "schtasks.exe"), "/End", "/TN", windowsTaskName(p.id))
	}
	// Ending the task may leave its child running; this also stops one serving
	// an earlier leaf from the same path.
	runRouteSystemCommand(ctx, nil, windowsPowerShell(), "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(windowsStopScript(p.certFile)))
	sni := routeSystemProbeName(p)
	if sni == "" {
		return nil
	}
	for attempt := 0; ; attempt++ {
		_, leaf4 := routeSystemPeer(ctx, "127.0.0.1:443", sni)
		_, leaf6 := routeSystemPeer(ctx, "[::1]:443", sni)
		if leaf4 != p.leafSHA256 && leaf6 != p.leafSHA256 {
			return nil
		}
		if attempt == 20 {
			return errors.New("a tfsaz route redirect process for this setup is still running; end it in Task Manager and retry")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func routeSystemProbeName(p routeSystemPlan) string {
	if len(p.aliases) > 0 {
		return p.aliases[0]
	}
	if names := routeSystemLeafNames(p.certDER); len(names) > 0 {
		return names[0]
	}
	return ""
}

func windowsStopScript(certFile string) string {
	return `Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -and $_.CommandLine.Contains(' route redirect ') -and $_.CommandLine.Contains(` +
		psQuote(`"`+certFile+`"`) + `) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`
}

func removeWindowsTask(ctx context.Context, p routeSystemPlan) error {
	if err := stopWindowsTask(ctx, p); err != nil {
		return err
	}
	if _, err := runRouteSystemCommand(ctx, nil, filepath.Join(winSystemDir, "schtasks.exe"), "/Query", "/TN", windowsTaskName(p.id)); err != nil {
		return nil
	}
	if _, err := runRouteSystemCommand(ctx, nil, filepath.Join(winSystemDir, "schtasks.exe"), "/Delete", "/TN", windowsTaskName(p.id), "/F"); err != nil {
		return fmt.Errorf("remove the logon task: %w", err)
	}
	return nil
}

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func powerShellEncode(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 0, 2*len(units))
	for _, u := range units {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// windowsRootEntry is a Root certificate visible to the current user. The
// CurrentUser view also lists Local Machine and policy roots. Only entries in
// the user's own registry store come from certutil -user, so restore owns
// only those.
type windowsRootEntry struct {
	thumbprint string
	userStore  bool
}

const windowsUserRootKey = `HKCU:\Software\Microsoft\SystemCertificates\Root\Certificates\`

// windowsRootThumbprints lists the Root entries whose SHA-256 equals sha256Hex.
func windowsRootThumbprints(ctx context.Context, sha256Hex string) ([]windowsRootEntry, error) {
	script := `$h = [Security.Cryptography.SHA256]::Create(); Get-ChildItem Cert:\CurrentUser\Root | ForEach-Object { $_.Thumbprint + ' ' + ([BitConverter]::ToString($h.ComputeHash($_.RawData)) -replace '-','') + ' ' + [int](Test-Path -LiteralPath (` +
		psQuote(windowsUserRootKey) + ` + $_.Thumbprint)) }`
	out, err := runRouteSystemCommand(ctx, nil, windowsPowerShell(), "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(script))
	if err != nil {
		return nil, fmt.Errorf("list the user's trusted root certificates: %w", err)
	}
	return parseWindowsRootEntries(string(out), sha256Hex), nil
}

func parseWindowsRootEntries(out, sha256Hex string) []windowsRootEntry {
	var entries []windowsRootEntry
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) == 3 && strings.EqualFold(f[1], sha256Hex) {
			entries = append(entries, windowsRootEntry{f[0], f[2] == "1"})
		}
	}
	return entries
}

func windowsUserRootEntries(entries []windowsRootEntry) (user, other []string) {
	for _, e := range entries {
		if e.userStore {
			user = append(user, e.thumbprint)
		} else {
			other = append(other, e.thumbprint)
		}
	}
	return user, other
}

// installWindowsTrust accepts the leaf wherever the user already trusts it,
// so an explicit machine-wide installation causes no per-user prompt.
func installWindowsTrust(ctx context.Context, p routeSystemPlan) error {
	entries, err := windowsRootThumbprints(ctx, p.leafSHA256)
	if err != nil || len(entries) > 0 {
		return err
	}
	if _, err := runRouteSystemCommand(ctx, nil, filepath.Join(winSystemDir, "certutil.exe"), "-user", "-addstore", "Root", p.certFile); err != nil {
		return fmt.Errorf("trust the routing certificate (confirm the dialog on the interactive desktop): %w", err)
	}
	if entries, err = windowsRootThumbprints(ctx, p.leafSHA256); err == nil && len(entries) == 0 {
		return errors.New("the routing certificate is not in the user's trusted root store; confirm the dialog and retry")
	}
	return err
}

// removeWindowsTrust removes the leaf from the user's own Root store only.
// A Local Machine or policy copy was installed outside tfsaz and needs an
// administrator, so restore reports it instead of claiming a dialog.
func removeWindowsTrust(ctx context.Context, sha256Hex string) error {
	entries, err := windowsRootThumbprints(ctx, sha256Hex)
	if err != nil {
		return err
	}
	user, _ := windowsUserRootEntries(entries)
	for _, thumb := range user {
		if _, err := runRouteSystemCommand(ctx, nil, filepath.Join(winSystemDir, "certutil.exe"), "-user", "-delstore", "Root", thumb); err != nil {
			return fmt.Errorf("remove the routing certificate from the user's trusted root store (confirm the dialog on the interactive desktop): %w", err)
		}
	}
	if entries, err = windowsRootThumbprints(ctx, sha256Hex); err != nil {
		return err
	}
	user, other := windowsUserRootEntries(entries)
	if len(user) > 0 {
		return errors.New("the routing certificate is still in the user's trusted root store; confirm the removal dialog on the interactive desktop and rerun restore")
	}
	if len(other) > 0 {
		return fmt.Errorf("the routing certificate is also trusted outside the user's own store, which tfsaz does not change; from an elevated prompt run certutil -delstore Root %s (or remove the policy entry) and rerun restore", strings.Join(other, ", "))
	}
	return nil
}

// windowsHostsScript mirrors editRouteHosts. It publishes
// an atomic replacement that preserves its ACL, treating bytes as Latin-1 so
// foreign content round-trips unchanged.
func windowsHostsScript(id string, aliases []string, install bool) string {
	return windowsHostsScriptAt(routeHostsPath(), id, aliases, install)
}

func windowsHostsScriptAt(path, id string, aliases []string, install bool) string {
	var add []string
	if install {
		for _, line := range routeHostsLines(id, aliases) {
			add = append(add, psQuote(line))
		}
	}
	flag := "$false"
	if install {
		flag = "$true"
	}
	return `$ErrorActionPreference = 'Stop'
$h = ` + psQuote(path) + `
$m = ` + psQuote(routeHostsMarker(id)) + `
$install = ` + flag + `
$add = @(` + strings.Join(add, ", ") + `)
$enc = [Text.Encoding]::GetEncoding(28591)
# Allow an atomic replacement while excluding competing in-place writers.
$share = [IO.FileShare]::Read -bor [IO.FileShare]::Delete
$fs = [IO.File]::Open($h, [IO.FileMode]::Open, [IO.FileAccess]::Read, $share)
$tmp = $null
try {
  $reader = [IO.BinaryReader]::new($fs)
  $text = $enc.GetString($reader.ReadBytes([int]$fs.Length))
  $kept = [Text.StringBuilder]::new()
  $nl = [Environment]::NewLine
  if ($text.Length -gt 0 -and -not $text.Contains([string][char]13 + [char]10)) { $nl = [string][char]10 }
  foreach ($entry in $add) { [void]$kept.Append($entry + $nl) }
  $aliases = @($add | ForEach-Object { ($_ -split ' ')[1] })
  foreach ($match in [regex]::Matches($text, '[^\n]*\n|[^\n]+\z')) {
    $line = $match.Value.TrimEnd([char[]]@(13, 10))
    if ($line.EndsWith($m, [StringComparison]::Ordinal)) { continue }
    if ($install) {
      $names = @(($line.Split('#')[0].Trim() -split '\s+') | Select-Object -Skip 1)
      foreach ($name in $names) { if ($aliases -contains $name.TrimEnd('.')) { throw 'Foreign hosts entry already names this alias' } }
    }
    [void]$kept.Append($match.Value)
  }
  $out = $kept.ToString()
  if ($out -cne $text) {
    $tmp = Join-Path ([IO.Path]::GetDirectoryName($h)) ([IO.Path]::GetRandomFileName())
    $writer = [IO.File]::Open($tmp, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try {
      $bytes = $enc.GetBytes($out)
      $writer.Write($bytes, 0, $bytes.Length)
      $writer.Flush($true)
    } finally { $writer.Dispose() }
    [IO.File]::Replace($tmp, $h, [System.Management.Automation.Language.NullString]::Value)
  }
} finally { $fs.Dispose(); if ($tmp -and [IO.File]::Exists($tmp)) { [IO.File]::Delete($tmp) } }
exit 0
`
}

// runWindowsElevated starts Windows PowerShell elevated through one UAC
// prompt. The script travels on the command line, never through a file.
func runWindowsElevated(ctx context.Context, script string) error {
	ps := windowsPowerShell()
	outer := `$p = Start-Process -FilePath ` + psQuote(ps) + ` -Verb RunAs -Wait -PassThru -WindowStyle Hidden -ArgumentList '-NoProfile','-NonInteractive','-EncodedCommand',` +
		psQuote(powerShellEncode(script)) + `
exit $p.ExitCode`
	if _, err := runRouteSystemCommand(ctx, nil, ps, "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(outer)); err != nil {
		return fmt.Errorf("administrator step for the hosts file (approve the UAC prompt on the interactive desktop): %w", err)
	}
	return nil
}
