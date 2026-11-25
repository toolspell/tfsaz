package az

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func sshTestRecord(t *testing.T) setupRecord {
	t.Helper()
	return setupRecord{
		RemoteURL: "ssh://git@vs-ssh.visualstudio.com/v3/tfsaz-example/My%20Project/Repo%21%27Name",
		SSHURL:    "ssh://server-user@native.invalid:2222/Second%20Collection/My%20Project/_git/Repo%21%27Name",
		SSHConfig: filepath.Join(t.TempDir(), "private ' ssh.conf"),
	}
}

func TestSetupSSHArgs(t *testing.T) {
	record := sshTestRecord(t)
	binding := []string{record.RemoteURL, record.SSHURL, record.SSHConfig, "--"}
	const synthetic = "git@vs-ssh.visualstudio.com"
	const path = `'/v3/tfsaz-example/My Project/Repo'\!''\''Name'`
	const nativePath = `'/Second Collection/My Project/_git/Repo'\!''\''Name'`
	for _, service := range []string{"git-upload-pack", "git-receive-pack", "git-upload-archive"} {
		t.Run(service, func(t *testing.T) {
			args := append(slices.Clone(binding), "-o", "SendEnv=GIT_PROTOCOL", synthetic, service+" "+path)
			got, probe, err := setupSSHArgs(args, false)
			want := []string{"-o", "SendEnv=GIT_PROTOCOL", "-F", record.SSHConfig, "-p", "2222", "-l", "server-user", "--", "native.invalid", service + " " + nativePath}
			if err != nil || probe || !slices.Equal(got, want) {
				t.Fatalf("got %q, probe %v, error %v; want %q", got, probe, err, want)
			}
		})
	}

	args := append(slices.Clone(binding), "-G", "-o", "SendEnv=GIT_PROTOCOL", synthetic)
	if got, probe, err := setupSSHArgs(args, false); got != nil || !probe || err != nil {
		t.Fatalf("probe returned %q, %v, %v", got, probe, err)
	}

	for _, destination := range []string{"git@other.invalid", "git@ssh.dev.azure.com"} {
		service := "git-upload-pack '/v3/genuine/Azure Repo/r'"
		args := append(slices.Clone(binding), "-p", "443", destination, service)
		got, probe, err := setupSSHArgs(args, false)
		want := []string{"-p", "443", "--", destination, service}
		if err != nil || probe || !slices.Equal(got, want) {
			t.Fatalf("ordinary SSH returned %q, %v, %v; want %q", got, probe, err, want)
		}
	}

	for _, invocation := range [][]string{
		{"-o", "ProxyCommand=untrusted", synthetic, "git-upload-pack " + path},
		{"--", "-invalid", "git-upload-pack " + path},
		{"untrusted$(command)@other.invalid", "git-upload-pack 'some/repo'"},
		{"-p", "22", synthetic, "git-upload-pack " + path},
		{synthetic, "git-upload-pack '/v3/tfsaz-other/My Project/Repo'"},
		{"git@SSH.DEV.AZURE.COM.", "git-upload-pack 'v3/TFSAZ-other/My Project/Repo'"},
		{synthetic, "git-upload-pack '/v3/tfsaz-example/My%20Project/Repo%21%27Name'"},
		{synthetic, "git-upload-pack '/v3/tfsaz-example/My Project/Repo'"},
		{synthetic, "git-upload-pack " + path + "; untrusted"},
		{synthetic, "custom-upload-pack " + path},
	} {
		if got, _, err := setupSSHArgs(append(slices.Clone(binding), invocation...), false); err == nil {
			t.Errorf("accepted %q as %q", invocation, got)
		}
	}
	for _, invocation := range [][]string{
		{synthetic, "git-receive-pack " + path},
		{"git@other.invalid", "git-upload-pack " + path},
	} {
		if _, _, err := setupSSHArgs(append(slices.Clone(binding), invocation...), true); err == nil {
			t.Errorf("read-only check accepted %q", invocation)
		}
	}
}

func TestRenderSetupSSHIncludesNativeHostConfig(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH is unavailable")
	}
	record := sshTestRecord(t)
	record.SSHURL = "ssh://native.invalid/Collection/Project/_git/Repo"
	home := filepath.Join(t.TempDir(), "home with [brackets]")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	config := "Host native.invalid\n    User from-user-config\n    HostKeyAlias native-host-key\n"
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := renderSetupSSH(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.SSHConfig, data, 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(ssh, "-G", "-F", record.SSHConfig, "--", "native.invalid").CombinedOutput()
	settings := strings.ReplaceAll(string(output), "\r\n", "\n")
	if err != nil || !strings.Contains(settings, "\nuser from-user-config\n") ||
		!strings.Contains(settings, "\nhostkeyalias native-host-key\n") {
		t.Fatalf("ssh -G did not load the native Host settings: %v\n%s", err, output)
	}
	record = sshTestRecord(t)
	data, err = renderSetupSSH(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.SSHConfig, data, 0600); err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command(ssh, "-G", "-F", record.SSHConfig, "--", setupSyntheticSSHHost).CombinedOutput()
	settings = strings.ReplaceAll(string(output), "\r\n", "\n")
	if err != nil || !strings.Contains(settings, "\nhostname native.invalid\n") ||
		!strings.Contains(settings, "\nuser server-user\n") || !strings.Contains(settings, "\nport 2222\n") {
		t.Fatalf("ssh -G did not resolve the synthetic alias: %v\n%s", err, output)
	}
}

func TestSetupSSHCheckDiagnostics(t *testing.T) {
	for _, tt := range []struct{ stderr, want string }{
		{"Permission denied (publickey).", "ssh-agent"},
		{"No ED25519 host key is known", "could not be verified"},
		{"REMOTE HOST IDENTIFICATION HAS CHANGED", "host key changed"},
		{"connect to host: Connection refused", "SSH connection failed"},
		{"SSH check timed out", "SSH check timed out"},
		{"TF401019: repository does not exist", "repository access denied"},
		{"SSH executable not found on PATH", "install OpenSSH"},
		{"unexpected server stderr", "repository read failed"},
	} {
		got := setupSSHCheckError("fixture-private-detail: " + tt.stderr).Error()
		if !strings.Contains(got, tt.want) || strings.Contains(got, "fixture-private-detail") {
			t.Errorf("%q produced %q, want %q", tt.stderr, got, tt.want)
		}
	}
}

func TestSetupSSHGitLSRemote(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh executable uses a POSIX shell script")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is unavailable")
	}
	record := sshTestRecord(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "tfsaz with ' spaces!")
	build := exec.Command("go", "build", "-o", bin, "../..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build tfsaz: %v\n%s", err, output)
	}

	// Generate a real core.sshCommand using the test binary's location,
	// then use the built CLI to exercise the production command dispatch.
	command, err := setupSSHCommand(record)
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command = setupSSHQuote(bin) + strings.TrimPrefix(command, setupSSHQuote(exe))

	script := `#!/bin/sh
printf '%s\n' "$GIT_PROTOCOL" "$@" > "$TFSAZ_TEST_TRACE"
printf '0000'
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(dir, "trace")
	var envs []string
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if !strings.HasPrefix(key, "GIT_") && key != "PATH" && key != "TFSAZ_CONFIG" {
			envs = append(envs, env)
		}
	}
	envs = append(envs, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "TFSAZ_TEST_TRACE="+trace,
		"TFSAZ_CONFIG="+filepath.Join(dir, "unavailable.json"))
	lsRemote := func(remote string) ([]byte, error) {
		cmd := exec.Command(git, "-c", "protocol.version=2", "-c", "core.sshCommand="+command, "ls-remote", remote)
		cmd.Dir, cmd.Env = dir, envs
		return cmd.CombinedOutput()
	}
	for _, tt := range []struct {
		remote string
		want   []string
	}{
		{record.RemoteURL, []string{"version=2", "-o", "SendEnv=GIT_PROTOCOL", "-F", record.SSHConfig, "-p", "2222",
			"-l", "server-user", "--", "native.invalid", `git-upload-pack '/Second Collection/My Project/_git/Repo'\!''\''Name'`}},
		{"ssh://git@other.invalid:443/ordinary%20repo", []string{"version=2", "-o", "SendEnv=GIT_PROTOCOL",
			"-p", "443", "--", "git@other.invalid", "git-upload-pack '/ordinary repo'"}},
	} {
		if output, err := lsRemote(tt.remote); err != nil {
			t.Fatalf("git ls-remote %s: %v\n%s", tt.remote, err, output)
		}
		log, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Split(strings.TrimSuffix(string(log), "\n"), "\n"); !slices.Equal(got, tt.want) {
			t.Fatalf("fake SSH got %q; want %q", got, tt.want)
		}
		if err := os.Remove(trace); err != nil {
			t.Fatal(err)
		}
	}
	output, err := lsRemote("ssh://git@vs-ssh.visualstudio.com/v3/tfsaz-unknown/project/repo")
	if err == nil || !strings.Contains(string(output), "synthetic repository SSH destination does not match") {
		t.Fatalf("unexpected result for unknown synthetic organization: %v\n%s", err, output)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("unexpected SSH trace for unknown synthetic organization: %v", err)
	}
}
