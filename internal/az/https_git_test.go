package az

import (
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHTTPSHelperKeepsAzureRemoteAndUsesNativeTransport(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	gitCommand := func(cwd string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), git, args...)
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	core := gitCommand(root, "--exec-path")
	if _, err := os.Stat(filepath.Join(core, gitHTTPSHelperName())); err != nil {
		t.Skipf("original Git HTTPS helper unavailable: %v", err)
	}
	helperDir := filepath.Join(root, "Git helpers")
	if err := os.Mkdir(helperDir, 0700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(helperDir, gitHTTPSHelperName())
	binary := filepath.Join(root, "tfsaz"+filepath.Ext(helper))
	if out, err := exec.CommandContext(t.Context(), "go", "build", "-tags", "tfsaztest", "-o", binary, "../..").CombinedOutput(); err != nil {
		t.Fatalf("build tfsaz: %v\n%s", err, out)
	}
	if err := os.Link(binary, helper); err != nil {
		t.Fatal(err)
	}
	// After the build, which needs the real Go caches under HOME.
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData", "Roaming"))
	userConfig, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}

	const repoPath = "/tfs/Second%20Collection/Team%20Project/_git/Tool%20Kit"
	const authPath = "/tfs/Second%20Collection/Team%20Project/_git/RequiresAuth/info/refs"
	requests := make(chan string, 8)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("Git sent authentication to the credential-free fixture")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.EscapedPath() == authPath {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/denied/info/refs" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method != "GET" || r.URL.RequestURI() != repoPath+"/info/refs?service=git-upload-pack" {
			t.Errorf("unexpected Git discovery request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requests <- r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		// Smart discovery for an empty repository.
		serveTestBody(w, "001e# service=git-upload-pack\n00000000")
	}))
	defer server.Close()
	cert := filepath.Join(root, "fixture.pem")
	managerTestWrite(t, cert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})))
	configPath := filepath.Join(root, "tfsaz.json")
	globalConfig := filepath.Join(root, "gitconfig")
	managerTestWrite(t, configPath, fmt.Sprintf(`{"profiles":[{"id":"fixture","organization":"https://https-fixture.visualstudio.com","collectionUrl":%q}]}`, server.URL+"/tfs/Second%20Collection"))

	t.Setenv("TFSAZ_CONFIG", configPath)
	t.Setenv("TFSAZ_GIT_CORE", core)
	t.Setenv("GIT_EXEC_PATH", core)
	t.Setenv("PATH", core+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_ASKPASS", "")
	t.Setenv("SSH_ASKPASS", "")
	t.Setenv("GIT_SSL_CAINFO", cert)
	t.Setenv("NO_PROXY", "127.0.0.1")
	t.Setenv("LC_ALL", "C")
	gitCommand(root, "config", "--global", "http.schannelUseSSLCAInfo", "true")

	const alias = "https://https-fixture.visualstudio.com/DefaultCollection/Team%20Project/_git/Tool%20Kit"
	checkout := filepath.Join(root, "checkout")
	expectDiscovery := func() {
		t.Helper()
		select {
		case <-requests:
		default:
			t.Fatal("Git did not reach the configured HTTPS collection")
		}
	}
	wrappedGit := func(cwd string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, append([]string{"route", "run", "--", git}, args...)...)
		cmd.Dir = cwd
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := wrappedGit(root, "clone", "--quiet", alias, checkout); err != nil {
		t.Fatalf("clone through route run: %v\n%s", err, out)
	}
	expectDiscovery()
	const sourceGitLibrary = `alias.tfsaz-library=!f() { . "$(git --exec-path)/git-sh-setup" && test -f "$(git --exec-path)/mergetools/vimdiff"; }; f`
	if out, err := wrappedGit(checkout, "-c", sourceGitLibrary, "tfsaz-library"); err != nil {
		t.Fatalf("Git could not load its shell library and mergetools from the routed exec path: %v\n%s", err, out)
	}
	if out, err := wrappedGit(root, "ls-remote", strings.TrimSuffix(alias, "Tool%20Kit")+"RequiresAuth"); err == nil || !strings.Contains(out, "terminal prompts disabled") {
		t.Fatalf("expected a non-interactive authentication failure: %v\n%s", err, out)
	}
	// A killed run whose launched process has exited too.
	if err := os.MkdirAll(configPath+".run/git-killed", 0700); err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, configPath+".run/git-killed.lock", "")
	// Git for Windows may print the exec path with forward slashes.
	if out, err := wrappedGit(root, "--exec-path"); err != nil || !strings.EqualFold(filepath.Dir(filepath.FromSlash(strings.TrimSpace(out))), configPath+".run") {
		t.Fatalf("route run overlay %q is not beside TFSAZ_CONFIG: %v", out, err)
	}
	for _, path := range []string{configPath + ".run", filepath.Join(userConfig, "tfsaz")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("route run left %s: %v", path, err)
		}
	}
	t.Setenv("GIT_EXEC_PATH", helperDir)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	for _, command := range [][]string{{"config", "--get-all", "remote.origin.url"}, {"remote", "get-url", "origin"}} {
		if got := gitCommand(checkout, command...); got != alias {
			t.Fatalf("git %v = %q, want %q", command, got, alias)
		}
	}
	if got := gitCommand(checkout, "remote", "-v"); !strings.Contains(got, "origin\t"+alias+" (fetch)") {
		t.Fatalf("T3's remote discovery did not see the Azure alias: %q", got)
	}
	// The launching Git process has closed stdin. Its helper protocol still
	// uses the pipes Git opens for discovery and fetch commands.
	gitCommand(checkout, "fetch", "origin")
	expectDiscovery()

	registry, err := setupRegistryPath()
	if err != nil {
		t.Fatal(err)
	}
	managed := setupRecord{
		Repo: checkout, GitDir: filepath.Join(checkout, ".git"), ProfileID: "fixture", Phase: setupPhaseActive,
		CollectionURL: server.URL + "/tfs/Second%20Collection", Project: "Team Project", Repository: "Tool Kit",
		OriginalURL: server.URL + repoPath, RemoteURL: alias, SSHCommand: "fixture",
	}
	managed.Host = setupHost(managed.ProfileID, managed.CollectionURL, managed.Project, managed.Repository)
	managed.SSHConfig = setupSSHPath(registry, managed.GitDir)
	data, err := json.Marshal([]setupRecord{managed})
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, registry, string(data))
	managedAlias := "https://" + managed.Host + "/Team%20Project/_git/Tool%20Kit"
	gitCommand(root, "ls-remote", managedAlias)
	expectDiscovery()
	for _, bad := range []string{managedAlias + "Extra", managedAlias + "/pullrequest/42", managedAlias + "?x=y", managedAlias + "%2Fextra"} {
		if _, err := gitHTTPSDestination(bad); err == nil {
			t.Fatalf("managed alias should refuse invalid scope or path: %s", bad)
		}
	}

	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	gitCommand(root, "ls-remote", server.URL+repoPath)
	expectDiscovery()

	runHelper := func(executable string) (int, string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), executable, "origin", server.URL+"/denied")
		cmd.Stdin = strings.NewReader("capabilities\nlist\n")
		out, err := cmd.CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("expected the HTTPS helper to fail: %s %v", out, err)
		}
		return exit.ExitCode(), string(out)
	}
	wantStatus, _ := runHelper(filepath.Join(core, gitHTTPSHelperName()))
	if status, out := runHelper(helper); status != wantStatus || strings.Contains(out, "tfsaz:") {
		t.Fatalf("wrong vendor exit status or diagnostics: %d, want %d\n%s", status, wantStatus, out)
	}
	t.Setenv("TFSAZ_GIT_CORE", helperDir)
	if _, out := runHelper(helper); !strings.Contains(out, "points to TFSaz itself") {
		t.Fatalf("expected recursion guard: %s", out)
	}
	copiedCore := t.TempDir()
	copyTestExecutable(t, binary, filepath.Join(copiedCore, gitHTTPSHelperName()))
	t.Setenv("TFSAZ_GIT_CORE", copiedCore)
	if _, out := runHelper(helper); !strings.Contains(out, "points to TFSaz itself") {
		t.Fatalf("expected copied helper recursion guard: %s", out)
	}
	t.Setenv("TFSAZ_GIT_CORE", "relative")
	if _, out := runHelper(helper); !strings.Contains(out, "must be the absolute path") {
		t.Fatalf("expected absolute Git exec directory guard: %s", out)
	}
}

func TestGitHTTPSDestinationRejectsUnboundManagedAliases(t *testing.T) {
	t.Setenv("TFSAZ_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	for _, raw := range []string{
		"https://tfsaz-0123456789abcdef01234567.visualstudio.com/Project/_git/Repo",
		"https://tfsaz-0123456789abcdef01234567.visualstudio.com:invalid/Project/_git/Repo",
		"https://dev.azure.com/tfsaz-0123456789abcdef01234567/Project/_git/Repo",
		"https://dev.azure.com/%74fsaz-0123456789abcdef01234567/Project/_git/%",
	} {
		if _, err := gitHTTPSDestination(raw); err == nil {
			t.Fatalf("unbound managed alias passed to original Git helper: %s", raw)
		}
	}
	for _, raw := range []string{"https://dev.azure.com/real/Project/_git/Repo", "https://native.invalid/Collection/Project/_git/Repo"} {
		if got, err := gitHTTPSDestination(raw); err != nil || got != raw {
			t.Fatalf("ordinary Git URL changed: %q, %v", got, err)
		}
	}
}
