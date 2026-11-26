//go:build windows

package az

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// Windows: a per-user Task Scheduler logon task runs the redirect service
// directly on loopback port 443 (Winsock needs no privilege for it), hidden
// through Windows PowerShell. The leaf goes into the user's Root store, which
// shows a confirmation dialog on the interactive desktop. Only the hosts edit
// runs elevated, as a fixed PowerShell script behind one UAC prompt.

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
	pending, err := windowsHostsRecoveryPending(routeHostsPath())
	if err != nil {
		return err
	}
	if !pending {
		if err := routeHostsConflict(hosts, p.id, p.aliases); err != nil {
			return err
		}
	}
	if err := installWindowsTask(ctx, p); err != nil {
		return err
	}
	if err := waitRouteSystemService(ctx, p, 45*time.Second); err != nil {
		if busy := checkRouteSystem443(ctx, p); busy != nil {
			return busy
		}
		return err
	}
	if err := installWindowsTrust(ctx, p); err != nil {
		return err
	}
	if !pending && editRouteHosts(hosts, p.id, p.aliases, true) == hosts {
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
	} else if pending, err := windowsHostsRecoveryPending(routeHostsPath()); err != nil {
		errs = append(errs, err)
	} else if pending || routeHostsHasMarker(hosts, p.id) {
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

// Task Scheduler may leave an on-demand run stopped after a failed action.
// Supervise the redirector in the task so crashes recover before the next logon.
// The encoded command preserves paths with spaces.
func windowsTaskArguments(p routeSystemPlan) string {
	cmd := filepath.Join(windowsRoot(), "System32", "cmd.exe")
	log := filepath.Join(p.dir, "redirect.log")
	inner := fmt.Sprintf(`/d /s /c ""%s" route redirect --cert "%s" --key "%s" --port 443 >> "%s" 2>&1"`,
		p.exe, p.certFile, p.keyFile, log)
	script := windowsSupervisorMarker(p.id) + `$env:TFSAZ_CONFIG = ` + psQuote(p.configFile) + `
$s = New-Object Diagnostics.ProcessStartInfo
$s.FileName = ` + psQuote(cmd) + `
$s.Arguments = ` + psQuote(inner) + `
$s.UseShellExecute = $false
$s.CreateNoWindow = $true
while ($true) {
  $c = [Diagnostics.Process]::Start($s)
  try { $c.WaitForExit(); $code = $c.ExitCode } finally { $c.Dispose() }
  if ($code -eq 0) { exit 0 }
  Start-Sleep -Seconds 10
}
`
	// A regular console can wait for the desktop terminal to initialize in a
	// disconnected session. Keep the supervisor inside its own headless console.
	return `--headless "` + windowsPowerShell() + `" -NoLogo -NoProfile -NonInteractive -EncodedCommand ` + powerShellEncode(script)
}

func windowsTaskXML(p routeSystemPlan, sid string) string {
	args := windowsTaskArguments(p)
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>TFSaz browser routing redirect (` + p.id + `)</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + xmlEscape(sid) + `</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>` + xmlEscape(sid) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure>
    <Hidden>true</Hidden>
    <Priority>5</Priority>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + xmlEscape(filepath.Join(winSystemDir, "conhost.exe")) + `</Command><Arguments>` + xmlEscape(args) + `</Arguments></Exec></Actions>
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

func installWindowsTask(ctx context.Context, p routeSystemPlan) (err error) {
	u, err := user.Current()
	if err != nil {
		return err
	}
	xmlFile := filepath.Join(p.dir, "route-task.xml")
	if err := os.WriteFile(xmlFile, utf16File(windowsTaskXML(p, u.Uid)), 0o600); err != nil {
		return err
	}
	defer removeStaged(xmlFile, &err)
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
		if _, endErr := runRouteSystemCommand(ctx, nil, filepath.Join(winSystemDir, "schtasks.exe"), "/End", "/TN", windowsTaskName(p.id)); endErr != nil {
			// An idle task needs no stop. A live supervisor must be stopped
			// before its child or it can relaunch the redirector during restore.
			check := `$ErrorActionPreference = 'Stop'; $s = New-Object -ComObject Schedule.Service; $s.Connect(); $t = $s.GetFolder('\tfsaz').GetTask(` +
				psQuote(filepath.Base(windowsTaskName(p.id))) + `); if ($t.GetInstances(0).Count -ne 0) { exit 1 }`
			if _, err := runRouteSystemCommand(ctx, nil, windowsPowerShell(), "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(check)); err != nil {
				return fmt.Errorf("could not confirm that the routing task stopped: %w", endErr)
			}
		}
	}
	// Ending the task may leave its child running; this also stops one serving
	// an earlier leaf from the same path.
	if _, err := runRouteSystemCommand(ctx, nil, windowsPowerShell(), "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(windowsStopScript(p))); err != nil {
		return fmt.Errorf("stop the routing task's child processes: %w", err)
	}
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

// windowsHostsJournalHead starts the Windows hosts journal. Restore reads
// journals that older versions wrote, so it never changes.
const windowsHostsJournalHead = "tfsaz hosts undo 1" // naming-allow: persisted format line

func windowsSupervisorMarker(id string) string {
	// naming-allow: restore finds running supervisors by this exact marker
	return "# tfsaz routing supervisor " + id + "\n"
}

func windowsStopScript(p routeSystemPlan) string {
	// Ending the console host can leave PowerShell behind in a disconnected
	// session. Stop the supervisor before its child, including while it sleeps.
	return `$ErrorActionPreference = 'Stop'
$marker = ` + psQuote(windowsSupervisorMarker(p.id)) + `
Get-CimInstance Win32_Process -Filter "Name = 'powershell.exe'" | ForEach-Object {
  if ($_.ProcessId -ne $PID -and $_.CommandLine -match ' -EncodedCommand\s+"?([a-zA-Z0-9+/=]+)"?\s*$') {
    try { $script = [Text.Encoding]::Unicode.GetString([Convert]::FromBase64String($Matches[1])) } catch { return }
    if ($script.StartsWith($marker, [StringComparison]::Ordinal)) { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
  }
}
Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -and $_.CommandLine.Contains(' route redirect ') -and $_.CommandLine.Contains(` +
		psQuote(`"`+p.certFile+`"`) + `) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`
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
	// schtasks leaves the \tfsaz folder behind. Task Scheduler refuses to delete
	// a folder that still holds another configuration's task, so this removes
	// only an empty one, and fails when the folder is already gone, so its
	// result says nothing about this configuration.
	_, _ = runRouteSystemCommand(ctx, nil, windowsPowerShell(), "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(windowsTaskFolderScript))
	return nil
}

const windowsTaskFolderScript = `$s = New-Object -ComObject Schedule.Service; $s.Connect(); $f = $s.GetFolder('\tfsaz'); if ($f.GetTasks(1).Count -eq 0 -and $f.GetFolders(0).Count -eq 0) { $s.GetFolder('\').DeleteFolder('tfsaz', 0) }`

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
// A Local Machine or policy copy was installed outside TFSaz and needs an
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
		return fmt.Errorf("the routing certificate is also trusted outside the user's own store, which TFSaz does not change; from an elevated prompt run certutil -delstore Root %s (or remove the policy entry) and rerun restore", strings.Join(other, ", "))
	}
	return nil
}

// windowsHostsScript mirrors editRouteHosts. It rewrites the hosts file in
// place, so its file ID, owner, group, ACL and attributes stay as they are,
// while a share mode keeps other writers, renames and deletes out. Bytes are
// treated as Latin-1, so foreign content and mixed line endings round-trip
// unchanged.
//
// Before the first byte changes, the script durably writes an undo journal
// beside the file holding only this edit's old and new content. A later run
// recovers it first: it drops the journal when the file holds either version
// and rolls back a file that is a torn mix of both. Any other content means
// someone edited the file after the interruption, so the script exits with
// windowsHostsExitChanged and keeps both files for the user. The test
// replaces the "# interrupt:" comments to stop the process at those points.
func windowsHostsScript(id string, aliases []string, install bool) string {
	return windowsHostsScriptAt(routeHostsPath(), id, aliases, install)
}

func windowsHostsJournal(path string) string { return path + ".tfsaz-journal" }

// Recovery must run even when the interrupted write left no marked line.
func windowsHostsRecoveryPending(path string) (bool, error) {
	_, err := os.Lstat(windowsHostsJournal(path))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

const (
	windowsHostsExitLocked  = 3
	windowsHostsExitChanged = 4
	windowsHostsExitForeign = 5
)

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
$j = ` + psQuote(windowsHostsJournal(path)) + `
$m = ` + psQuote(routeHostsMarker(id)) + `
$install = ` + flag + `
$add = @(` + strings.Join(add, ", ") + `)
$enc = [Text.Encoding]::GetEncoding(28591)
$head = ` + psQuote(windowsHostsJournalHead) + ` + "` + "`n" + `"
$tail = "` + "`n" + `end` + "`n" + `"
function Same([string]$a, [string]$b) { [string]::Equals($a, $b) }
function Read-Text([IO.FileStream]$s) {
  $b = New-Object byte[] ([int]$s.Length)
  $s.Position = 0
  $n = 0
  while ($n -lt $b.Length) { $r = $s.Read($b, $n, $b.Length - $n); if ($r -le 0) { throw 'The hosts file ended early' }; $n += $r }
  $enc.GetString($b)
}
function Write-Text([IO.FileStream]$s, [string]$text) {
  $b = $enc.GetBytes($text)
  $s.Position = 0
  $s.Write($b, 0, $b.Length)
  $s.SetLength($b.Length)
  $s.Flush($true)
}
function Read-Journal {
  $t = $enc.GetString([IO.File]::ReadAllBytes($j))
  if (-not $t.StartsWith($head, [StringComparison]::Ordinal) -or -not $t.EndsWith($tail, [StringComparison]::Ordinal)) { return $null }
  $eol = $t.IndexOf([char]10, $head.Length)
  if ($eol -lt 0) { return $null }
  $sizes = $t.Substring($head.Length, $eol - $head.Length).Split(' ')
  $lo = 0; $ln = 0
  if ($sizes.Count -ne 2 -or -not [int]::TryParse($sizes[0], [ref]$lo) -or -not [int]::TryParse($sizes[1], [ref]$ln) -or $lo -lt 0 -or $ln -lt 0) { return $null }
  if ($t.Length -ne $eol + 1 + $lo + $ln + $tail.Length) { return $null }
  @{ old = $t.Substring($eol + 1, $lo); new = $t.Substring($eol + 1 + $lo, $ln) }
}
# A torn in-place write leaves each byte from the old or new content, or zero
# where the file grew, at a length between the two.
function Test-Torn([string]$cur, [string]$old, [string]$new) {
  if ($cur.Length -lt [Math]::Min($old.Length, $new.Length) -or $cur.Length -gt [Math]::Max($old.Length, $new.Length)) { return $false }
  for ($i = 0; $i -lt $cur.Length; $i++) {
    $c = [int]$cur[$i]
    if (($i -lt $old.Length -and $c -eq [int]$old[$i]) -or ($i -lt $new.Length -and $c -eq [int]$new[$i]) -or ($i -ge $old.Length -and $c -eq 0)) { continue }
    return $false
  }
  $true
}
try {
  # Readers such as the DNS client stay allowed. Writers, renames and deletes do not.
  $fs = [IO.File]::Open($h, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::Read)
} catch {
  $e = $_.Exception
  while ($e.InnerException) { $e = $e.InnerException }
  if ($e -is [IO.IOException] -and ($e.HResult -band 0xFFFF) -eq 32) { exit ` + fmt.Sprint(windowsHostsExitLocked) + ` }
  throw
}
try {
  $text = Read-Text $fs
  if ([IO.File]::Exists($j)) {
    # The journal is complete before the hosts file changes, so an incomplete
    # one means the hosts file was never touched.
    $undo = Read-Journal
    if ($undo -and -not (Same $text $undo.old) -and -not (Same $text $undo.new)) {
      if (-not (Test-Torn $text $undo.old $undo.new)) { exit ` + fmt.Sprint(windowsHostsExitChanged) + ` }
      Write-Text $fs $undo.old
      $text = $undo.old
    }
    # interrupt: recovered
    [IO.File]::Delete($j)
  }
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
      foreach ($name in $names) { if ($aliases -contains $name.TrimEnd('.')) { exit ` + fmt.Sprint(windowsHostsExitForeign) + ` } }
    }
    [void]$kept.Append($match.Value)
  }
  $out = $kept.ToString()
  if (-not (Same $out $text)) {
    $record = $enc.GetBytes($head + $text.Length + ' ' + $out.Length + [char]10 + $text + $out + $tail)
    $js = [IO.File]::Open($j, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $js.Write($record, 0, $record.Length); $js.Flush($true) } finally { $js.Dispose() }
    # interrupt: journaled
    Write-Text $fs $out
    # interrupt: written
    [IO.File]::Delete($j)
  }
} finally { $fs.Dispose() }
exit 0
`
}

// runWindowsElevated starts Windows PowerShell elevated through one UAC
// prompt. The script travels on the command line, never through a file.
func windowsElevationCommand(script string) string {
	return `$ErrorActionPreference = 'Stop'; $p = Start-Process -FilePath ` + psQuote(windowsPowerShell()) + ` -Verb RunAs -Wait -PassThru -WindowStyle Hidden -ArgumentList '-NoProfile','-NonInteractive','-EncodedCommand',` +
		psQuote(powerShellEncode(script)) + `
exit $p.ExitCode`
}

func runWindowsElevated(ctx context.Context, script string) error {
	// Encode the inner script once. Double-encoding even a small hosts update
	// can exceed the Windows process command-line limit.
	if _, err := runRouteSystemCommand(ctx, nil, windowsPowerShell(), "-NoProfile", "-NonInteractive", "-Command", windowsElevationCommand(script)); err != nil {
		if msg := windowsHostsExitMessage(err); msg != "" {
			return errors.New(msg)
		}
		return fmt.Errorf("administrator step for the hosts file (approve the UAC prompt on the interactive desktop): %w", err)
	}
	return nil
}

func windowsHostsExitMessage(err error) string {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return ""
	}
	switch exit.ExitCode() {
	case windowsHostsExitLocked:
		return "another program is blocking writes to the hosts file; retry when it releases the file"
	case windowsHostsExitChanged:
		return "the hosts file changed after an interrupted TFSaz edit; compare it with " + windowsHostsJournal(routeHostsPath()) + ", then delete that journal and retry"
	case windowsHostsExitForeign:
		return "the hosts file gained an entry for this alias that TFSaz did not add; remove it first"
	}
	return ""
}

func routeSystemLeafNames(der []byte) []string {
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil
	}
	return leaf.DNSNames
}
