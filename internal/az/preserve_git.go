package az

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// RunPreserveGitHelper is Git's per-remote entry point. It accepts only the
// repository alias and target committed together in the common Git config.
func RunPreserveGitHelper(ctx context.Context, args []string) error {
	if slices.Equal(args, []string{"--version"}) {
		_, err := fmt.Fprintln(os.Stdout, preserveHelperVersion)
		return err
	}
	if len(args) != 2 || args[0] != "origin" {
		return errors.New("git-remote-tfsaz requires a managed origin")
	}
	binding, err := readPreserveGitBinding(ctx, args[1])
	if err != nil {
		return err
	}
	target, alias, err := binding.destination()
	if err != nil {
		return err
	}
	if alias != args[1] {
		return errors.New("Git origin does not match its managed transport binding")
	}
	if err := permitPreserveTransport(ctx, target.Scheme); err != nil {
		return err
	}
	if target.Scheme == "ssh" {
		return runPreserveSSH(ctx, target)
	}
	// Git's remote-http and remote-https commands run the same HTTP
	// transport. The URL selects TLS, and remote-http also works in route run
	// environments where remote-https is the older tfsaz overlay.
	cmd := exec.CommandContext(ctx, "git", "remote-http", args[0], binding.Target)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func readPreserveGitBinding(ctx context.Context, alias string) (preserveBinding, error) {
	var binding preserveBinding
	// Git sets GIT_DIR to the checkout or linked worktree before starting
	// a remote helper. --local reads the shared config even in a worktree.
	const managedKeys = `^remote\.origin\.(url|vcs|tfsazbinding)$`
	out, err := exec.CommandContext(ctx, "git", "config", "--local", "--no-includes", "--null", "--get-regexp", managedKeys).Output()
	if err != nil {
		return binding, errors.New("could not read the repository's managed Git transport binding")
	}
	values := make(map[string][]string)
	for entry := range strings.SplitSeq(string(out), "\x00") {
		key, value, _ := strings.Cut(entry, "\n")
		switch key {
		case setupOriginKey, setupVCSKey, strings.ToLower(setupBindingKey):
			values[key] = append(values[key], value)
		}
	}
	if !slices.Equal(values[setupOriginKey], []string{alias}) || !slices.Equal(values[setupVCSKey], []string{"tfsaz"}) {
		return binding, errors.New("Git origin does not match its managed transport binding")
	}
	raw := values[strings.ToLower(setupBindingKey)]
	if len(raw) != 1 || json.Unmarshal([]byte(raw[0]), &binding) != nil {
		return binding, errors.New("invalid or missing managed Git transport binding")
	}
	return binding, nil
}

func permitPreserveTransport(ctx context.Context, scheme string) error {
	blocked := fmt.Errorf("Git policy disallows the bound %s transport", scheme)
	if allow, ok := os.LookupEnv("GIT_ALLOW_PROTOCOL"); ok {
		if !slices.Contains(strings.Split(allow, ":"), scheme) {
			return blocked
		}
		return nil
	}
	policy, err := preserveGitValue(ctx, "", "protocol."+scheme+".allow")
	if err != nil {
		return err
	}
	if policy == "" {
		policy, err = preserveGitValue(ctx, "", "protocol.allow")
		if err != nil {
			return err
		}
	}
	switch policy {
	case "", "always":
		return nil
	case "user":
		allowed, found := os.LookupEnv("GIT_PROTOCOL_FROM_USER")
		number, _ := strconv.Atoi(allowed)
		if !found || number != 0 || slices.Contains([]string{"yes", "on", "true"}, strings.ToLower(allowed)) {
			return nil
		}
	}
	return blocked
}

func runPreserveSSH(ctx context.Context, target *url.URL) error {
	ssh, err := loadPreserveSSH(ctx, "")
	if err != nil {
		return err
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil
		}
		if err != nil {
			return errors.New("read Git remote-helper request")
		}
		switch string(line) {
		case "\n":
			return nil
		case "capabilities\n":
			if _, err := io.WriteString(os.Stdout, "connect\n\n"); err != nil {
				return err
			}
		case "connect git-upload-pack\n", "connect git-receive-pack\n", "connect git-upload-archive\n":
			service := strings.TrimSuffix(string(line[len("connect "):]), "\n")
			cmd, err := ssh.connect(ctx, target, service)
			if err != nil {
				return err
			}
			// Git waits for the acknowledgement before sending pack traffic.
			// Inherit its pipe directly so it remains full duplex.
			if reader.Buffered() != 0 {
				return errors.New("Git remote-helper input arrived before connect acknowledgement")
			}
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if _, err := io.WriteString(os.Stdout, "\n"); err != nil {
				return err
			}
			return cmd.Run()
		default:
			return errors.New("unsupported Git remote-helper request")
		}
	}
}

type preserveSSH struct {
	command string
	shell   bool
	variant string
	version string
	env     []string
	local   []string
	cwd     string
}

func loadPreserveSSH(ctx context.Context, cwd string) (preserveSSH, error) {
	s := preserveSSH{cwd: cwd}
	var err error
	s.command, s.shell = os.LookupEnv("GIT_SSH_COMMAND")
	if !s.shell {
		s.command, err = preserveGitValue(ctx, cwd, "core.sshCommand")
		if err != nil {
			return s, err
		}
		s.shell = s.command != ""
	}
	if !s.shell {
		s.command = os.Getenv("GIT_SSH")
		if s.command == "" {
			s.command = "ssh"
		}
	}
	if s.command == "" {
		return s, errors.New("Git SSH command is empty")
	}
	if s.variant = os.Getenv("GIT_SSH_VARIANT"); s.variant == "" {
		s.variant, err = preserveGitValue(ctx, cwd, "ssh.variant")
		if err != nil {
			return s, err
		}
	}
	s.variant = strings.ToLower(s.variant)
	s.version, err = preserveGitValue(ctx, cwd, "protocol.version")
	if err != nil {
		return s, err
	}
	if s.version == "" {
		s.version = "2"
	}
	local, err := exec.CommandContext(ctx, "git", "rev-parse", "--local-env-vars").Output()
	if err != nil {
		return s, errors.New("could not determine Git's repository-local environment")
	}
	s.local = strings.Fields(string(local))
	s.env = slices.DeleteFunc(os.Environ(), func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return slices.Contains(s.local, strings.ToUpper(key))
	})
	return s, nil
}

func (s preserveSSH) commandFor(ctx context.Context, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if !s.shell {
		cmd = exec.CommandContext(ctx, s.command, args...)
	} else {
		// Git supplies its own shell and appends the SSH arguments. Its
		// alias runner sets repo-local variables again in linked worktrees.
		alias := "tfsaz-native-ssh"
		shell := "unset " + strings.Join(s.local, " ") + "; " + s.command
		gitArgs := append([]string{"-c", "alias." + alias + "=!" + shell, alias}, args...)
		cmd = exec.CommandContext(ctx, "git", gitArgs...)
	}
	cmd.Env = s.env
	cmd.Dir = s.cwd
	if s.cwd != "" {
		// Setup checks have an explicit checkout and no controlling terminal,
		// including any SSH children run by Match exec or a proxy command.
		cmd.Env = append(cmd.Env, "SSH_ASKPASS_REQUIRE=never")
		detachPreserveSSHProbe(cmd)
	}
	return cmd
}

func preserveSSHHost(target *url.URL) string {
	if target.User != nil {
		return target.User.Username() + "@" + target.Hostname()
	}
	return target.Hostname()
}

func (s preserveSSH) openSSHOptions(target *url.URL, service string) ([]string, string, error) {
	version := s.version
	if version == "2" && service != "git-upload-pack" {
		version = "0"
	}
	if version != "0" && version != "1" && version != "2" {
		return nil, "", errors.New("unsupported Git protocol.version")
	}
	openSSHOptions := []string{}
	if version != "0" {
		openSSHOptions = append(openSSHOptions, "-o", "SendEnv=GIT_PROTOCOL")
	}
	if target.Port() != "" {
		openSSHOptions = append(openSSHOptions, "-p", target.Port())
	}
	return openSSHOptions, version, nil
}

func (s preserveSSH) variantFor(ctx context.Context, target *url.URL, openSSHOptions []string) string {
	if s.variant != "" && s.variant != "auto" {
		return s.variant
	}
	command := s.command
	if s.shell {
		command, _, _ = strings.Cut(command, " ")
	}
	switch name := strings.TrimSuffix(strings.ToLower(filepath.Base(command)), ".exe"); name {
	case "ssh", "plink", "tortoiseplink":
		return name
	}
	detect := s.commandFor(ctx, append(append([]string{"-G"}, openSSHOptions...), preserveSSHHost(target))...)
	if detect.Run() != nil {
		return "simple"
	}
	return "ssh"
}

func (s preserveSSH) connect(ctx context.Context, target *url.URL, service string) (*exec.Cmd, error) {
	openSSHOptions, version, err := s.openSSHOptions(target, service)
	if err != nil {
		return nil, err
	}
	variant := s.variantFor(ctx, target, openSSHOptions)
	var args []string
	switch variant {
	case "ssh":
		args = openSSHOptions
	case "plink", "putty", "tortoiseplink":
		if variant == "tortoiseplink" {
			args = append(args, "-batch")
		}
		if target.Port() != "" {
			args = append(args, "-P", target.Port())
		}
	case "simple":
		if target.Port() != "" {
			return nil, errors.New("the simple Git SSH variant does not support an explicit port")
		}
	default:
		return nil, errors.New("unsupported Git ssh.variant")
	}
	cmd := s.commandFor(ctx, append(args, preserveSSHHost(target), service+" "+setupSSHQuote(target.Path))...)
	if variant == "ssh" && version != "0" {
		cmd.Env = append(cmd.Environ(), "GIT_PROTOCOL=version="+version)
	}
	return cmd, nil
}

func (s preserveSSH) checkCommand(ctx context.Context, target *url.URL) (command, variant string, err error) {
	options, _, err := s.openSSHOptions(target, "git-upload-pack")
	if err != nil {
		return "", "", err
	}
	variant = s.variantFor(ctx, target, options)
	command = s.command
	if !s.shell {
		command = setupSSHQuote(filepath.ToSlash(command))
	}
	switch variant {
	case "ssh":
		strict := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes"}
		args := append(append([]string{"-G"}, strict...), options...)
		config, err := s.commandFor(ctx, append(args, preserveSSHHost(target))...).Output()
		if err != nil {
			return "", "", errors.New("SSH check could not read OpenSSH settings; check the Git SSH command")
		}
		settings := "\n" + strings.ReplaceAll(string(config), "\r\n", "\n")
		if !strings.Contains(settings, "\nbatchmode yes\n") ||
			(!strings.Contains(settings, "\nstricthostkeychecking true\n") &&
				!strings.Contains(settings, "\nstricthostkeychecking yes\n")) {
			return "", "", errors.New("SSH check requires BatchMode=yes and StrictHostKeyChecking=yes; check the Git SSH command")
		}
		command += " " + strings.Join(strict, " ")
	case "plink", "putty":
		command += " -batch"
	case "tortoiseplink":
		// Git adds -batch for this variant.
	default:
		return "", "", errors.New("SSH check requires an OpenSSH or PuTTY Git SSH command")
	}
	return command, variant, nil
}
