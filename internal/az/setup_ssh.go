package az

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const setupSyntheticSSHHost = "vs-ssh.visualstudio.com"

const setupSSHCheckTimeout = 15 * time.Second

var (
	setupSSHHostName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	setupSSHUserName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]*$`)
)

func setupSSHCommand(r setupRecord) (string, error) {
	if _, _, err := setupSSHURLs(r.RemoteURL, r.SSHURL); err != nil {
		return "", err
	}
	if !filepath.IsAbs(r.SSHConfig) || setupSSHHasControl(r.SSHConfig) {
		return "", errors.New("invalid repository SSH config path")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", errors.New("locate TFSaz executable for Git SSH command")
	}
	// Prefer the installation link (for example Homebrew's bin/tfsaz)
	// when it resolves to this binary. Versioned install directories can move.
	if installed, err := exec.LookPath("tfsaz"); err == nil && filepath.IsAbs(installed) {
		current, e1 := os.Stat(exe)
		target, e2 := os.Stat(installed)
		if e1 == nil && e2 == nil && os.SameFile(current, target) {
			exe = installed
		}
	}
	command := setupSSHQuote(filepath.ToSlash(exe)) + " repo ssh " +
		setupSSHQuote(r.RemoteURL) + " " + setupSSHQuote(r.SSHURL) + " " +
		setupSSHQuote(r.SSHConfig) + " --"
	if runtime.GOOS == "windows" {
		// Git for Windows invokes core.sshCommand through its POSIX shell.
		// The arguments include server paths, which must reach tfsaz verbatim.
		command = "MSYS2_ARG_CONV_EXCL='*' " + command
	}
	return command, nil
}

func renderSetupSSH(r setupRecord) ([]byte, error) {
	_, native, err := setupSSHURLs(r.RemoteURL, r.SSHURL)
	if err != nil {
		return nil, err
	}
	// The file records the endpoint for this checkout. Git's helper passes
	// the verified native target to ordinary OpenSSH, preserving its normal
	// Host/Match processing and platform-specific configuration directories.
	var config strings.Builder
	fmt.Fprintf(&config, "Host %s\n    HostName %s\n", setupSyntheticSSHHost,
		strings.ReplaceAll(native.Hostname(), "%", "%%"))
	if native.Port() != "" {
		fmt.Fprintf(&config, "    Port %s\n", native.Port())
	}
	if native.User != nil {
		fmt.Fprintf(&config, "    User %s\n", native.User.Username())
	}
	return []byte(config.String()), nil
}

func setupSSHURLs(remoteURL, sshURL string) (synthetic, native *url.URL, err error) {
	synthetic, err = parseSetupSSHURL(remoteURL)
	if err != nil || synthetic.Host != setupSyntheticSSHHost || synthetic.User == nil ||
		synthetic.User.Username() != "git" || !setupSyntheticSSHPath(synthetic.Path) {
		return nil, nil, errors.New("invalid synthetic repository SSH URL")
	}
	if parts, ok := pathSegments(synthetic); !ok || len(parts) != 4 || parts[0] != "v3" || strings.HasSuffix(synthetic.Path, "/") {
		return nil, nil, errors.New("invalid synthetic repository SSH path")
	}

	native, err = parseSetupSSHURL(sshURL)
	if err != nil {
		return nil, nil, errors.New("invalid native repository SSH URL")
	}
	parts, ok := pathSegments(native)
	if !ok || len(parts) < 2 || parts[len(parts)-2] != "_git" || strings.HasSuffix(native.Path, "/") {
		return nil, nil, errors.New("invalid native repository SSH path")
	}
	return synthetic, native, nil
}

func parseSetupSSHURL(raw string) (*url.URL, error) {
	invalid := errors.New("invalid SSH URL")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ssh" || u.Host == "" || u.ForceQuery ||
		u.RawQuery != "" || strings.Contains(raw, "#") || setupSSHHasControl(u.Path) ||
		!strings.HasPrefix(u.Path, "/") {
		return nil, invalid
	}
	if !validSetupSSHHost(u.Hostname()) {
		return nil, invalid
	}
	if strings.HasSuffix(u.Host, ":") || (u.Port() != "" && !setupSSHValidPort(u.Port())) {
		return nil, invalid
	}
	if u.User != nil {
		if _, password := u.User.Password(); password || !setupSSHUserName.MatchString(u.User.Username()) {
			return nil, invalid
		}
	}
	return u, nil
}

func validSetupSSHHost(host string) bool {
	if setupSSHHostName.MatchString(host) {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && (ip.Zone() == "" || setupSSHHostName.MatchString(ip.Zone()))
}

func setupSSHHasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

func setupSSHValidPort(port string) bool {
	n, err := strconv.ParseUint(port, 10, 16)
	return err == nil && n != 0
}

func setupSyntheticSSHPath(path string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimLeft(path, "/")), "v3/tfsaz-")
}

// Git quotes the decoded path with its sq_quote_buf convention. Keep the
// spelling Git uses for both single quotes and exclamation marks.
func setupSSHQuote(s string) string {
	q := strings.NewReplacer("'", "'\\''", "!", "'\\!'")
	return "'" + q.Replace(s) + "'"
}

func parseSetupSSHService(command string) (service, path string, ok bool) {
	service, quoted, _ := strings.Cut(command, " ")
	switch service {
	case "git-upload-pack", "git-receive-pack", "git-upload-archive":
	default:
		return "", "", false
	}
	if len(quoted) < 2 || quoted[0] != '\'' || quoted[len(quoted)-1] != '\'' {
		return "", "", false
	}
	unquote := strings.NewReplacer("'\\''", "'", "'\\!'", "!")
	path = unquote.Replace(quoted[1 : len(quoted)-1])
	return service, path, quoted == setupSSHQuote(path)
}

func runSetupSSH(ctx context.Context, args []string) error {
	check := len(args) > 4 && args[4] == "--check"
	if check {
		args = append(append([]string(nil), args[:4]...), args[5:]...)
	}
	sshArgs, probe, err := setupSSHArgs(args, check)
	if err != nil || probe {
		return err
	}
	if check {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, setupSSHCheckTimeout)
		defer cancel()
		sshArgs = append([]string{
			"-T",
			"-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=yes",
			"-o", "PreferredAuthentications=publickey",
			"-o", "PubkeyAuthentication=yes",
			"-o", "ConnectTimeout=8",
			"-o", "ConnectionAttempts=1",
			"-o", "ControlPath=none",
			"-o", "ClearAllForwardings=yes",
			"-o", "ForwardAgent=no",
			"-o", "ForwardX11=no",
			"-o", "PermitLocalCommand=no",
			"-o", "UpdateHostKeys=no",
			"-o", "AddKeysToAgent=no",
		}, sshArgs...)
	}
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if check {
		cmd.Env = append(os.Environ(), "SSH_ASKPASS_REQUIRE=never", "LC_ALL=C")
	}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			if check && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errors.New("SSH check timed out")
			}
			return ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("ssh exited with status %d", exit.ExitCode())
		}
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("SSH executable not found on PATH")
		}
		return errors.New("could not start ssh")
	}
	return nil
}

func setupSSHArgs(args []string, check bool) (sshArgs []string, probe bool, err error) {
	invalid := errors.New("unsupported Git SSH invocation")
	if len(args) < 5 || args[3] != "--" || !filepath.IsAbs(args[2]) || setupSSHHasControl(args[2]) {
		return nil, false, invalid
	}
	synthetic, native, err := setupSSHURLs(args[0], args[1])
	if err != nil {
		return nil, false, err
	}
	config := args[2]
	args = args[4:]
	var port string
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") && args[i] != "--" {
		switch args[i] {
		case "-G":
			probe = true
			i++
		case "-4", "-6":
			sshArgs = append(sshArgs, args[i])
			i++
		case "-o":
			if i+1 == len(args) || args[i+1] != "SendEnv=GIT_PROTOCOL" {
				return nil, false, invalid
			}
			sshArgs = append(sshArgs, args[i:i+2]...)
			i += 2
		case "-p":
			if port != "" || i+1 == len(args) || !setupSSHValidPort(args[i+1]) {
				return nil, false, invalid
			}
			port = args[i+1]
			i += 2
		default:
			return nil, false, invalid
		}
	}
	if i < len(args) && args[i] == "--" {
		i++
	}
	args = args[i:]
	if len(args) == 0 {
		return nil, false, invalid
	}
	at := strings.LastIndexByte(args[0], '@')
	host := args[0][at+1:]
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if !validSetupSSHHost(host) || (at >= 0 && !setupSSHUserName.MatchString(args[0][:at])) {
		return nil, false, invalid
	}
	if probe && len(args) == 1 {
		// Git calls -G before it supplies the repo path and interprets success
		// as OpenSSH-style option support. There is no route to select yet.
		return nil, true, nil
	}
	if probe || len(args) != 2 {
		return nil, false, invalid
	}
	service, repoPath, ok := parseSetupSSHService(args[1])
	if !ok {
		return nil, false, invalid
	}

	if args[0] == "git@"+synthetic.Host && port == "" && repoPath == synthetic.Path {
		if check && service != "git-upload-pack" {
			return nil, false, invalid
		}
		if !check {
			binding := setupRecord{RemoteURL: synthetic.String(), SSHURL: native.String(), SSHConfig: config}
			expected, err := renderSetupSSH(binding)
			if err != nil {
				return nil, false, err
			}
			present, err := checkSetupSSH(binding, expected)
			if err != nil {
				return nil, false, err
			}
			if !present {
				return nil, false, errors.New("managed repository SSH configuration is missing")
			}
		}
		if native.Port() != "" {
			sshArgs = append(sshArgs, "-p", native.Port())
		}
		if native.User != nil {
			sshArgs = append(sshArgs, "-l", native.User.Username())
		}
		return append(sshArgs, "--", native.Hostname(), service+" "+setupSSHQuote(native.Path)), false, nil
	}

	if check {
		return nil, false, errors.New("SSH check destination does not match this checkout")
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if (host == setupSyntheticSSHHost || host == "ssh.dev.azure.com") && setupSyntheticSSHPath(repoPath) {
		return nil, false, errors.New("synthetic repository SSH destination does not match this checkout")
	}
	if port != "" {
		sshArgs = append(sshArgs, "-p", port)
	}
	return append(sshArgs, "--", args[0], args[1]), false, nil
}

func checkSetupSSHAccess(ctx context.Context, c setupCheckout, r setupRecord, remote string) error {
	ctx, cancel := context.WithTimeout(ctx, setupSSHCheckTimeout+3*time.Second)
	defer cancel()
	cmd := c.gitCommand(ctx, "-c", "core.sshCommand="+r.SSHCommand+" --check",
		"ls-remote", "--", remote, "HEAD")
	cmd.Env = append(cmd.Env, "GIT_ALLOW_PROTOCOL=ssh", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.WaitDelay = time.Second
	var stderr setupSSHCheckOutput
	cmd.Stderr = &stderr
	err := cmd.Run()
	switch {
	case err == nil:
		return nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return setupSSHCheckError("SSH check timed out")
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.Is(err, exec.ErrNotFound):
		return errors.New("SSH check could not start Git; install Git and ensure git is on PATH")
	default:
		return setupSSHCheckError(stderr.data.String())
	}
}

// Only use the captured output to select a fixed diagnostic. SSH and the
// server can print sensitive paths, hostnames, and arbitrary text.
type setupSSHCheckOutput struct{ data bytes.Buffer }

func (b *setupSSHCheckOutput) Write(p []byte) (int, error) {
	const maxOutput = 32 * 1024
	_, _ = b.data.Write(p[:min(len(p), maxOutput-b.data.Len())])
	return len(p), nil
}

func setupSSHCheckError(output string) error {
	output = strings.ToLower(output)
	contains := func(phrases ...string) bool {
		for _, phrase := range phrases {
			if strings.Contains(output, phrase) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("remote host identification has changed", "offending ed25519 key", "offending ecdsa key", "offending rsa key"):
		return errors.New("SSH host key changed; verify the new fingerprint with your administrator before updating known_hosts")
	case contains("host key verification failed", "host key is known", "no matching host key type"):
		return errors.New("SSH host key could not be verified; check the server fingerprint with your administrator and configure known_hosts")
	case contains("permission denied (", "too many authentication failures", "no supported authentication methods",
		"no mutual signature algorithm", "signing failed"):
		return errors.New("SSH key authentication failed; load the key into ssh-agent or configure IdentityFile, and register only the public key with Azure DevOps Server")
	case contains("ssh executable not found"):
		return errors.New("SSH check could not start OpenSSH; install OpenSSH and ensure ssh is on PATH")
	case contains("ssh check timed out"):
		return errors.New("SSH check timed out; check network access and the server's SSH service")
	case contains("could not resolve hostname", "connection refused", "timed out", "operation timeout", "no route to host", "network is unreachable", "connection reset", "connection closed"):
		return errors.New("SSH connection failed; check network access, the SSH hostname and port, and try again")
	case contains("repository not found", "does not exist", "tf401019", "access denied", "permission denied", "not authorized", "not permitted"):
		return errors.New("SSH repository access denied; verify the repository path and grant the SSH user read access")
	case contains("destination does not match"):
		return errors.New("SSH check did not reach the bound repository; check Git URL rewrites")
	default:
		return errors.New("SSH repository read failed; check the SSH configuration, repository path, and read permission")
	}
}
