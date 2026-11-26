package az

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The helper must honor the same protocol policy Git applies to the URL it
// replaces, or a managed origin would bypass a user's or admin's block.
func TestPermitPreserveTransport(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, tc := range []struct {
		name    string
		allow   *string // GIT_ALLOW_PROTOCOL
		config  []string
		user    *string // GIT_PROTOCOL_FROM_USER
		allowed bool
	}{
		{name: "no policy", allowed: true},
		{name: "allow list with the scheme", allow: new("file:ssh"), config: []string{"protocol.ssh.allow", "never"}, allowed: true},
		{name: "allow list without the scheme", allow: new("https"), allowed: false},
		{name: "empty allow list", allow: new(""), allowed: false},
		{name: "scheme never", config: []string{"protocol.ssh.allow", "never"}, allowed: false},
		{name: "scheme always over global never", config: []string{"protocol.allow", "never", "protocol.ssh.allow", "always"}, allowed: true},
		{name: "global never", config: []string{"protocol.allow", "never"}, allowed: false},
		{name: "user without the variable", config: []string{"protocol.ssh.allow", "user"}, allowed: true},
		{name: "user from a user", config: []string{"protocol.ssh.allow", "user"}, user: new("1"), allowed: true},
		{name: "user from a script", config: []string{"protocol.ssh.allow", "user"}, user: new("0"), allowed: false},
		{name: "unknown policy", config: []string{"protocol.ssh.allow", "sometimes"}, allowed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The helper reads the policy outside any checkout.
			t.Chdir(t.TempDir())
			global := filepath.Join(t.TempDir(), "gitconfig")
			managerTestWrite(t, global, "")
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			for i := 0; i < len(tc.config); i += 2 {
				managerTestGit(t, t.TempDir(), "config", "--file", global, tc.config[i], tc.config[i+1])
			}
			for name, value := range map[string]*string{"GIT_ALLOW_PROTOCOL": tc.allow, "GIT_PROTOCOL_FROM_USER": tc.user} {
				t.Setenv(name, "")
				if value == nil {
					if err := os.Unsetenv(name); err != nil {
						t.Fatal(err)
					}
				} else {
					t.Setenv(name, *value)
				}
			}
			err := permitPreserveTransport(t.Context(), "ssh")
			if tc.allowed != (err == nil) {
				t.Fatalf("got %v, want allowed %v", err, tc.allowed)
			}
			if err != nil && !strings.Contains(err.Error(), "disallows the bound ssh transport") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPreserveSSHConnect(t *testing.T) {
	const path = "/tfs/Collection/Team Project/_git/Tool Kit"
	quoted := setupSSHQuote(path)
	withPort := &url.URL{Scheme: "ssh", User: url.User("git"), Host: "server.invalid:2222", Path: path}
	noPort := &url.URL{Scheme: "ssh", User: url.User("git"), Host: "server.invalid", Path: path}
	for _, tc := range []struct {
		name     string
		ssh      preserveSSH
		target   *url.URL
		service  string
		args     []string
		protocol string // GIT_PROTOCOL for the child, if any
		err      string
	}{
		{
			name: "OpenSSH fetch", ssh: preserveSSH{command: "ssh", version: "2"}, target: withPort, service: "git-upload-pack",
			args:     []string{"-o", "SendEnv=GIT_PROTOCOL", "-p", "2222", "git@server.invalid", "git-upload-pack " + quoted},
			protocol: "version=2",
		},
		{
			name: "OpenSSH push falls back to protocol 0", ssh: preserveSSH{command: "ssh", version: "2"}, target: noPort,
			service: "git-receive-pack", args: []string{"git@server.invalid", "git-receive-pack " + quoted},
		},
		{
			name: "OpenSSH push with protocol 1", ssh: preserveSSH{command: "/usr/bin/ssh", version: "1"}, target: noPort,
			service: "git-receive-pack", args: []string{"-o", "SendEnv=GIT_PROTOCOL", "git@server.invalid", "git-receive-pack " + quoted},
			protocol: "version=1",
		},
		{
			name: "plink by name", ssh: preserveSSH{command: "/opt/PuTTY/PLINK.EXE", version: "2"}, target: withPort,
			service: "git-upload-pack", args: []string{"-P", "2222", "git@server.invalid", "git-upload-pack " + quoted},
		},
		{
			name: "TortoisePlink by name", ssh: preserveSSH{command: "/opt/TortoiseGit/TortoisePlink.exe", version: "2"}, target: withPort,
			service: "git-upload-pack", args: []string{"-batch", "-P", "2222", "git@server.invalid", "git-upload-pack " + quoted},
		},
		{
			name: "explicit putty variant", ssh: preserveSSH{command: "/opt/custom-ssh", variant: "putty", version: "2"}, target: withPort,
			service: "git-upload-pack", args: []string{"-P", "2222", "git@server.invalid", "git-upload-pack " + quoted},
		},
		{
			name: "simple variant", ssh: preserveSSH{command: "/opt/custom-ssh", variant: "simple", version: "2"}, target: noPort,
			service: "git-upload-pack", args: []string{"git@server.invalid", "git-upload-pack " + quoted},
		},
		{
			name: "simple variant with a port", ssh: preserveSSH{command: "/opt/custom-ssh", variant: "simple", version: "2"},
			target: withPort, service: "git-upload-pack", err: "does not support an explicit port",
		},
		{
			name: "unknown variant", ssh: preserveSSH{command: "/opt/custom-ssh", variant: "other", version: "2"},
			target: noPort, service: "git-upload-pack", err: "unsupported Git ssh.variant",
		},
		{
			name: "unknown protocol version", ssh: preserveSSH{command: "ssh", version: "3"},
			target: noPort, service: "git-upload-pack", err: "unsupported Git protocol.version",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := tc.ssh.connect(t.Context(), tc.target, tc.service)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %v, want an error with %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(cmd.Args[1:], tc.args) {
				t.Errorf("args %q, want %q", cmd.Args[1:], tc.args)
			}
			var protocol string
			for _, entry := range cmd.Env {
				if value, ok := strings.CutPrefix(entry, "GIT_PROTOCOL="); ok {
					protocol = value
				}
			}
			if protocol != tc.protocol {
				t.Errorf("GIT_PROTOCOL %q, want %q", protocol, tc.protocol)
			}
		})
	}
}

// An SSH command Git cannot name is OpenSSH only if it accepts ssh -G.
func TestPreserveSSHVariantDetection(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	t.Chdir(t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	target := &url.URL{Scheme: "ssh", User: url.User("git"), Host: "server.invalid", Path: "/Repo"}
	for _, tc := range []struct{ name, command, want string }{
		{"accepts -G", "f() { exit 0; }; f", "ssh"},
		{"rejects -G", "f() { exit 1; }; f", "simple"},
		{"named plink", "plink -v", "plink"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := preserveSSH{command: tc.command, shell: true, variant: "auto", local: []string{"GIT_DIR"}}
			if got := s.variantFor(t.Context(), target, nil); got != tc.want {
				t.Fatalf("variant %q, want %q", got, tc.want)
			}
		})
	}
}
