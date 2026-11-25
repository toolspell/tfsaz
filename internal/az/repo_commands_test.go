package az

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReposCreate(t *testing.T) {
	fixture := repoFixture(t, "repository")
	client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost ||
			r.URL.EscapedPath() != repoTestPrefix+"/Shared%20Project/_apis/git/repositories" ||
			r.URL.RawQuery != "api-version=7.1" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected repository create request: %s %s (%s)", r.Method, r.URL, r.Header.Get("Content-Type"))
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, map[string]string{"name": "Tool Kit"}) {
			t.Errorf("repository create body = %#v, %v", body, err)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, strings.ReplaceAll(fixture, "Fixture%20Collection", "Fixture Collection"))
	})
	got, err := (app{}).repoCommand(t.Context(), client, Scope{Project: "Implicit Project"}, "repos create", map[string]string{
		"project": "Shared Project",
		"name":    "Tool Kit",
		"detect":  "false",
	})
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fixture), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repository output = %#v, want %#v", got, want)
	}
}

func TestCheckoutPullRequestScopeUsesNamedRemote(t *testing.T) {
	root := checkoutTestGitRoot(t)
	gitAt(t, root, "init")
	gitAt(t, root, "remote", "add", "origin", "https://first.visualstudio.com/App/_git/Repo")
	gitAt(t, root, "remote", "add", "upstream", "git@vs-ssh.visualstudio.com:v3/second/Shared%20Project/Tool%20Kit")
	cfg := config{Profiles: []Profile{
		{ID: "first", Organization: "https://first.visualstudio.com", CollectionURL: "https://server.example.invalid/First"},
		{ID: "second", Organization: "https://second.visualstudio.com", CollectionURL: "https://server.example.invalid/Second"},
	}}
	opts := map[string]string{"remote-name": "upstream"}
	p, scope, err := cfg.repoCheckoutScope(t.Context(), root, opts)
	if err != nil || p != &cfg.Profiles[1] || scope.Project != "Shared Project" || scope.Repository != "Tool Kit" {
		t.Fatalf("scope = %+v, %+v, %v", p, scope, err)
	}
	opts["organization"] = cfg.Profiles[0].Organization
	if _, _, err := cfg.repoCheckoutScope(t.Context(), root, opts); err == nil || !strings.Contains(err.Error(), "different collection") {
		t.Fatalf("mismatched organization error = %v", err)
	}
	opts["organization"] = cfg.Profiles[1].CollectionURL
	if p, _, err := cfg.repoCheckoutScope(t.Context(), root, opts); err != nil || p != &cfg.Profiles[1] {
		t.Fatalf("matching collection URL = %+v, %v", p, err)
	}
}

func TestCheckoutPullRequestBranchPreservesLocalWork(t *testing.T) {
	root := checkoutTestGitRoot(t)
	source := filepath.Join(root, "source")
	gitAt(t, root, "init", "--initial-branch=main", source)
	file := filepath.Join(source, "document")
	managerTestWrite(t, file, "main\n")
	gitAt(t, source, "add", "document")
	checkoutTestCommit(t, source)
	gitAt(t, source, "switch", "-c", "feature/test")
	managerTestWrite(t, file, "upstream feature\n")
	gitAt(t, source, "add", "document")
	checkoutTestCommit(t, source)

	cwd := filepath.Join(root, "checkout")
	gitAt(t, root, "clone", "--single-branch", "--branch", "main", "--origin", "upstream", source, cwd)
	git := setupCheckout{repo: cwd}
	pullRequest := strings.ReplaceAll(repoFixture(t, "pull-request"), "user/feat/1-(#2)-slug", "feature/test")
	client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.EscapedPath() != repoTestPrefix+repoTestPRPath {
			t.Errorf("unexpected pull request request: %s %s", r.Method, r.URL)
		}
		io.WriteString(w, pullRequest)
	})
	a := app{cwd: cwd}
	scope := Scope{Project: "Shared Project", Repository: "Tool Kit"}
	opts := map[string]string{"id": "42", "remote-name": "upstream"}
	checkout := func() error {
		got, err := a.repoCommand(t.Context(), client, scope, "repos pr checkout", opts)
		if got != nil {
			t.Errorf("checkout returned output %#v", got)
		}
		return err
	}
	managerTestWrite(t, filepath.Join(cwd, "document"), "local edit\n")
	if err := checkout(); err == nil || !strings.Contains(err.Error(), "local changes would be overwritten") {
		t.Fatalf("checkout with local changes = %v", err)
	}
	if head := checkoutTestGitOutput(t, git, "branch", "--show-current"); head != "main" {
		t.Fatalf("branch after failed checkout = %q", head)
	}
	if body, err := os.ReadFile(filepath.Join(cwd, "document")); err != nil || string(body) != "local edit\n" {
		t.Fatalf("worktree file after failed checkout = %q, %v", body, err)
	}

	managerTestWrite(t, filepath.Join(cwd, "document"), "main\n")
	if err := checkout(); err != nil {
		t.Fatal(err)
	}
	if head := checkoutTestGitOutput(t, git, "branch", "--show-current"); head != "feature/test" {
		t.Fatalf("branch after checkout = %q", head)
	}
	remoteCommit := checkoutTestGitOutput(t, setupCheckout{repo: source}, "rev-parse", "HEAD")
	if localCommit := checkoutTestGitOutput(t, git, "rev-parse", "HEAD"); remoteCommit != localCommit {
		t.Fatalf("checked out %q, want %q", localCommit, remoteCommit)
	}

	checkoutTestCommit(t, cwd)
	localCommit := checkoutTestGitOutput(t, git, "rev-parse", "HEAD")
	gitAt(t, cwd, "switch", "main")
	// Rewrite the remote history while retaining a local commit on the PR branch.
	gitAt(t, source, "reset", "--hard", "main")
	checkoutTestCommit(t, source)
	remoteCommit = checkoutTestGitOutput(t, setupCheckout{repo: source}, "rev-parse", "HEAD")
	if err := checkout(); err != nil {
		t.Fatal(err)
	}
	if head := checkoutTestGitOutput(t, git, "rev-parse", "HEAD"); head != localCommit {
		t.Errorf("checkout moved existing local branch to %q, want %q", head, localCommit)
	}
	if tracking := checkoutTestGitOutput(t, git, "rev-parse", "refs/remotes/upstream/feature/test"); tracking != remoteCommit {
		t.Errorf("remote-tracking ref = %q, want %q", tracking, remoteCommit)
	}

	full := filepath.Join(root, "full checkout")
	gitAt(t, root, "clone", "--branch", "main", "--origin", "upstream", source, full)
	if _, err := (app{cwd: full}).repoCommand(t.Context(), client, scope, "repos pr checkout", opts); err != nil {
		t.Fatal(err)
	}
	if upstream := checkoutTestGitOutput(t, setupCheckout{repo: full}, "rev-parse", "--symbolic-full-name", "@{upstream}"); upstream != "refs/remotes/upstream/feature/test" {
		t.Errorf("new branch upstream = %q", upstream)
	}

	gitAt(t, source, "switch", "main")
	gitAt(t, source, "branch", "-D", "feature/test")
	gitAt(t, cwd, "switch", "main")
	if err := checkout(); err == nil || !strings.Contains(err.Error(), "fetch the pull request source branch") {
		t.Fatalf("checkout with a vanished remote branch = %v", err)
	}
	if head := checkoutTestGitOutput(t, git, "branch", "--show-current"); head != "main" {
		t.Errorf("checkout with a stale tracking ref switched to %q", head)
	}
}

func TestCheckoutPullRequestRejectsWrongRepositoryAndFork(t *testing.T) {
	fixture := repoFixture(t, "pull-request")
	for _, tc := range []struct{ name, response, repo, want string }{
		{"wrong repository", fixture, "Different", "different repository"},
		{"fork", strings.Replace(fixture, `"forkSource": null`, `"forkSource": {}`, 1), "Tool Kit", "from a fork"},
		{"wrong ID", strings.Replace(fixture, `"pullRequestId": 42`, `"pullRequestId": 43`, 1), "Tool Kit", "does not identify pull request 42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := repoTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tc.response)
			})
			a := app{cwd: t.TempDir()}
			err := a.checkoutPullRequest(t.Context(), client, Scope{Project: "Shared Project", Repository: tc.repo}, map[string]string{"id": "42"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkout error = %v, want %q", err, tc.want)
			}
		})
	}
}

func checkoutTestGitRoot(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "empty.gitconfig"))
	t.Setenv("LC_ALL", "C")
	return root
}

func checkoutTestGitOutput(t *testing.T, git setupCheckout, args ...string) string {
	t.Helper()
	out, err := git.git(t.Context(), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func checkoutTestCommit(t *testing.T, cwd string) {
	t.Helper()
	gitAt(t, cwd, "-c", "user.name=Tester", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "Fixture")
}
