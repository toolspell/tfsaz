package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// sshTarget is one VM login. Host keys are new in every clone, so each run
// keeps its own known_hosts file and accepts the first key it sees.
type sshTarget struct {
	user, host, key, knownHosts string
}

func (t sshTarget) args(command ...string) []string {
	args := []string{
		"-i", t.key, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=" + t.knownHosts,
		"-o", "ConnectTimeout=5", "-o", "LogLevel=ERROR",
		t.user + "@" + t.host,
	}
	return append(args, command...)
}

// run executes a remote shell command; stdin may be nil.
func (t sshTarget) run(stdin io.Reader, command string) (string, error) {
	return t.runContext(context.Background(), stdin, command)
}

func (t sshTarget) runContext(ctx context.Context, stdin io.Reader, command string) (string, error) {
	c := exec.CommandContext(ctx, "ssh", t.args(command)...)
	c.Stdin = stdin
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return stdout.String(), fmt.Errorf("ssh %s %q: %w: %s", t.host, command, err, strings.TrimSpace(stderr.String()))
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
		_, err := t.runContext(ctx, nil, "true")
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
