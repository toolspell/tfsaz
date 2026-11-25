//go:build windows

package az

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
)

// When the test binary itself is started as the task's tfsaz executable, it
// records what it received instead of serving.
const windowsTaskHelperEnv = "TFSAZ_TEST_TASK_ACTION_HELPER"

func init() {
	if os.Getenv(windowsTaskHelperEnv) == "1" {
		fmt.Printf("args=%q\nconfig=%q\n", os.Args[1:], os.Getenv("TFSAZ_CONFIG"))
		os.Exit(3)
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
	for _, want := range []string{"<LogonType>InteractiveToken</LogonType>", "<RunLevel>LeastPrivilege</RunLevel>", "conhost.exe</Command>"} {
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

// TestWindowsTaskActionRuns starts the task action through the real conhost,
// PowerShell and cmd chain and checks that paths with spaces, an apostrophe
// and non-ASCII letters reach the executable and the log intact.
func TestWindowsTaskActionRuns(t *testing.T) {
	conhost := filepath.Join(windowsRoot(), "System32", "conhost.exe")
	if _, err := os.Stat(conhost); err != nil {
		t.Skip("conhost.exe is unavailable")
	}
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
	cmd := exec.Command(task.Command)
	// Task Scheduler joins Command and Arguments into one command line.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + task.Command + `" ` + task.Arguments}
	cmd.Env = append(os.Environ(), windowsTaskHelperEnv+"=1", "TFSAZ_CONFIG=")
	// conhost does not always wait for its client or pass on its exit code, so
	// wait for the helper's output instead.
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("args=%q\nconfig=%q\n", []string{"route", "redirect", "--cert", p.certFile, "--key", p.keyFile, "--port", "443"}, p.configFile)
	var got string
	for deadline := time.Now().Add(60 * time.Second); ; {
		log, _ := os.ReadFile(filepath.Join(dir, "redirect.log"))
		if got = strings.ReplaceAll(string(log), "\r\n", "\n"); got == want {
			break
		}
		if time.Now().After(deadline) {
			// Run the script without conhost to show why the action failed.
			direct := exec.Command(windowsPowerShell(), "-NoProfile", "-NonInteractive", "-Command", decodePowerShellCommand(t, task.Arguments))
			direct.Env = cmd.Env
			out, err := direct.CombinedOutput()
			t.Fatalf("redirect.log:\n%s\nwant:\n%s\ndirect run: %v: %s", got, want, err, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Let cmd release the log before the temporary directory is removed.
	time.Sleep(time.Second)
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

// Exercise .NET's file replacement and argument binding on an ordinary file.
// Use a separate script builder argument so the fixture never opens system hosts.
func TestWindowsHostsFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	original := "127.0.0.1 localhost\r\n# no final newline"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for _, install := range []bool{true, true, false, false} {
		script := windowsHostsScriptAt(path, routeTestID, []string{routeTestAlias}, install)
		cmd := exec.Command(windowsPowerShell(), "-NoProfile", "-NonInteractive", "-EncodedCommand", powerShellEncode(script))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("PowerShell hosts edit: %v: %s", err, out)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != editRouteHosts(original, routeTestID, []string{routeTestAlias}, install) {
			t.Fatalf("install %v: got %q, err %v", install, got, err)
		}
	}
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
