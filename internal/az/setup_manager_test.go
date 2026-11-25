package az

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
	if runtime.GOOS == "windows" {
		t.Skip("SSH fixture uses a POSIX shell script")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "tfsaz")
	if output, err := exec.Command("go", "build", "-o", bin, "../..").CombinedOutput(); err != nil {
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
    if ! ssh-add -T "$TFSAZ_TEST_PUBLIC_KEY" 2>/dev/null; then
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
	if err := os.WriteFile(filepath.Join(root, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
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
}

func TestRepoSetupSSHKeyCheckAndOfflineRestore(t *testing.T) {
	for _, name := range []string{"ssh-agent", "ssh-add", "ssh-keygen"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s unavailable", name)
		}
	}
	a, _, c := managerTestRepo(t)
	key := filepath.Join(filepath.Dir(c.repo), "fixture-key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("generate fixture key: %v\n%s", err, out)
	}
	agentDir, err := os.MkdirTemp("/tmp", "tfsaz-agent-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(agentDir)
	socket := filepath.Join(agentDir, "sock")
	if out, err := exec.Command("ssh-agent", "-s", "-a", socket).CombinedOutput(); err != nil {
		t.Fatalf("start fixture agent: %v\n%s", err, out)
	} else {
		_, tail, _ := strings.Cut(string(out), "SSH_AGENT_PID=")
		pid, _, _ := strings.Cut(tail, ";")
		if pid == "" {
			t.Fatalf("fixture agent did not report its PID: %s", out)
		}
		t.Setenv("SSH_AGENT_PID", pid)
		defer exec.Command("ssh-agent", "-k").Run()
	}
	t.Setenv("SSH_AUTH_SOCK", socket)
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
	if out, err := exec.Command("ssh-add", key).CombinedOutput(); err != nil {
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
	if err := a.runRepo(t.Context(), []string{"restore", other}); err == nil {
		t.Fatal("restore accepted a manually edited SSH config")
	}
	data, err := os.ReadFile(records[1].SSHConfig)
	if err != nil || string(data) != "manually changed" {
		t.Fatalf("manual edit was lost: %q, %v", data, err)
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
	r, err := makeSetupRecord(t.Context(), cfg, c.repo, c.gitDir, managerTestOrigin, "", "", setupSSHPath(path, c.gitDir))
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
	if err := a.runRepo(t.Context(), []string{"restore"}); err == nil {
		t.Fatal("restore accepted a manually edited origin")
	}
	managerTestState(t, c, setupGitState{url: managerTestOrigin, ssh: r.SSHCommand})
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
