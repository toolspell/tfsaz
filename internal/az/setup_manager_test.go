package az

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const managerTestOrigin = "ssh://git@server.example:2222/tfs/Collection/Project/_git/Repository"

func managerTestGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func managerTestWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

type managerTestCLI struct {
	executable string
	cwd        string
	stdout     *bytes.Buffer
}

func (a managerTestCLI) runRepo(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, a.executable, append([]string{"repo"}, args...)...)
	var stderr bytes.Buffer
	cmd.Dir, cmd.Stdout, cmd.Stderr = a.cwd, a.stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func managerTestRepo(t *testing.T) (managerTestCLI, *bytes.Buffer, setupCheckout) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	bin := testExecutable(filepath.Join(root, "tfsaz"))
	if output, err := exec.Command("go", "build", "-tags", "tfsaztest", "-o", bin, "../..").CombinedOutput(); err != nil {
		t.Fatalf("build tfsaz: %v\n%s", err, output)
	}
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$TFSAZ_TEST_SSH_TRACE"
if [ "$SSH_ASKPASS_REQUIRE" != never ]; then exit 90; fi
for option in BatchMode=yes StrictHostKeyChecking=yes PreferredAuthentications=publickey; do
    case " $* " in
        *" -o $option "*) ;;
        *) exit 91 ;;
    esac
done
case " $* " in
    *" -- server.example git-upload-pack '/tfs/Collection/Project/_git/Repository'"*) ;;
    *) exit 92 ;;
esac
if [ -n "$TFSAZ_TEST_GIT_WRITE" ]; then
    git config --local tfsaz.writtenduringprobe yes || exit 93
fi
if [ -n "$TFSAZ_TEST_PUBLIC_KEY" ]; then
    if ! "$TFSAZ_TEST_SSH_ADD" -T "$TFSAZ_TEST_PUBLIC_KEY" 2>/dev/null; then
        printf '%s\n' 'fixture-private-detail: Permission denied (publickey).' >&2
        exit 255
    fi
fi
if [ -n "$TFSAZ_TEST_SSH_FAILURE" ]; then
    printf '%s\n' "$TFSAZ_TEST_SSH_FAILURE" >&2
    exit 255
fi
printf '0000'
`
	setupTestSSH(t, root, script)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TFSAZ_TEST_SSH_TRACE", filepath.Join(root, "ssh.trace"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "global.gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	path := filepath.Join(root, "settings.json")
	t.Setenv("TFSAZ_CONFIG", path)
	managerTestWrite(t, path, `{"profiles":[{"id":"test","organization":"https://dev.azure.com/example","collectionUrl":"https://server.example/tfs/Collection"}]}`)
	repo := filepath.Join(root, "checkout with spaces")
	managerTestGit(t, root, "init", repo)
	managerTestGit(t, repo, "remote", "add", "origin", managerTestOrigin)
	c, err := findSetupCheckout(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	return managerTestCLI{executable: bin, cwd: repo, stdout: &out}, &out, c
}

func managerTestRun(t *testing.T, a managerTestCLI, args ...string) {
	t.Helper()
	if err := a.runRepo(t.Context(), args); err != nil {
		t.Fatalf("repo %v: %v", args, err)
	}
}

func managerTestRecords(t *testing.T) []setupRecord {
	t.Helper()
	records, err := readSetupRecords()
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func managerTestState(t *testing.T, c setupCheckout, want setupGitState) {
	t.Helper()
	if actual, err := c.localState(t.Context()); err != nil || actual != want {
		t.Fatalf("Git state = %#v, %v, want %#v", actual, err, want)
	}
}

func TestRepoSetupRoundTripSharedWorktrees(t *testing.T) {
	a, out, c := managerTestRepo(t)
	managerTestGit(t, c.repo, "-c", "user.name=Tester", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Initial")
	linked := filepath.Join(filepath.Dir(c.repo), "linked")
	managerTestGit(t, c.repo, "worktree", "add", "-b", "linked", linked)

	managerTestRun(t, a, "setup", "--managed-ssh", "--profile=test", linked)
	records := managerTestRecords(t)
	if len(records) != 1 || records[0].Phase != setupPhaseActive || !sameSetupPath(records[0].GitDir, c.gitDir) {
		t.Fatalf("records = %+v", records)
	}
	r := records[0]
	original, active := setupStates(r)
	managerTestState(t, c, active)
	managerTestRun(t, a, "setup")
	if err := a.runRepo(t.Context(), []string{"setup", "--preserve-transport"}); err == nil || !strings.Contains(err.Error(), "different transport mode") {
		t.Fatalf("preserve transport replaced existing managed SSH: %v", err)
	}
	managerTestRun(t, a, "list", c.repo)
	managerTestRun(t, a, "verify")
	if !strings.Contains(out.String(), r.Host) || !strings.Contains(out.String(), "verified") {
		t.Fatalf("unexpected command output %q", out)
	}
	ssh, err := os.Stat(r.SSHConfig)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && ssh.Mode().Perm() != 0600 {
		t.Fatalf("generated SSH config mode %o", ssh.Mode().Perm())
	}
	managerTestGit(t, c.repo, "config", "user.name", "Concurrent edit")
	managerTestRun(t, a, "restore")
	managerTestState(t, c, original)
	if values, err := c.configValues(t.Context(), "", "user.name"); err != nil || strings.Join(values, "") != "Concurrent edit" {
		t.Fatalf("unrelated Git config = %v, %v", values, err)
	}
	if _, err := os.Stat(r.SSHConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generated SSH config remains: %v", err)
	}
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("records remain: %+v", records)
	}
	registry, err := setupRegistryPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, lock := range []string{registry + ".lock", filepath.Join(c.gitDir, "tfsaz-repo-setup.lock")} {
		if _, err := os.Lstat(lock); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restore left the lock file %s: %v", lock, err)
		}
	}
}

func TestRepoSetupResolvesSurvivingCheckout(t *testing.T) {
	a, out, c := managerTestRepo(t)
	managerTestGit(t, c.repo, "-c", "user.name=Tester", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Initial")
	linked := filepath.Join(filepath.Dir(c.repo), "temporary worktree")
	managerTestGit(t, c.repo, "worktree", "add", "-b", "linked", linked)
	managerTestRun(t, a, "setup", "--managed-ssh", linked)
	r := managerTestRecords(t)[0]
	// Leave the registration behind so Git can list a reused path.
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	managerTestRun(t, a, "verify")
	if err := os.Mkdir(linked, 0700); err != nil {
		t.Fatal(err)
	}
	managerTestRun(t, a, "verify")
	managerTestGit(t, filepath.Dir(linked), "init", linked)

	out.Reset()
	managerTestRun(t, a, "verify")
	if got := out.String(); got != fmt.Sprintf("verified %s\n", c.repo) {
		t.Fatalf("verify output = %q", got)
	}
	out.Reset()
	managerTestRun(t, a, "list")
	if got := out.String(); !strings.Contains(got, "\t"+c.repo+"\t") {
		t.Fatalf("list output = %q", got)
	}
	if err := a.runRepo(t.Context(), []string{"verify", linked}); err == nil || !strings.Contains(err.Error(), "no setup recorded") {
		t.Fatalf("verifying unrelated replacement: %v", err)
	}
	if got := managerTestRecords(t)[0]; got != r {
		t.Fatalf("registry record changed: %+v", got)
	}
}

func TestRepoSetupResolvesMovedWorktreeWithBarePrimary(t *testing.T) {
	a, out, c := managerTestRepo(t)
	managerTestGit(t, c.repo, "-c", "user.name=Tester", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Initial")
	root := filepath.Dir(c.repo)
	bare := filepath.Join(root, "common.git")
	managerTestGit(t, root, "clone", "--bare", c.repo, bare)
	managerTestGit(t, bare, "remote", "set-url", "origin", managerTestOrigin)
	linked := filepath.Join(root, "temporary worktree")
	moved := filepath.Join(root, "moved worktree")
	managerTestGit(t, bare, "worktree", "add", "-b", "linked", linked)
	managerTestRun(t, a, "setup", "--managed-ssh", linked)
	managerTestGit(t, bare, "worktree", "move", linked, moved)
	resolved, err := filepath.EvalSymlinks(moved)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	managerTestRun(t, a, "verify")
	if got := out.String(); got != fmt.Sprintf("verified %s\n", resolved) {
		t.Fatalf("verify output = %q", got)
	}
	out.Reset()
	managerTestRun(t, a, "list")
	if got := out.String(); !strings.Contains(got, "\t"+resolved+"\t") {
		t.Fatalf("list output = %q", got)
	}
	managerTestRun(t, a, "restore", moved)
}

func TestRepoSetupResolvesCheckoutWithSeparateGitDirectory(t *testing.T) {
	a, out, c := managerTestRepo(t)
	root := filepath.Dir(c.repo)
	primary := filepath.Join(root, "separate checkout")
	common := filepath.Join(root, "separate metadata")
	managerTestGit(t, root, "init", "--separate-git-dir", common, primary)
	managerTestGit(t, primary, "remote", "add", "origin", managerTestOrigin)
	managerTestGit(t, primary, "-c", "user.name=Tester", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Initial")
	linked := filepath.Join(root, "temporary worktree")
	surviving := filepath.Join(root, "surviving worktree")
	managerTestGit(t, primary, "worktree", "add", "-b", "linked", linked)
	managerTestGit(t, primary, "worktree", "add", "-b", "surviving", surviving)
	managerTestRun(t, a, "setup", "--managed-ssh", linked)
	managerTestGit(t, primary, "worktree", "remove", linked)

	out.Reset()
	managerTestRun(t, a, "verify")
	if got := out.String(); got != fmt.Sprintf("verified %s\n", surviving) {
		t.Fatalf("verify output = %q", got)
	}
}

func TestRepoSetupSSHKeyCheckAndOfflineRestore(t *testing.T) {
	var toolsDir string
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git unavailable")
		}
		// Use the Git for Windows tools together. Windows OpenSSH uses the
		// system agent service, while Git's agent creates a disposable socket.
		toolsDir = testWindowsGitTools(t)
	}
	tools := make(map[string]string)
	for _, name := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		path := name
		if toolsDir != "" {
			path = testExecutable(filepath.Join(toolsDir, name))
		}
		executable, err := exec.LookPath(path)
		if err != nil {
			t.Skipf("%s unavailable", name)
		}
		tools[name] = executable
	}
	a, _, c := managerTestRepo(t)
	key := filepath.Join(filepath.Dir(c.repo), "fixture-key")
	if out, err := exec.Command(tools["ssh-keygen"], "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("generate fixture key: %v\n%s", err, out)
	}
	agentArgs := []string{"-s"}
	if runtime.GOOS != "windows" {
		agentDir, err := os.MkdirTemp("/tmp", "tfsaz-agent-")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.RemoveAll(agentDir); err != nil {
				t.Error(err)
			}
		}()
		agentArgs = append(agentArgs, "-a", filepath.Join(agentDir, "sock"))
	}
	out, err := exec.Command(tools["ssh-agent"], agentArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("start fixture agent: %v\n%s", err, out)
	}
	_, tail, _ := strings.Cut(string(out), "SSH_AGENT_PID=")
	pid, _, _ := strings.Cut(tail, ";")
	if pid == "" {
		t.Fatalf("fixture agent did not report its PID: %s", out)
	}
	t.Setenv("SSH_AGENT_PID", pid)
	defer func() {
		if out, err := exec.Command(tools["ssh-agent"], "-k").CombinedOutput(); err != nil {
			t.Errorf("stop fixture agent: %v\n%s", err, out)
		}
	}()
	_, tail, _ = strings.Cut(string(out), "SSH_AUTH_SOCK=")
	socket, _, _ := strings.Cut(tail, ";")
	if socket == "" {
		t.Fatalf("fixture agent did not report its socket: %s", out)
	}
	t.Setenv("SSH_AUTH_SOCK", socket)
	t.Setenv("TFSAZ_TEST_SSH_ADD", tools["ssh-add"])
	t.Setenv("TFSAZ_TEST_PUBLIC_KEY", key+".pub")
	configPath := filepath.Join(c.gitDir, "config")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	checkKeyError := func(action string) {
		t.Helper()
		args := []string{action, c.repo}
		if action == "setup" {
			args = append(args, "--managed-ssh")
		}
		err := a.runRepo(t.Context(), args)
		if err == nil || !strings.Contains(err.Error(), "ssh-agent or configure IdentityFile") ||
			!strings.Contains(err.Error(), "only the public key") || strings.Contains(err.Error(), "fixture-private-detail") {
			t.Fatalf("repo %s returned %v", action, err)
		}
	}
	checkKeyError("setup")
	after, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed setup changed Git config: %v", err)
	}
	path, err := setupRegistryPath()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(setupSSHPath(path, c.gitDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed setup left a generated SSH config: %v", err)
	}
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("failed setup left registry records: %+v", records)
	}
	if out, err := exec.Command(tools["ssh-add"], key).CombinedOutput(); err != nil {
		t.Fatalf("load fixture key: %v\n%s", err, out)
	}
	managerTestRun(t, a, "setup", "--managed-ssh")
	managerTestRun(t, a, "verify", c.repo)
	t.Setenv("TFSAZ_TEST_SSH_FAILURE", "fixture-private-detail: Permission denied (publickey).")
	checkKeyError("verify")
	trace := os.Getenv("TFSAZ_TEST_SSH_TRACE")
	if err := os.Remove(trace); err != nil {
		t.Fatal(err)
	}
	managerTestRun(t, a, "restore")
	if _, err := os.Stat(trace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore invoked SSH: %v", err)
	}
	managerTestState(t, c, setupGitState{url: managerTestOrigin})
}

func TestRepoSetupRefusesConflicts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, setupCheckout)
	}{
		{"multiple fetch URLs", func(t *testing.T, c setupCheckout) {
			managerTestGit(t, c.repo, "config", "--add", setupOriginKey, managerTestOrigin)
		}},
		{"push URL", func(t *testing.T, c setupCheckout) {
			managerTestGit(t, c.repo, "config", "remote.origin.pushurl", managerTestOrigin)
		}},
		{"global SSH command", func(t *testing.T, c setupCheckout) {
			managerTestGit(t, c.repo, "config", "--global", setupSSHKey, "custom")
		}},
		{"Git config lock", func(t *testing.T, c setupCheckout) {
			managerTestWrite(t, filepath.Join(c.gitDir, "config.lock"), "foreign lock")
		}},
		{"SSH file exists", func(t *testing.T, c setupCheckout) {
			path, err := setupRegistryPath()
			if err != nil {
				t.Fatal(err)
			}
			managerTestWrite(t, setupSSHPath(path, c.gitDir), "user file")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, c := managerTestRepo(t)
			tc.change(t, c)
			before, err := os.ReadFile(filepath.Join(c.gitDir, "config"))
			if err != nil {
				t.Fatal(err)
			}
			if err := a.runRepo(t.Context(), []string{"setup", "--managed-ssh"}); err == nil {
				t.Fatal("setup accepted a conflict")
			}
			after, err := os.ReadFile(filepath.Join(c.gitDir, "config"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("Git config changed: %v", err)
			}
			if records := managerTestRecords(t); len(records) != 0 {
				t.Fatalf("records = %+v", records)
			}
		})
	}
}

func TestRepoSetupDriftAcrossAllRecords(t *testing.T) {
	a, _, c := managerTestRepo(t)
	managerTestRun(t, a, "setup", "--managed-ssh")
	other := filepath.Join(filepath.Dir(c.repo), "other")
	managerTestGit(t, c.repo, "init", other)
	managerTestGit(t, other, "remote", "add", "origin", managerTestOrigin)
	managerTestRun(t, a, "setup", other, "--managed-ssh")
	records := managerTestRecords(t)
	managerTestWrite(t, records[1].SSHConfig, "manually changed")
	managerTestRun(t, a, "verify", c.repo)
	if err := a.runRepo(t.Context(), []string{"verify"}); err == nil || !strings.Contains(err.Error(), "generated SSH configuration has drifted") {
		t.Fatalf("verify all returned %v", err)
	}
	if err := a.runRepo(t.Context(), []string{"restore", other}); err == nil || !strings.Contains(err.Error(), "repo restore --force") {
		t.Fatalf("restore of a manually edited SSH config returned %v", err)
	}
	data, err := os.ReadFile(records[1].SSHConfig)
	if err != nil || string(data) != "manually changed" {
		t.Fatalf("manual edit was lost: %q, %v", data, err)
	}
	a.stdout.Reset()
	managerTestRun(t, a, "restore", "--force", other)
	if got := a.stdout.String(); !strings.Contains(got, "  "+records[1].SSHConfig+"\n    current:  edited, differs from the generated file\n    recorded: (deleted)\n") {
		t.Fatalf("force restore output = %q", got)
	}
	if _, err := os.Lstat(records[1].SSHConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("force restore left the SSH config: %v", err)
	}
	managerTestState(t, setupCheckout{repo: other, gitDir: records[1].GitDir}, setupGitState{url: managerTestOrigin})
	if got := managerTestRecords(t); len(got) != 1 || got[0] != records[0] {
		t.Fatalf("records = %+v", got)
	}
}

func TestRepoSetupRecoversInterruptedGitTransaction(t *testing.T) {
	a, _, c := managerTestRepo(t)
	managerTestRun(t, a, "setup", "--managed-ssh")
	r := managerTestRecords(t)[0]
	path, err := setupRegistryPath()
	if err != nil {
		t.Fatal(err)
	}
	registry := setupRegistry{path: path, records: []setupRecord{r}}
	// Simulate a crash after the Git change but before the registry commit.
	if err := registry.update(r, setupPhasePreparing); err != nil {
		t.Fatal(err)
	}
	managerTestRun(t, a, "show")
	if !strings.Contains(a.stdout.String(), "Phase: preparing") {
		t.Fatalf("pending setup show = %q", a.stdout)
	}
	lock, err := lockSetupGit(c)
	if err != nil {
		t.Fatal(err)
	}
	managerTestRun(t, a, "restore")
	original, _ := setupStates(r)
	managerTestState(t, c, original)
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
	if _, err := os.Stat(lock.proof); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted lock proof remains: %v", err)
	}
}

func TestRepoSetupRollsBackWhenAnIncludeActivates(t *testing.T) {
	a, _, c := managerTestRepo(t)
	path, err := setupRegistryPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	r, err := makeSetupRecord(t.Context(), &memoryStore{}, cfg, c.repo, c.gitDir, managerTestOrigin, "", "", setupSSHPath(path, c.gitDir))
	if err != nil {
		t.Fatal(err)
	}
	included := filepath.Join(filepath.Dir(c.repo), "included.gitconfig")
	managerTestWrite(t, included, "[remote \"origin\"]\n\tpushurl = "+managerTestOrigin+"\n")
	managerTestGit(t, c.repo, "config", "includeIf.hasconfig:remote.*.url:"+r.RemoteURL+".path", filepath.ToSlash(included))
	if err := a.runRepo(context.Background(), []string{"setup", "--managed-ssh"}); err == nil || !strings.Contains(err.Error(), "remote.origin.pushurl") {
		t.Fatalf("setup with conflicting include returned %v", err)
	}
	managerTestState(t, c, setupGitState{url: managerTestOrigin})
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
	if _, err := os.Stat(r.SSHConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SSH config remains: %v", err)
	}
}

func TestRepoSetupRegistryNamespacesAndLocks(t *testing.T) {
	a, _, c := managerTestRepo(t)
	path, err := setupRegistryPath()
	if err != nil {
		t.Fatal(err)
	}
	release, err := acquireSetupLock(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	err = a.runRepo(t.Context(), []string{"setup", "--managed-ssh"})
	if unlockErr := release(); unlockErr != nil {
		t.Fatal(unlockErr)
	}
	if err == nil || !strings.Contains(err.Error(), "another repository setup transaction") {
		t.Fatalf("setup while the registry was locked returned %v", err)
	}
	managerTestRun(t, a, "setup", "--managed-ssh")
	r := managerTestRecords(t)[0]
	alternate := filepath.Join(filepath.Dir(path), "alternate.json")
	managerTestWrite(t, alternate, `{"profiles":[{"id":"test","organization":"https://dev.azure.com/example","collectionUrl":"https://server.example/tfs/Collection"}]}`)
	t.Setenv("TFSAZ_CONFIG", alternate)
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("different config file exposed records: %+v", records)
	}
	if err := a.runRepo(t.Context(), []string{"setup", "--managed-ssh"}); err == nil {
		t.Fatal("another registry took over the repository")
	}
	_, active := setupStates(r)
	managerTestState(t, c, active)
}

func TestRepoSetupCompletesInterruptedRestore(t *testing.T) {
	a, _, c := managerTestRepo(t)
	managerTestRun(t, a, "setup", "--managed-ssh")
	r := managerTestRecords(t)[0]
	path, err := setupRegistryPath()
	if err != nil {
		t.Fatal(err)
	}
	registry := setupRegistry{path: path, records: []setupRecord{r}}
	if err := registry.update(r, setupPhaseRestoring); err != nil {
		t.Fatal(err)
	}
	original, active := setupStates(r)
	if err := registry.gitTransaction(c, func(lock *setupGitLock) error {
		return lock.change(t.Context(), active, original)
	}); err != nil {
		t.Fatal(err)
	}
	managerTestRun(t, a, "restore")
	managerTestState(t, c, original)
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("interrupted restore record remains: %+v", records)
	}
	if _, err := os.Stat(r.SSHConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted restore SSH config remains: %v", err)
	}
}

func TestRepoSetupHTTPSOverrideAndManualOriginDrift(t *testing.T) {
	a, _, c := managerTestRepo(t)
	const httpsOrigin = "https://server.example/tfs/Collection/Project/_git/Repository"
	managerTestGit(t, c.repo, "remote", "set-url", "origin", httpsOrigin)
	managerTestRun(t, a, "setup", "--ssh-url", managerTestOrigin)
	managerTestRun(t, a, "verify")
	managerTestRun(t, a, "restore")
	managerTestState(t, c, setupGitState{url: httpsOrigin})

	managerTestRun(t, a, "setup", "--ssh-url", managerTestOrigin)
	r := managerTestRecords(t)[0]
	managerTestGit(t, c.repo, "remote", "set-url", "origin", managerTestOrigin)
	if err := a.runRepo(t.Context(), []string{"restore"}); err == nil || !strings.Contains(err.Error(), "repo restore --force") {
		t.Fatalf("restore of a manually edited origin returned %v", err)
	}
	managerTestState(t, c, setupGitState{url: managerTestOrigin, ssh: r.SSHCommand})

	managerTestGit(t, c.repo, "config", "--add", setupOriginKey, r.RemoteURL)
	managerTestGit(t, c.repo, "config", setupVCSKey, "manual")
	if err := a.runRepo(t.Context(), []string{"restore"}); err == nil || !strings.Contains(err.Error(), "repo restore --force") {
		t.Fatalf("restore of two origin URLs returned %v", err)
	}
	a.stdout.Reset()
	managerTestRun(t, a, "restore", "--force")
	want := fmt.Sprintf("Overwriting in %s:\n  %s\n    current:  %s\n    current:  %s\n    recorded: %s\n  %s\n    current:  %s\n    recorded: (unset)\n  %s\n    current:  generated file\n    recorded: (deleted)\nrestore complete for %s\n",
		c.repo, setupOriginKey, managerTestOrigin, r.RemoteURL, httpsOrigin, setupSSHKey, r.SSHCommand, r.SSHConfig, c.repo)
	if got := a.stdout.String(); got != want {
		t.Fatalf("force restore output = %q, want %q", got, want)
	}
	managerTestState(t, c, setupGitState{url: httpsOrigin, vcs: "manual"})
	if _, err := os.Lstat(r.SSHConfig); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("force restore left the SSH config: %v", err)
	}
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
	if err := a.runRepo(t.Context(), []string{"restore", "--force"}); err == nil || !strings.Contains(err.Error(), "no setup recorded") {
		t.Fatalf("second force restore returned %v", err)
	}
}

func TestRepoForgetDeletedOrMovedCheckout(t *testing.T) {
	a, out, c := managerTestRepo(t)
	managerTestRun(t, a, "setup", "--managed-ssh")
	r := managerTestRecords(t)[0]
	root := filepath.Dir(c.repo)
	a.cwd = root
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"forget"}, "needs the checkout path"},
		{[]string{"forget", c.repo, "--force"}, "unsupported option"},
		{[]string{"forget", c.repo}, "still exists; run repo restore"},
		{[]string{"forget", filepath.Join(root, "never set up")}, "no setup recorded"},
	} {
		if err := a.runRepo(t.Context(), tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("repo %q returned %v, want %q", tc.args, err, tc.want)
		}
	}
	if got := managerTestRecords(t); len(got) != 1 || got[0] != r {
		t.Fatalf("refused forget changed records: %+v", got)
	}

	moved := filepath.Join(root, "moved checkout")
	if err := os.Rename(c.repo, moved); err != nil {
		t.Fatal(err)
	}
	if err := a.runRepo(t.Context(), []string{"restore", moved}); err == nil || !strings.Contains(err.Error(), "no setup recorded") {
		t.Fatalf("restore of the moved checkout returned %v", err)
	}
	if err := a.runRepo(t.Context(), []string{"restore", c.repo}); err == nil ||
		!strings.Contains(err.Error(), "no longer exists") || !strings.Contains(err.Error(), "repo forget "+setupCommandPath(c.repo)) {
		t.Fatalf("restore of the missing checkout returned %v", err)
	}
	movedConfig := filepath.Join(moved, ".git", "config")
	before, err := os.ReadFile(movedConfig)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	managerTestRun(t, a, "forget", filepath.Base(c.repo))
	if want := fmt.Sprintf("forgot %s\nOriginal origin: %s\nRecorded origin: %s\nGenerated SSH configuration: %s\n", r.Repo, r.OriginalURL, r.RemoteURL, filepath.Base(r.SSHConfig)); out.String() != want {
		t.Fatalf("forget output = %q, want %q", out, want)
	}
	if got := managerTestRecords(t); len(got) != 0 {
		t.Fatalf("records = %+v", got)
	}
	if after, err := os.ReadFile(movedConfig); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("forget changed the moved checkout: %v", err)
	}
	if err := a.runRepo(t.Context(), []string{"forget", c.repo}); err == nil || !strings.Contains(err.Error(), "no setup recorded") {
		t.Fatalf("second forget returned %v", err)
	}
}

func TestRepoForgetRefusesSurvivingGitDirectory(t *testing.T) {
	a, _, c := managerTestRepo(t)
	managerTestGit(t, c.repo, "-c", "user.name=Tester", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "Initial")
	root := filepath.Dir(c.repo)
	bare := filepath.Join(root, "common.git")
	managerTestGit(t, root, "clone", "--bare", c.repo, bare)
	managerTestGit(t, bare, "remote", "set-url", "origin", managerTestOrigin)
	linked := filepath.Join(root, "linked worktree")
	managerTestGit(t, bare, "worktree", "add", "-b", "linked", linked)
	managerTestRun(t, a, "setup", "--managed-ssh", linked)
	r := managerTestRecords(t)[0]
	if err := os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	if err := a.runRepo(t.Context(), []string{"forget", linked}); err == nil || !strings.Contains(err.Error(), "Git directory "+r.GitDir) {
		t.Fatalf("forget with a surviving Git directory returned %v", err)
	}
	if err := a.runRepo(t.Context(), []string{"restore", linked}); err == nil || !strings.Contains(err.Error(), "Git directory "+r.GitDir) {
		t.Fatalf("restore with a surviving Git directory returned %v", err)
	}
	if got := managerTestRecords(t); len(got) != 1 || got[0] != r {
		t.Fatalf("refused forget changed records: %+v", got)
	}
	managerTestGit(t, bare, "worktree", "prune")
	managerTestGit(t, bare, "worktree", "add", linked, "linked")
	managerTestRun(t, a, "restore", linked)
}

func TestRepoRestoreWithMissingSSHConfigAndExternalOverride(t *testing.T) {
	a, _, c := managerTestRepo(t)
	managerTestRun(t, a, "setup", "--managed-ssh")
	r := managerTestRecords(t)[0]
	if err := os.Remove(r.SSHConfig); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", "unavailable-custom-command")
	if err := a.runRepo(t.Context(), []string{"verify"}); err == nil {
		t.Fatal("verify accepted drift")
	}
	managerTestRun(t, a, "restore")
	managerTestState(t, c, setupGitState{url: managerTestOrigin})
}

func TestRepoSetupChecksSSHWithGitConfigUnlocked(t *testing.T) {
	a, _, c := managerTestRepo(t)
	t.Setenv("TFSAZ_TEST_GIT_WRITE", "1")
	managerTestRun(t, a, "setup", "--managed-ssh")
	values, err := c.configValues(t.Context(), "", "tfsaz.writtenduringprobe")
	if err != nil || len(values) != 1 || values[0] != "yes" {
		t.Fatalf("concurrent Git config write was lost: %v", err)
	}
	managerTestRun(t, a, "restore")
}

func TestSetupCommandPathQuotesForTheShell(t *testing.T) {
	for _, tc := range []struct {
		goos, path, want string
	}{
		{"linux", "/tmp/checkout", "/tmp/checkout"},
		{"linux", "/tmp/checkout with spaces", "'/tmp/checkout with spaces'"},
		{"darwin", `/tmp/checkout\name`, `'/tmp/checkout\name'`},
		{"linux", "/tmp/it's $HOME", `'/tmp/it'\''s $HOME'`},
		{"windows", `C:\work\checkout`, `C:\work\checkout`},
		{"windows", `C:\work\check out`, `'C:\work\check out'`},
		{"windows", "C:\\work\\$draft\u2019x \u201cy\u201d", "'C:\\work\\$draft\u2019\u2019x \u201cy\u201d'"},
		{"windows", `C:\work\$repo`, `'C:\work\$repo'`},
		{"windows", "C:\\work\\it's `x`", "'C:\\work\\it''s `x`'"},
	} {
		got := setupCommandPathFor(tc.goos, tc.path)
		if got != tc.want {
			t.Errorf("setupCommandPathFor(%s, %q) = %s, want %s", tc.goos, tc.path, got, tc.want)
			continue
		}
		if (tc.goos == "windows") != (runtime.GOOS == "windows") {
			continue
		}
		var parsed string
		var err error
		if runtime.GOOS == "windows" {
			parsed, err = managerTestPowerShellArgument(t, got)
		} else {
			var out []byte
			out, err = exec.Command("sh", "-c", "printf '%s' "+got).Output()
			parsed = string(out)
		}
		if err != nil || parsed != tc.path {
			t.Errorf("the shell parsed %s as %q, %v, want %q", got, parsed, err, tc.path)
		}
	}
}

func managerTestPowerShellArgument(t *testing.T, argument string) (string, error) {
	t.Helper()
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		if shell, err = exec.LookPath("powershell"); err != nil {
			t.Skip("PowerShell unavailable")
		}
	}
	out := filepath.Join(t.TempDir(), "argument.txt")
	cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command",
		"function record { [IO.File]::WriteAllText($env:TFSAZ_TEST_OUT, $args[0]) }; record "+argument)
	cmd.Env = append(os.Environ(), "TFSAZ_TEST_OUT="+out)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%w: %s", err, output)
	}
	data, err := os.ReadFile(out)
	return string(data), err
}

func TestRepoVerifyChecksCollectionAPI(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkClientTestAuth(t, r)
		if r.Method != http.MethodOptions {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		// The collection path names the API level the fixture advertises.
		level, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		serveTestCatalog(w, level)
	}))
	defer server.Close()
	tests := []struct {
		name, collection, stored string
		stdout, stderr, err      string
	}{
		{name: "floor", collection: "7.1", stored: clientTestPassword, stdout: "verified API 7.1 for profile test-collection\n"},
		{name: "below floor", collection: "6.0", stored: clientTestPassword, err: "profile test-collection: the collection supports API 6.0 at most"},
		{name: "no PAT skips the check", collection: "7.1", stderr: "WARNING: API version not checked: profile test-collection has no PAT"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := clientTestProfile(t, server, "/"+tc.collection)
			store := &memoryStore{values: map[string]string{}}
			if tc.stored != "" {
				store.values[p.ID] = tc.stored
			}
			var stdout, stderr bytes.Buffer
			a := app{stdout: &stdout, stderr: &stderr, store: store}
			err := a.verifySetupAPI(t.Context(), config{Profiles: []Profile{p}}, p.ID)
			if (err == nil) != (tc.err == "") || (err != nil && !strings.Contains(err.Error(), tc.err)) ||
				stdout.String() != tc.stdout || !strings.HasPrefix(stderr.String(), tc.stderr) || (tc.stderr == "" && stderr.Len() != 0) {
				t.Fatalf("verifySetupAPI = %v, stdout %q, stderr %q", err, &stdout, &stderr)
			}
		})
	}
	if err := (app{store: &memoryStore{}}).verifySetupAPI(t.Context(), config{}, "missing"); err == nil {
		t.Fatal("verifySetupAPI accepted an unconfigured profile")
	}
}
