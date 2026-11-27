package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// runResult is what vm run -json prints.
type runResult struct {
	ExitCode int     `json:"exitCode"`
	Stdout   string  `json:"stdout"`
	Stderr   string  `json:"stderr"`
	TimedOut bool    `json:"timedOut"`
	Seconds  float64 `json:"seconds"`
}

// splitCommand splits NAME [FLAGS] -- CMD... into its parts.
func splitCommand(args []string) (name string, flagArgs, command []string, ok bool) {
	i := slices.Index(args, "--")
	if len(args) < 1 || i < 1 || i == len(args)-1 {
		return "", nil, nil, false
	}
	return args[0], args[1:i], args[i+1:], true
}

// vmRun runs a command in a clone: in its shell over SSH, or with -gui in
// the test user's desktop session, where keychains, link handlers and
// browsers behave as for a user at the machine.
func (e *env) vmRun(args []string) error {
	name, flagArgs, command, ok := splitCommand(args)
	if !ok {
		return errUsage
	}
	flags := flag.NewFlagSet("vm run", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "print exit code, stdout and stderr as JSON")
	timeout := flags.Duration("timeout", 10*time.Minute, "stop waiting after this long; exit code 124")
	gui := flags.Bool("gui", false, "run in the test user's desktop session")
	if flags.Parse(flagArgs) != nil || flags.NArg() != 0 {
		return errUsage
	}
	info, err := e.loadVM(name)
	if err != nil {
		return err
	}
	o, _ := osOfVM(name)
	script := strings.Join(command, " ")
	login := info.login()
	var remote string
	switch {
	case *gui && o.guest.powershell:
		remote = encodePowerShell(windowsGUI(script, *timeout))
	case *gui && o.guest.gui == "":
		return fmt.Errorf("%s has no desktop session for -gui yet", o.name)
	case *gui:
		remote = fmt.Sprintf(o.guest.gui, shellQuote(script))
	case o.guest.powershell:
		remote = encodeUserPowerShell(script)
	default:
		remote = script
	}
	// The guest enforces the timeout of a -gui command; this one only
	// catches a guest that stops answering.
	limit := *timeout
	if *gui {
		limit += 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	c := exec.CommandContext(ctx, "ssh", login.args(remote)...)
	var stdout, stderr bytes.Buffer
	if *asJSON {
		c.Stdout, c.Stderr = &stdout, &stderr
	} else {
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
	}
	if !*gui {
		c.Stdin = os.Stdin
	}
	start := time.Now()
	err = c.Run()
	result := runResult{Seconds: time.Since(start).Seconds()}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		result.ExitCode, result.TimedOut = 124, true
	case errors.As(err, &exit):
		result.ExitCode = exit.ExitCode()
	case err != nil:
		return err
	}
	if !*asJSON {
		if result.TimedOut {
			fmt.Fprintf(os.Stderr, "live: %s did not finish within %s\n", name, *timeout)
			return exitStatus(124)
		}
		if result.ExitCode != 0 {
			return exitStatus(result.ExitCode)
		}
		return nil
	}
	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	if result.ExitCode == 124 && *gui && strings.Contains(result.Stderr, "tfsaz-live: timed out") {
		result.TimedOut = true
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// windowsGUI returns a PowerShell script that runs command as its own
// script in the test user's desktop session through a one-time scheduled
// task, then prints its output and exits with its exit code. The task runs
// without elevation, as the user's own programs do.
func windowsGUI(command string, timeout time.Duration) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	user := base64.StdEncoding.EncodeToString([]byte(command))
	return fmt.Sprintf(`$dir = Join-Path $env:TEMP 'tfsaz-gui-%[1]s'
$name = 'tfsaz-gui-%[1]s'
$pwsh = 'C:\Program Files\PowerShell\7\pwsh.exe'
New-Item -ItemType Directory -Force $dir | Out-Null
[IO.File]::WriteAllText((Join-Path $dir 'user.ps1'), [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%[2]s')))
$inner = "& '$pwsh' -NoProfile -NonInteractive -File '$dir\user.ps1' > '$dir\out.txt' 2> '$dir\err.txt'; Set-Content '$dir\exit.txt' ([string]`+"`"+`$LASTEXITCODE)"
$encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($inner))
$action = New-ScheduledTaskAction -Execute $pwsh -Argument "-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand $encoded"
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName $name -Action $action -Principal $principal -Force | Out-Null
try {
	Start-ScheduledTask -TaskName $name
	$deadline = (Get-Date).AddSeconds(%[3]d)
	while (-not ((Test-Path "$dir\exit.txt") -and (Get-Content "$dir\exit.txt"))) {
		if ((Get-Date) -gt $deadline) {
			Stop-ScheduledTask -TaskName $name
			# The task's own process goes; the script and whatever it
			# started, such as a native program, go with its process tree.
			Get-CimInstance Win32_Process | Where-Object CommandLine -like "*$dir*" |
				ForEach-Object { taskkill.exe /T /F /PID $_.ProcessId 2>&1 | Out-Null }
			[Console]::Error.WriteLine('tfsaz-live: timed out')
			exit 124
		}
		Start-Sleep -Milliseconds 200
	}
	foreach ($stream in 'out', 'err') {
		$text = Get-Content -Raw "$dir\$stream.txt" -ErrorAction SilentlyContinue
		if ($text -and $stream -eq 'out') { [Console]::Out.Write($text) }
		if ($text -and $stream -eq 'err') { [Console]::Error.Write($text) }
	}
	exit [int](Get-Content "$dir\exit.txt")
} finally {
	Unregister-ScheduledTask -TaskName $name -Confirm:$false
	Remove-Item -Recurse -Force $dir -ErrorAction SilentlyContinue
}`, id, user, int(timeout.Seconds()))
}

// scpArgs returns scp's arguments for one copy; remote is a path in the
// clone, and push says whether the copy goes to the clone.
func (t sshTarget) scpArgs(local, remote string, push bool) []string {
	args := append([]string{"-r", "-q"}, t.options()...)
	if t.port != 0 {
		args = append(args, "-P", strconv.Itoa(t.port))
	}
	target := t.user + "@" + t.host + ":" + remote
	if push {
		return append(args, local, target)
	}
	return append(args, target, local)
}

// vmCopy copies files to (push) or from a clone with scp.
func (e *env) vmCopy(args []string, push bool) error {
	if len(args) != 3 {
		return errUsage
	}
	info, err := e.loadVM(args[0])
	if err != nil {
		return err
	}
	local, remote := args[1], args[2]
	if !push {
		local, remote = args[2], args[1]
	}
	_, err = output(nil, "scp", info.login().scpArgs(local, remote, push)...)
	return err
}

// vmForward forwards a loopback port on the host to a port on the clone's
// loopback until it is stopped, for example to reach a browser's or an
// app's debugging port from Playwright on the host.
func (e *env) vmForward(args []string) error {
	if len(args) != 2 {
		return errUsage
	}
	localPort, remotePort, ok := strings.Cut(args[1], ":")
	if !ok {
		remotePort = localPort
	}
	for _, p := range []string{localPort, remotePort} {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return errUsage
		}
	}
	info, err := e.loadVM(args[0])
	if err != nil {
		return err
	}
	login := info.login()
	sshArgs := append([]string{"-N", "-o", "ExitOnForwardFailure=yes",
		"-L", "127.0.0.1:" + localPort + ":127.0.0.1:" + remotePort}, login.args()...)
	step("forwarding 127.0.0.1:%s to %s port %s until stopped", localPort, args[0], remotePort)
	c := exec.Command("ssh", sshArgs...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

// vmVNC prints the VNC address of a clone's screen, served by the
// hypervisor, for a VNC client or an agent's VNC tool.
func (e *env) vmVNC(args []string) error {
	if len(args) != 1 {
		return errUsage
	}
	if _, err := e.loadVM(args[0]); err != nil {
		return err
	}
	o, _ := osOfVM(args[0])
	address, err := e.vms(o).vnc(args[0], filepath.Join(e.runDir(args[0]), o.provider+".log"))
	if err == nil {
		fmt.Println(address)
	}
	return err
}

// vmScreenshot saves a PNG of the clone's screen.
func (e *env) vmScreenshot(args []string) error {
	if len(args) != 2 {
		return errUsage
	}
	name := args[0]
	file, err := filepath.Abs(args[1])
	if err != nil {
		return err
	}
	if _, err := e.loadVM(name); err != nil {
		return err
	}
	o, _ := osOfVM(name)
	return e.vms(o).screenshot(name, filepath.Join(e.runDir(name), o.provider+".log"), file)
}
