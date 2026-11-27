package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// sshTarget is one VM login. Host keys are new in every clone, so each run
// keeps its own known_hosts file and accepts the first key it sees.
type sshTarget struct {
	user, host, key, knownHosts string
	port                        int  // 0 is 22
	powershell                  bool // the tool's commands are PowerShell 7, not sh
}

// options are the ssh and scp options of the login, without the port,
// which the two take with different flags.
func (t sshTarget) options() []string {
	return []string{
		"-i", t.key, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=" + t.knownHosts,
		"-o", "ConnectTimeout=5", "-o", "LogLevel=ERROR",
		// A guest that restarts or shuts down may never close the session.
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3",
	}
}

func (t sshTarget) args(command ...string) []string {
	args := t.options()
	if t.port != 0 {
		args = append(args, "-p", strconv.Itoa(t.port))
	}
	return append(append(args, t.user+"@"+t.host), command...)
}

// run executes one of the tool's commands in the VM; stdin may be nil.
func (t sshTarget) run(stdin io.Reader, command string) (string, error) {
	return t.runContext(context.Background(), stdin, command)
}

func (t sshTarget) runContext(ctx context.Context, stdin io.Reader, command string) (string, error) {
	remote := command
	if t.powershell {
		remote = encodePowerShell(command)
	}
	return t.runRemote(ctx, stdin, remote, command)
}

// runRemote runs remote as the login shell's command line, unchanged;
// label names it in errors.
func (t sshTarget) runRemote(ctx context.Context, stdin io.Reader, remote, label string) (string, error) {
	c := exec.CommandContext(ctx, "ssh", t.args(remote)...)
	c.Stdin = stdin
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh %s %q: %w: %s", t.host, label, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// wait retries until the VM accepts the key or the timeout passes.
func (t sshTarget) wait(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		// ConnectTimeout bounds only the connection; a guest that stalls
		// after it must not hold the attempt past the deadline.
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		_, err := t.runContext(ctx, nil, "exit 0")
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("no SSH login after %s: %w", timeout, err)
		}
		time.Sleep(3 * time.Second)
	}
}

// attach runs ssh with the terminal's stdin and stdout, for vm ssh.
func (t sshTarget) attach(command []string) error {
	args := t.args(command...)
	if len(command) == 0 {
		args = append([]string{"-t"}, args...)
	}
	c := exec.Command("ssh", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exitStatus(exit.ExitCode())
	}
	return err
}

func newSSHKey(path, comment string) error {
	for _, p := range []string{path, path + ".pub"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	_, err := output(nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", comment, "-f", path)
	return err
}

// encodePowerShell wraps one of the tool's PowerShell scripts so that no
// shell on the way changes its quotes: Windows' OpenSSH hands the command
// line to its default shell, which splits and strips double quotes. The
// tool's scripts stop at the first error.
func encodePowerShell(script string) string {
	return encodeScript("$ProgressPreference = 'SilentlyContinue'; $ErrorActionPreference = 'Stop'\n" + script)
}

// encodeScript wraps a PowerShell script as it is, for vm run: a script may
// start with using or param, which must come first. Without -OutputFormat
// Text, an encoded command writes its errors as CLIXML. The SSH shell,
// itself PowerShell 7, reports only success or failure of a command unless
// told to pass on its exit code.
func encodeScript(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(raw[2*i:], u)
	}
	return "pwsh -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand " + base64.StdEncoding.EncodeToString(raw) + "; exit $LASTEXITCODE"
}
