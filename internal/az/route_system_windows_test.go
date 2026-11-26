//go:build windows

package az

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// When the test binary itself is started as the task's tfsaz executable, it
// records what it received instead of serving.
const windowsTaskHelperEnv = "TFSAZ_TEST_TASK_ACTION_HELPER"

func init() {
	if os.Getenv(windowsTaskHelperEnv) == "1" {
		fmt.Printf("args=%q\nconfig=%q\n", os.Args[1:], os.Getenv("TFSAZ_CONFIG"))
		os.Exit(0)
	}
}

func decodePowerShellCommand(t *testing.T, args string) string {
	t.Helper()
	i := strings.Index(args, "-EncodedCommand ")
	if i < 0 {
		t.Fatalf("no encoded command in %s", args)
	}
	raw, err := base64.StdEncoding.DecodeString(args[i+len("-EncodedCommand "):])
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("encoded command: %v", err)
	}
	units := make([]uint16, len(raw)/2)
	for j := range units {
		units[j] = binary.LittleEndian.Uint16(raw[2*j:])
	}
	return string(utf16.Decode(units))
}

func TestWindowsTaskXML(t *testing.T) {
	p := routeSystemPlan{id: routeTestID, exe: `C:\Program Files\tfsaz\tfsaz.exe`, configFile: `C:\Users\a b\cfg.json`,
		certFile: `C:\Users\a b\r\cert.pem`, keyFile: `C:\Users\a b\r\key.pem`, dir: `C:\Users\a b\r`}
	if err := checkWindowsPaths(p); err != nil {
		t.Fatal(err)
	}
	x := windowsTaskXML(p, "S-1-5-21-1")
	for _, want := range []string{"<LogonType>InteractiveToken</LogonType>", "<RunLevel>LeastPrivilege</RunLevel>", "conhost.exe</Command>", "--headless"} {
		if !strings.Contains(x, want) {
			t.Errorf("task lacks %s:\n%s", want, x)
		}
	}
	script := decodePowerShellCommand(t, windowsTaskArguments(p))
	for _, want := range []string{`$env:TFSAZ_CONFIG = 'C:\Users\a b\cfg.json'`,
		`""C:\Program Files\tfsaz\tfsaz.exe" route redirect --cert "C:\Users\a b\r\cert.pem" --key "C:\Users\a b\r\key.pem" --port 443 >> "C:\Users\a b\r\redirect.log" 2>&1"`} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %s:\n%s", want, script)
		}
	}
	p.configFile = `C:\Users\100%\cfg.json`
	if checkWindowsPaths(p) == nil {
		t.Fatal("accepted a cmd metacharacter")
	}
}

// TestWindowsTaskActionRuns starts the task action through the real
// PowerShell and cmd chain and checks that paths with spaces, an apostrophe
// and non-ASCII letters reach the executable and the log intact.
func TestWindowsTaskActionRuns(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "J\u00fcrgen O'Brien x")
	dir := filepath.Join(root, "cfg.json.browser-route")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "bin dir", "tfsaz test.exe")
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, data, 0o700); err != nil {
		t.Fatal(err)
	}
	p := routeSystemPlan{id: routeTestID, exe: exe, configFile: filepath.Join(root, "cfg.json"),
		certFile: filepath.Join(dir, routeCertName), keyFile: filepath.Join(dir, routeKeyName), dir: dir}
	if err := checkWindowsPaths(p); err != nil {
		t.Fatal(err)
	}
	var task struct {
		Command   string `xml:"Actions>Exec>Command"`
		Arguments string `xml:"Actions>Exec>Arguments"`
	}
	if err := xml.Unmarshal([]byte(strings.Replace(windowsTaskXML(p, "S-1-5-21-1"), `encoding="UTF-16"`, `encoding="UTF-8"`, 1)), &task); err != nil {
		t.Fatal(err)
	}
	launch := `$p = Start-Process -Wait -PassThru -FilePath ` + psQuote(task.Command) + ` -ArgumentList ` + psQuote(task.Arguments) + ` -WindowStyle Hidden; exit $p.ExitCode`
	cmd := exec.Command(windowsPowerShell(), "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(launch))
	cmd.Env = append(os.Environ(), windowsTaskHelperEnv+"=1", "TFSAZ_CONFIG=")
	out, err := cmd.CombinedOutput()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0 {
		t.Fatalf("task should exit after a successful redirector run: %v: %s", err, out)
	}
	want := fmt.Sprintf("args=%q\nconfig=%q\n", []string{"route", "redirect", "--cert", p.certFile, "--key", p.keyFile, "--port", "443"}, p.configFile)
	log, err := os.ReadFile(filepath.Join(dir, "redirect.log"))
	if err != nil || strings.ReplaceAll(string(log), "\r\n", "\n") != want {
		t.Fatalf("unexpected redirector output: %q, err=%v", log, err)
	}
}

func TestWindowsHostsScriptIsFixed(t *testing.T) {
	s := windowsHostsScript(routeTestID, []string{routeTestAlias}, true)
	if !strings.Contains(s, "'127.0.0.1 "+routeTestAlias+" # tfsaz-route "+routeTestID+"'") || !strings.Contains(s, "[IO.FileMode]::Open") {
		t.Fatalf("script:\n%s", s)
	}
	if r := windowsHostsScript(routeTestID, nil, false); !strings.Contains(r, "$add = @()") || !strings.Contains(r, "$install = $false") {
		t.Fatalf("restore script:\n%s", r)
	}
}

// hostsFixture creates a file whose owner, group and DACL, including which
// entries are inherited, match the system hosts file. Its directory carries
// the inheritable part so the file's ACL is a real inherited one.
func hostsFixture(t *testing.T, content string) string {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		path := filepath.Join(t.TempDir(), "hosts")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	sys, err := windows.GetNamedSecurityInfo(routeHostsPath(), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Skipf("read the system hosts security descriptor: %v", err)
	}
	want := sys.String()
	i := strings.Index(want, "D:")
	if i < 0 {
		t.Fatalf("no DACL in %s", want)
	}
	var inherited, explicit strings.Builder
	for _, ace := range regexp.MustCompile(`\([^)]*\)`).FindAllString(want[i:], -1) {
		f := strings.Split(ace[1:len(ace)-1], ";")
		if strings.Contains(f[1], "ID") {
			f[1] = strings.Replace(f[1], "ID", "OICI", 1)
			inherited.WriteString("(" + strings.Join(f, ";") + ")")
		} else {
			explicit.WriteString(ace)
		}
	}
	dir := filepath.Join(t.TempDir(), "etc")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	setSecurity(t, dir, "D:P"+inherited.String(), windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION)
	path := filepath.Join(dir, "hosts")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := want[:i]
	setSecurity(t, path, owner+"D:"+explicit.String(), windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION)
	// SetNamedSecurityInfo can mark the fixture DACL as auto-inherited even
	// when the original file has the same inherited ACEs without that flag.
	// Match all owners, groups and ACEs. Every subsequent check compares the
	// complete pre-edit descriptor, including this control flag, byte for byte.
	got := securityString(t, path)
	if got != want && strings.Replace(got, "D:AI(", "D:(", 1) != want {
		t.Fatalf("fixture security\n got %s\nwant %s", got, want)
	}
	// Let the temporary directory be removed with the fixture's ACL in place.
	t.Cleanup(func() {
		setSecurity(t, dir, "D:(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)", windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION)
		setSecurity(t, path, "D:(A;;FA;;;BA)", windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION)
	})
	return path
}

func setSecurity(t *testing.T, path, sddl string, info windows.SECURITY_INFORMATION) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatalf("%s: %v", sddl, err)
	}
	owner, _, _ := sd.Owner()
	group, _, _ := sd.Group()
	dacl, _, _ := sd.DACL()
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, owner, group, dacl, nil); err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_INVALID_OWNER) || errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("apply the hosts security descriptor (needs an elevated test): %v", err)
		}
		t.Fatalf("set security on %s: %v", path, err)
	}
}

func securityString(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.GROUP_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

type hostsIdentity struct {
	security string
	info     windows.ByHandleFileInformation
}

func readHostsIdentity(t *testing.T, path string) hostsIdentity {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var id hostsIdentity
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &id.info); err != nil {
		t.Fatal(err)
	}
	id.security = securityString(t, path)
	return id
}

// sameHostsFile compares everything an in-place edit must keep.
func (a hostsIdentity) sameHostsFile(b hostsIdentity) bool {
	return a.security == b.security && a.info.VolumeSerialNumber == b.info.VolumeSerialNumber &&
		a.info.FileIndexHigh == b.info.FileIndexHigh && a.info.FileIndexLow == b.info.FileIndexLow &&
		a.info.FileAttributes == b.info.FileAttributes && a.info.CreationTime == b.info.CreationTime &&
		a.info.NumberOfLinks == b.info.NumberOfLinks
}

// runHostsScript runs the real elevated script body, optionally with a hook
// spliced in at one of its "# interrupt:" points, and returns its exit code.
func runHostsScript(t *testing.T, path string, install bool, from, to string) int {
	t.Helper()
	script := windowsHostsScriptAt(path, routeTestID, []string{routeTestAlias}, install)
	if from != "" {
		if !strings.Contains(script, from) {
			t.Fatalf("script lacks %q", from)
		}
		script = strings.Replace(script, from, to, 1)
	}
	cmd := exec.Command(windowsPowerShell(), "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(script))
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		t.Fatalf("PowerShell hosts edit: %v: %s", err, out)
	}
	return 0
}

// Bytes outside ASCII, a lone CR, mixed endings and no final newline must
// round-trip unchanged.
const hostsFixtureContent = "# caf\xe9 \xff\r\n127.0.0.1 localhost\r\n::1 localhost\nweird\rline\r\n# no final newline"

func TestWindowsHostsFileRoundTrip(t *testing.T) {
	path := hostsFixture(t, hostsFixtureContent)
	before := readHostsIdentity(t, path)
	for cycle := range 2 {
		for _, install := range []bool{true, true, false, false} {
			if code := runHostsScript(t, path, install, "", ""); code != 0 {
				t.Fatalf("cycle %d install %v: exit %d", cycle, install, code)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != editRouteHosts(hostsFixtureContent, routeTestID, []string{routeTestAlias}, install) {
				t.Fatalf("cycle %d install %v: got %q, err %v", cycle, install, got, err)
			}
			if after := readHostsIdentity(t, path); !after.sameHostsFile(before) {
				t.Fatalf("cycle %d install %v: identity changed\n got %+v\nwant %+v", cycle, install, after, before)
			}
			if _, err := os.Stat(windowsHostsJournal(path)); !os.IsNotExist(err) {
				t.Fatalf("journal left behind: %v", err)
			}
		}
	}
}

const hostsKill = "Stop-Process -Id $PID -Force"

// Each case stops the script at one point of an install, then checks that a
// plain retry ends with exactly the intended file under the same identity.
func TestWindowsHostsEditRecovers(t *testing.T) {
	installed := editRouteHosts(hostsFixtureContent, routeTestID, []string{routeTestAlias}, true)
	for _, tc := range []struct {
		name, from, to string
		install        bool
		start          string
	}{
		{"after journal", "# interrupt: journaled", hostsKill, true, hostsFixtureContent},
		{"torn grow", "    Write-Text $fs $out\n", "$b = $enc.GetBytes($out); $fs.Position = 0; $fs.Write($b, 0, [int]($b.Length / 2)); $fs.Flush($true); " + hostsKill + "\n", true, hostsFixtureContent},
		{"grown with zeros", "    Write-Text $fs $out\n", "$fs.SetLength($enc.GetByteCount($out)); $fs.Flush($true); " + hostsKill + "\n", true, hostsFixtureContent},
		{"untruncated shrink", "    Write-Text $fs $out\n", "$b = $enc.GetBytes($out); $fs.Position = 0; $fs.Write($b, 0, $b.Length); $fs.Flush($true); " + hostsKill + "\n", false, installed},
		{"after write", "# interrupt: written", hostsKill, true, hostsFixtureContent},
		{"during recovery", "# interrupt: recovered", hostsKill, true, hostsFixtureContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := hostsFixture(t, tc.start)
			before := readHostsIdentity(t, path)
			want := editRouteHosts(tc.start, routeTestID, []string{routeTestAlias}, tc.install)
			if tc.name == "during recovery" {
				// Leave a torn file first, then interrupt its rollback.
				if code := runHostsScript(t, path, true, "    Write-Text $fs $out\n", "$b = $enc.GetBytes($out); $fs.Position = 0; $fs.Write($b, 0, [int]($b.Length / 2)); $fs.Flush($true); "+hostsKill+"\n"); code == 0 {
					t.Fatal("first run was not interrupted")
				}
			}
			if code := runHostsScript(t, path, tc.install, tc.from, tc.to); code == 0 {
				t.Fatal("run was not interrupted")
			}
			if _, err := os.Stat(windowsHostsJournal(path)); err != nil {
				t.Fatalf("no journal after the interruption: %v", err)
			}
			if code := runHostsScript(t, path, tc.install, "", ""); code != 0 {
				t.Fatalf("retry exit %d", code)
			}
			got, _ := os.ReadFile(path)
			if string(got) != want {
				t.Fatalf("after retry got %q\nwant %q", got, want)
			}
			if after := readHostsIdentity(t, path); !after.sameHostsFile(before) {
				t.Fatalf("identity changed\n got %+v\nwant %+v", after, before)
			}
			if _, err := os.Stat(windowsHostsJournal(path)); !os.IsNotExist(err) {
				t.Fatalf("journal left behind: %v", err)
			}
		})
	}
}

func TestWindowsHostsEditRefuses(t *testing.T) {
	t.Run("foreign edit after interruption", func(t *testing.T) {
		path := hostsFixture(t, hostsFixtureContent)
		if code := runHostsScript(t, path, true, "# interrupt: journaled", hostsKill); code == 0 {
			t.Fatal("run was not interrupted")
		}
		edited := hostsFixtureContent + "\r\n10.0.0.1 other\r\n"
		if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
			t.Fatal(err)
		}
		journal, _ := os.ReadFile(windowsHostsJournal(path))
		if code := runHostsScript(t, path, true, "", ""); code != windowsHostsExitChanged {
			t.Fatalf("exit %d", code)
		}
		got, _ := os.ReadFile(path)
		kept, _ := os.ReadFile(windowsHostsJournal(path))
		if string(got) != edited || string(kept) != string(journal) {
			t.Fatalf("refusal changed files: %q / %q", got, kept)
		}
	})
	t.Run("incomplete journal", func(t *testing.T) {
		path := hostsFixture(t, hostsFixtureContent)
		if err := os.WriteFile(windowsHostsJournal(path), []byte("tfsaz hosts undo 1\n12 "), 0o600); err != nil {
			t.Fatal(err)
		}
		if code := runHostsScript(t, path, true, "", ""); code != 0 {
			t.Fatalf("exit %d", code)
		}
		got, _ := os.ReadFile(path)
		if string(got) != editRouteHosts(hostsFixtureContent, routeTestID, []string{routeTestAlias}, true) {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("open for writing", func(t *testing.T) {
		path := hostsFixture(t, hostsFixtureContent)
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		code := runHostsScript(t, path, true, "", "")
		f.Close()
		got, _ := os.ReadFile(path)
		if code != windowsHostsExitLocked || string(got) != hostsFixtureContent {
			t.Fatalf("exit %d, got %q", code, got)
		}
	})
	t.Run("foreign alias", func(t *testing.T) {
		content := hostsFixtureContent + "\r\n10.0.0.1 " + routeTestAlias + "\r\n"
		path := hostsFixture(t, content)
		code := runHostsScript(t, path, true, "", "")
		got, _ := os.ReadFile(path)
		if code != windowsHostsExitForeign || string(got) != content {
			t.Fatalf("exit %d, got %q", code, got)
		}
		if _, err := os.Stat(windowsHostsJournal(path)); !os.IsNotExist(err) {
			t.Fatalf("journal: %v", err)
		}
	})
}

func TestWindowsRootEntriesSeparateUserStore(t *testing.T) {
	const sha = "ab01"
	out := "AAA ab01 1\r\nBBB AB01 0\r\nCCC ff00 1\r\nDDD ab01\r\n"
	user, other := windowsUserRootEntries(parseWindowsRootEntries(out, sha))
	if !slices.Equal(user, []string{"AAA"}) || !slices.Equal(other, []string{"BBB"}) {
		t.Fatalf("user %v, other %v", user, other)
	}
}

// The listing script must run in Windows PowerShell and report machine roots,
// which every installation has, as outside the user's own store.
func TestWindowsRootListingMarksMachineRoots(t *testing.T) {
	script := `Get-ChildItem Cert:\LocalMachine\Root | Select-Object -First 1 | ForEach-Object { $h = [Security.Cryptography.SHA256]::Create(); ([BitConverter]::ToString($h.ComputeHash($_.RawData)) -replace '-','') + ' ' + [int](Test-Path -LiteralPath ('HKLM:\SOFTWARE\Microsoft\SystemCertificates\ROOT\Certificates\' + $_.Thumbprint)) + ' ' + [int](Test-Path -LiteralPath ('HKCU:\Software\Microsoft\SystemCertificates\Root\Certificates\' + $_.Thumbprint)) }`
	out, err := exec.Command(windowsPowerShell(), "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(script)).Output()
	f := strings.Fields(string(out))
	if err != nil || len(f) != 3 {
		t.Skipf("no Local Machine root to compare: %v %q", err, out)
	}
	if f[2] == "1" {
		t.Skip("the first machine root is also in the user's own store")
	}
	entries, err := windowsRootThumbprints(t.Context(), f[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 || entries[0].userStore {
		t.Fatalf("machine root reported as %+v", entries)
	}
}

// Register a real task only on an explicitly selected Windows test desktop.
func TestWindowsScheduledServiceRestartsFailedAction(t *testing.T) {
	if os.Getenv("TFSAZ_LIVE_TASK_SCHEDULER") != "1" {
		t.Skip("set TFSAZ_LIVE_TASK_SCHEDULER=1 in an interactive Windows test session to register a temporary scheduled task")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "fail once.cmd")
	// Keep failing so the test can stop the real supervisor during a retry.
	script := "@echo off\r\necho attempt>> \"%~dp0runs.txt\"\r\nexit /b 7\r\n"
	if err := os.WriteFile(exe, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	p := routeSystemPlan{id: routeSystemID(dir), exe: exe, dir: dir, configFile: filepath.Join(dir, "config.json"), certFile: filepath.Join(dir, "leaf.pem"), keyFile: filepath.Join(dir, "key.pem")}
	t.Cleanup(func() {
		if err := removeWindowsTask(context.Background(), p); err != nil {
			t.Errorf("remove task: %v", err)
		}
	})
	if err := installWindowsTask(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	runs := filepath.Join(dir, "runs.txt")
	// Task Scheduler runs the supervisor out of process and reports no event
	// per retry, so poll the runs file.
	for deadline := time.Now().Add(100 * time.Second); time.Now().Before(deadline); {
		out, _ := os.ReadFile(runs)
		if len(strings.Fields(string(out))) >= 2 {
			if err := stopWindowsTask(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			stopped, err := os.ReadFile(runs)
			if err != nil {
				t.Fatal(err)
			}
			// Outlast the supervisor's 10 s retry delay: a restart that did not
			// happen has no event to wait on.
			time.Sleep(12 * time.Second)
			now, err := os.ReadFile(runs)
			if err != nil || string(now) != string(stopped) {
				t.Fatalf("the task started its child again after stop: %q, %v", now, err)
			}
			return
		}
		time.Sleep(time.Second)
	}
	task := windowsTaskName(p.id)
	tool := filepath.Join(winSystemDir, "schtasks.exe")
	log, _ := os.ReadFile(filepath.Join(dir, "redirect.log"))
	state, _ := exec.Command(tool, "/Query", "/TN", task, "/V", "/FO", "LIST").CombinedOutput()
	attempts, _ := os.ReadFile(filepath.Join(dir, "runs.txt"))
	t.Fatalf("scheduled service did not retry the failed action: attempts=%q task=%s redirector=%s", attempts, state, log)
}

func TestWindowsHostsLauncherHandlesFullScript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(hostsFixtureContent), 0600); err != nil {
		t.Fatal(err)
	}
	script := windowsHostsScriptAt(path, routeTestID, []string{routeTestAlias}, true)
	// Keep the production launch chain and command-line size; the fixture
	// needs no privilege change and must never ask for UAC approval.
	command := strings.Replace(windowsElevationCommand(script), " -Verb RunAs ", " ", 1)
	out, err := exec.Command(windowsPowerShell(), "-NoProfile", "-NonInteractive", "-Command", command).CombinedOutput()
	if err != nil {
		t.Fatalf("launch: %v %s", err, out)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != editRouteHosts(hostsFixtureContent, routeTestID, []string{routeTestAlias}, true) {
		t.Fatalf("hosts edit failed: %v", err)
	}
	if pending, err := windowsHostsRecoveryPending(path); err != nil || pending {
		t.Fatalf("completed edit: pending=%v err=%v", pending, err)
	}
	if err := os.WriteFile(windowsHostsJournal(path), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if pending, err := windowsHostsRecoveryPending(path); err != nil || !pending {
		t.Fatalf("interrupted edit: pending=%v err=%v", pending, err)
	}
	// A launch failure must propagate even when there is no child exit code.
	missing := filepath.Join(filepath.Dir(path), "missing-powershell.exe")
	command = strings.Replace(command, psQuote(windowsPowerShell()), psQuote(missing), 1)
	if err := exec.Command(windowsPowerShell(), "-NoProfile", "-NonInteractive", "-Command", command).Run(); err == nil {
		t.Fatal("launcher reported success without starting the hosts edit")
	}
}
