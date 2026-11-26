package az

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func preserveTestRepo(t *testing.T) (managerTestCLI, setupCheckout, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	bin := testExecutable(filepath.Join(root, "tfsaz"))
	if output, err := exec.Command("go", "build", "-o", bin, "../..").CombinedOutput(); err != nil {
		t.Fatalf("build tfsaz: %v\n%s", err, output)
	}
	copyTestExecutable(t, bin, testExecutable(filepath.Join(root, "git-remote-tfsaz")))
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "global.gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	config := filepath.Join(root, "settings.json")
	t.Setenv("TFSAZ_CONFIG", config)
	repo := filepath.Join(root, "checkout with spaces")
	server := filepath.Join(root, "server.git")
	managerTestGit(t, root, "init", "-b", "main", repo)
	managerTestGit(t, root, "init", "--bare", "-b", "main", server)
	managerTestGit(t, repo, "config", "user.name", "Tester")
	managerTestGit(t, repo, "config", "user.email", "tester@example.invalid")
	managerTestGit(t, repo, "commit", "--allow-empty", "-m", "Initial")
	managerTestGit(t, repo, "push", server, "HEAD:main")
	c, err := findSetupCheckout(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return managerTestCLI{executable: bin, cwd: repo, stdout: &bytes.Buffer{}}, c, config, server
}

func preserveTestConfig(t *testing.T, path, collection string) {
	t.Helper()
	data, err := json.Marshal(config{Profiles: []Profile{{
		ID: "fixture", Organization: "https://dev.azure.com/fixture", CollectionURL: collection,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, path, string(data))
}

func TestRepoPreserveSSHFetchPushAndRestore(t *testing.T) {
	a, c, cfgPath, server := preserveTestRepo(t)
	root := filepath.Dir(c.repo)
	preserveTestConfig(t, cfgPath, "https://server.example/Collection%20Space")
	origin := "ssh://git@server.example:2222/Collection%20Space/Project%20Space/_git/Repository"
	managerTestGit(t, c.repo, "remote", "add", "origin", origin)
	managerTestGit(t, c.repo, "remote", "add", "other", server)
	trace := filepath.Join(root, "ssh.trace")
	t.Setenv("TFSAZ_TEST_SSH_TRACE", trace)
	t.Setenv("TFSAZ_TEST_REPO", server)
	ssh := writeShellFixture(t, filepath.Join(root, "custom ssh"), `#!/bin/sh
if [ "$1" != --record ]; then exit 90; fi
shift
if [ "$1" = "-G" ]; then
    if [ "$TFSAZ_TEST_SSH_UNSAFE" = 1 ]; then printf 'batchmode no\nstricthostkeychecking false\n'; exit 0; fi
    if [ "$2 $3 $4 $5" = '-o BatchMode=yes -o StrictHostKeyChecking=yes' ]; then
        printf 'batchmode yes\nstricthostkeychecking true\n'
    fi
    exit 0
fi
printf 'GIT_DIR=%s\n' "$GIT_DIR" >> "$TFSAZ_TEST_SSH_TRACE"
printf '%s\n' "$@" >> "$TFSAZ_TEST_SSH_TRACE"
if [ -n "$GIT_DIR" ]; then exit 91; fi
if [ "$1 $2 $3 $4" = '-o BatchMode=yes -o StrictHostKeyChecking=yes' ]; then
    shift 4
elif [ "$TFSAZ_TEST_SSH_REQUIRE_BATCH" = 1 ]; then
    exit 94
fi
if [ "$TFSAZ_TEST_SSH_FAIL_MANAGED" = 1 ] && [ "$(git config --local --get remote.origin.vcs)" = "tfsaz" ]; then
    exit 95
fi
if [ "$1" = "-o" ]; then shift 2; fi
if [ "$1" != '-p' ] || [ "$2" != '2222' ] || [ "$3" != 'git@server.example' ]; then exit 92; fi
case "$4" in
    "git-upload-pack '/Collection Space/Project Space/_git/Repository'") exec git upload-pack "$TFSAZ_TEST_REPO" ;;
    "git-receive-pack '/Collection Space/Project Space/_git/Repository'") exec git receive-pack "$TFSAZ_TEST_REPO" ;;
    *) exit 93 ;;
esac
`)
	sshCommand := setupSSHQuote(filepath.ToSlash(ssh)) + " --record"
	managerTestGit(t, c.repo, "config", "--local", "core.sshCommand", sshCommand)
	managerTestGit(t, c.repo, "config", "--local", "ssh.variant", "ssh")
	linked := filepath.Join(root, "linked")
	managerTestGit(t, c.repo, "worktree", "add", "-b", "linked", linked)

	setup := []string{"setup", linked, "--preserve-transport", "--profile", "fixture"}
	t.Setenv("TFSAZ_TEST_SSH_REQUIRE_BATCH", "1")
	t.Setenv("TFSAZ_TEST_SSH_UNSAFE", "1")
	if err := a.runRepo(t.Context(), setup); err == nil || !strings.Contains(err.Error(), "requires BatchMode=yes and StrictHostKeyChecking=yes") {
		t.Fatalf("interactive SSH probe returned %v", err)
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("interactive SSH settings reached the server: %v", err)
	}
	t.Setenv("TFSAZ_TEST_SSH_UNSAFE", "")
	t.Setenv("TFSAZ_TEST_SSH_FAIL_MANAGED", "1")
	if err := a.runRepo(t.Context(), setup); err == nil || !strings.Contains(err.Error(), "SSH repository read failed") {
		t.Fatalf("failed managed SSH probe returned %v", err)
	}
	managerTestState(t, c, setupGitState{url: origin, ssh: sshCommand})
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("failed managed SSH probe left records: %+v", records)
	}
	t.Setenv("TFSAZ_TEST_SSH_FAIL_MANAGED", "")
	if err := a.runRepo(t.Context(), setup); err != nil {
		output, _ := os.ReadFile(trace)
		t.Fatalf("repo setup: %v\nSSH fixture calls:\n%s", err, output)
	}
	managerTestRun(t, a, "setup")
	if err := a.runRepo(t.Context(), []string{"setup", "--managed-ssh"}); err == nil || !strings.Contains(err.Error(), "different transport mode") {
		t.Fatalf("managed SSH replaced existing preserved transport: %v", err)
	}
	managerTestRun(t, a, "show")
	if !strings.Contains(a.stdout.String(), "Transport: ssh (preserve)") || !strings.Contains(a.stdout.String(), origin) ||
		!strings.Contains(a.stdout.String(), "Remote: origin\n") {
		t.Fatalf("show output = %q", a.stdout)
	}
	r := managerTestRecords(t)[0]
	if r.Transport != "ssh" || r.SSHCommand != "" || r.SSHConfig != "" || !strings.Contains(r.RemoteURL, "/Project%20Space/Repository") {
		t.Fatalf("registration = %+v", r)
	}
	managerTestRun(t, a, "verify", linked)
	if got, err := c.configValues(t.Context(), "", setupSSHKey); err != nil || len(got) != 1 || got[0] != sshCommand {
		t.Fatalf("core.sshCommand = %v (%v)", got, err)
	}

	// A deliberately invalid TFSAZ_CONFIG proves ordinary Git stays local.
	t.Setenv("TFSAZ_TEST_SSH_REQUIRE_BATCH", "")
	t.Setenv("TFSAZ_CONFIG", "not-an-absolute-path")
	managerTestGit(t, linked, "fetch", "origin")
	managerTestGit(t, linked, "push", "origin", "HEAD:refs/heads/routed")
	managerTestGit(t, linked, "fetch", "other")
	managerTestGit(t, server, "show-ref", "--verify", "refs/heads/routed")
	before, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	managerTestGit(t, c.repo, "config", "--replace-all", setupOriginKey, strings.ReplaceAll(r.RemoteURL, "Repository", "Other"))
	cmd := exec.CommandContext(t.Context(), "git", "ls-remote", "origin")
	cmd.Dir = c.repo
	if out, err := cmd.CombinedOutput(); err == nil || !bytes.Contains(out, []byte("does not match its managed transport binding")) {
		t.Fatalf("mismatched alias = %v\n%s", err, out)
	}
	after, err := os.ReadFile(trace)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("mismatched alias reached SSH: %v", err)
	}
	managerTestGit(t, c.repo, "config", "--replace-all", setupOriginKey, r.RemoteURL)

	t.Setenv("TFSAZ_CONFIG", cfgPath)
	managerTestGit(t, c.repo, "config", "remote.origin.pushurl", origin)
	if err := a.runRepo(t.Context(), []string{"show", linked}); err == nil || !strings.Contains(err.Error(), "remote.origin.pushurl") {
		t.Fatalf("repo show did not report push URL drift: %v", err)
	}
	managerTestGit(t, c.repo, "config", "--unset", "remote.origin.pushurl")
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	managerTestRun(t, a, "show", linked)
	managerTestGit(t, c.repo, "config", setupBindingKey, "manual binding")
	if err := a.runRepo(t.Context(), []string{"restore", linked}); err == nil || !strings.Contains(err.Error(), "repo restore --force") {
		t.Fatalf("restore of a drifted binding returned %v", err)
	}
	managerTestRun(t, a, "restore", "--force", linked)
	if got := a.stdout.String(); !strings.Contains(got, "  "+setupBindingKey+"\n    current:  manual binding\n    recorded: (unset)\n") ||
		strings.Contains(got, setupSSHKey) {
		t.Fatalf("force restore output = %q", got)
	}
	managerTestState(t, c, setupGitState{url: origin, ssh: sshCommand})
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("restored repository still registered: %+v", records)
	}
}

func TestRepoPreserveDefaultRequiresHelper(t *testing.T) {
	a, c, cfgPath, _ := preserveTestRepo(t)
	root := filepath.Dir(c.repo)
	preserveTestConfig(t, cfgPath, "https://server.example/tfs/Collection")
	managerTestGit(t, c.repo, "remote", "add", "origin", managerTestOrigin)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(testExecutable(filepath.Join(root, "git-remote-tfsaz"))); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_EXEC_PATH", root)
	t.Setenv("PATH", filepath.Dir(git))
	if err := a.runRepo(t.Context(), []string{"setup"}); err == nil || !strings.Contains(err.Error(), "tfsaz shim install --dir DIR") {
		t.Fatalf("default setup without helper returned %v", err)
	}
	managerTestState(t, c, setupGitState{url: managerTestOrigin})
	if records := managerTestRecords(t); len(records) != 0 {
		t.Fatalf("failed default setup left records: %+v", records)
	}
}

func TestRepoPreserveHTTPSFetchPushAndRestore(t *testing.T) {
	a, c, cfgPath, repo := preserveTestRepo(t)
	var requests []string
	var requestsMu sync.Mutex
	const path = "/Collection Space/Project Space/_git/Repository"
	const username, password = "fixture-user", "fixture-password"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, path+"/") {
			t.Errorf("unexpected Git path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		requestsMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		requestsMu.Unlock()
		if user, pass, ok := r.BasicAuth(); !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		runPreserveTestHTTPBackend(t, w, r, filepath.Dir(repo), "/"+filepath.Base(repo)+strings.TrimPrefix(r.URL.Path, path), username)
	}))
	defer server.Close()
	root := filepath.Dir(c.repo)
	cert := filepath.Join(root, "fixture-ca.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	preserveTestConfig(t, cfgPath, server.URL+"/Collection%20Space")
	origin := server.URL + strings.ReplaceAll(path, " ", "%20")
	managerTestGit(t, c.repo, "remote", "add", "origin", origin)
	managerTestGit(t, c.repo, "config", "http.sslCAInfo", cert)
	credentialTrace := filepath.Join(root, "credential.trace")
	t.Setenv("TFSAZ_TEST_CREDENTIAL_TRACE", credentialTrace)
	credentials := writeShellFixture(t, filepath.Join(root, "credentials"), `#!/bin/sh
if [ "$1" = "get" ]; then
    cat >> "$TFSAZ_TEST_CREDENTIAL_TRACE"
    printf 'username=%s\npassword=%s\n' '`+username+`' '`+password+`'
fi
`)
	managerTestGit(t, c.repo, "config", "credential.helper", "!"+setupSSHQuote(filepath.ToSlash(credentials)))
	managerTestGit(t, c.repo, "config", "credential.useHttpPath", "true")

	managerTestRun(t, a, "setup")
	managerTestRun(t, a, "show", c.repo)
	r := managerTestRecords(t)[0]
	if r.Transport != "https" || !strings.HasPrefix(r.RemoteURL, "https://tfsaz-") ||
		!strings.Contains(a.stdout.String(), "Transport: https (preserve)") {
		t.Fatalf("registration = %+v, show = %q", r, a.stdout)
	}
	managerTestRun(t, a, "verify", c.repo)

	t.Setenv("TFSAZ_CONFIG", "not-an-absolute-path")
	managerTestGit(t, c.repo, "fetch", "origin")
	linked := filepath.Join(root, "worktree after setup")
	managerTestGit(t, c.repo, "worktree", "add", "--detach", linked, "HEAD")
	managerTestGit(t, linked, "fetch", "origin")
	managerTestGit(t, linked, "push", "origin", "HEAD:refs/heads/routed")
	managerTestGit(t, repo, "show-ref", "--verify", "refs/heads/routed")
	credentialsUsed, err := os.ReadFile(credentialTrace)
	if err != nil || !bytes.Contains(credentialsUsed, []byte("host="+strings.TrimPrefix(server.URL, "https://"))) ||
		bytes.Contains(credentialsUsed, []byte(".visualstudio.com")) {
		t.Fatalf("unexpected credential scope = %q (%v)", credentialsUsed, err)
	}
	requestsMu.Lock()
	seen := strings.Join(requests, "\n")
	requestsMu.Unlock()
	if !strings.Contains(seen, "POST /Collection%20Space/Project%20Space/_git/Repository/git-receive-pack") {
		t.Fatalf("push did not use original HTTPS target: %s", seen)
	}
	t.Setenv("TFSAZ_CONFIG", cfgPath)
	server.Close()
	managerTestRun(t, a, "restore", linked)
	managerTestState(t, c, setupGitState{url: origin})
}

func runPreserveTestHTTPBackend(t *testing.T, w http.ResponseWriter, r *http.Request, root, path, user string) {
	t.Helper()
	cmd := exec.CommandContext(r.Context(), "git", "http-backend")
	cmd.Env = append(cmd.Environ(),
		"GIT_PROJECT_ROOT="+root, "GIT_HTTP_EXPORT_ALL=1", "PATH_INFO="+path,
		"CONTENT_TYPE="+r.Header.Get("Content-Type"), "CONTENT_LENGTH="+strconv.FormatInt(r.ContentLength, 10),
		"REQUEST_METHOD="+r.Method, "QUERY_STRING="+r.URL.RawQuery, "REMOTE_USER="+user,
		"HTTP_GIT_PROTOCOL="+r.Header.Get("Git-Protocol"))
	cmd.Stdin = r.Body
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Errorf("fixture Git HTTP backend failed: %v\n%s", err, &stderr)
		http.Error(w, "backend failed", http.StatusInternalServerError)
		return
	}
	reader := bufio.NewReader(bytes.NewReader(out))
	header, err := textproto.NewReader(reader).ReadMIMEHeader()
	if err != nil {
		t.Errorf("read fixture response: %v", err)
		http.Error(w, "invalid backend response", http.StatusInternalServerError)
		return
	}
	status := http.StatusOK
	if value := header.Get("Status"); len(value) >= 3 {
		status, err = strconv.Atoi(value[:3])
		if err != nil {
			t.Errorf("read fixture response status: %v", err)
			http.Error(w, "invalid backend status", http.StatusInternalServerError)
			return
		}
	}
	header.Del("Status")
	for key, values := range header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(status)
	if _, err := io.Copy(w, reader); err != nil {
		t.Error(fmt.Errorf("write fixture repository reply: %w", err))
	}
}

func TestPreserveHelperProbeDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	err := checkPreserveHelper(ctx, setupCheckout{repo: t.TempDir()})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expired helper probe returned %v", err)
	}
}
