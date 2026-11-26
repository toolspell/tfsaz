package az

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofrs/flock"
)

func profileTestInput(t *testing.T, pat string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(r) })
	if _, err := io.WriteString(w, pat+"\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestProfileOnboardingAndLogout(t *testing.T) {
	firstPAT, secondPAT := rand.Text(), rand.Text()
	var firstRequests, secondRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sent, _ := r.BasicAuth()
		var pat, account, level string
		switch r.URL.EscapedPath() {
		case "/First/_apis/connectionData":
			firstRequests.Add(1)
			pat, account = firstPAT, "first@example.invalid"
		case "/First/_apis":
			pat, level = firstPAT, "7.1"
		case "/prefix/Second%20Collection/_apis/connectionData":
			secondRequests.Add(1)
			pat, account = secondPAT, "second@example.invalid"
		case "/prefix/Second%20Collection/_apis":
			pat, level = secondPAT, "6.0"
		default:
			t.Error("profile login sent a request outside the configured collection")
			http.NotFound(w, r)
			return
		}
		if sent != pat {
			t.Error("profile login did not use the credential for its configured collection")
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		if level != "" {
			serveTestCatalog(w, level)
			return
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":%q}}`, repoTestViewerID, account)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "fresh", "config.json")
	t.Setenv("TFSAZ_CONFIG", path)
	ca := clientTestProfile(t, server, "/First").CAFile
	store := &memoryStore{values: map[string]string{"shared": rand.Text()}}
	legacy := store.values["shared"]
	var out bytes.Buffer
	a := app{cwd: filepath.Dir(ca), stdout: &out, stderr: &out, store: store}
	run := func(args ...string) string {
		t.Helper()
		out.Reset()
		if err := a.run(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := run("profile", "list"); got != "No collection profiles configured\n" || len(store.reads) != 0 {
		t.Fatal("fresh profile list must work without a credential store")
	}
	a.stdin = profileTestInput(t, firstPAT)
	if got := run("init", "--id=shared", "--collection", server.URL+"/First/", "--ca-file", filepath.Base(ca)); strings.Contains(got, "WARNING") {
		t.Fatalf("init warned about a compatible collection: %q", got)
	}
	firstCfg, err := loadConfig()
	if err != nil || len(firstCfg.Profiles) != 1 {
		t.Fatalf("load initialized config: %v", err)
	}
	first := firstCfg.Profiles[0]
	if first.CollectionURL != server.URL+"/First" || first.CAFile != ca || first.ID != "shared" ||
		!strings.HasSuffix(first.Organization, ".visualstudio.com") || first.CredentialVersion != collectionCredentialVersion {
		t.Fatalf("unexpected initialized profile: %#v", first)
	}

	run("profile", "add", "--collection="+server.URL+"/prefix/Second Collection", "--ca-file", ca, "--no-login")
	cfg, err := loadConfig()
	if err != nil || len(cfg.Profiles) != 2 {
		t.Fatalf("load two profiles: %v", err)
	}
	second := cfg.Profiles[1]
	if second.CollectionURL != server.URL+"/prefix/Second%20Collection" || second.Organization == first.Organization {
		t.Fatalf("unexpected second profile: %#v", second)
	}
	if firstRequests.Load() != 1 || secondRequests.Load() != 0 {
		t.Fatal("config-only onboarding made a server request")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run("init", "--collection", server.URL+"/prefix/Second%20Collection/", "--ca-file", ca, "--no-login")
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || bytes.Contains(after, []byte(firstPAT)) {
		t.Fatal("an equivalent retry changed the saved configuration or saved a PAT")
	}
	wantList := fmt.Sprintf("%s\t%s\t%s\n%s\t%s\t%s\n",
		first.ID, first.Organization, first.CollectionURL, second.ID, second.Organization, second.CollectionURL)
	if got := run("profile", "list"); got != wantList {
		t.Fatalf("profile list = %q, want %q", got, wantList)
	}
	a.stdin = profileTestInput(t, secondPAT)
	if got := run("devops", "login", "--organization", second.Organization); got != "WARNING: the collection supports API 6.0 at most; TFSaz needs API 7.1 (Azure DevOps Server 2022.1 or later)\n" {
		t.Fatalf("devops login on a server below API 7.1 wrote %q", got)
	}
	if got := run("account", "show", "--query", "user.name", "-o", "tsv"); got != "first@example.invalid\n" {
		t.Fatalf("unexpected global account: %q", got)
	}
	for range 2 {
		run("profile", "logout", "--profile", first.ID)
	}
	if got := run("account", "show", "--query", "user.name", "-o", "tsv"); got != "second@example.invalid\n" {
		t.Fatalf("unexpected global account after logout: %q", got)
	}
	if store.values["shared"] != legacy || secondRequests.Load() != 2 {
		t.Fatal("logout changed credentials outside the selected profile")
	}
	if _, err := os.Lstat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile commands left the configuration lock: %v", err)
	}
}

func TestProfileConfigFilesCanReuseIDForDifferentEndpoints(t *testing.T) {
	pat := rand.Text()
	var foreignRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && r.URL.Path == "/First/_apis" {
			serveTestCatalog(w, "7.1")
			return
		}
		if r.URL.Path != "/First/_apis/connectionData" {
			foreignRequests.Add(1)
			http.NotFound(w, r)
			return
		}
		if _, sent, ok := r.BasicAuth(); !ok || sent != pat {
			t.Error("incorrect credential for first collection")
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"first@example.invalid"}}`, repoTestViewerID)
	}))
	defer server.Close()
	ca := clientTestProfile(t, server, "/First").CAFile
	a := app{stdout: io.Discard, stderr: io.Discard, store: &memoryStore{}}
	run := func(args ...string) error {
		return a.run(t.Context(), args)
	}
	firstPath := filepath.Join(t.TempDir(), "first.json")
	secondPath := filepath.Join(t.TempDir(), "second.json")
	for path, endpoint := range map[string]string{firstPath: "/First", secondPath: "/Second"} {
		t.Setenv("TFSAZ_CONFIG", path)
		if err := run("init", "--id", "shared", "--collection", server.URL+endpoint, "--ca-file", ca, "--no-login"); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TFSAZ_CONFIG", firstPath)
	a.stdin = profileTestInput(t, pat)
	if err := run("devops", "login", "--organization", server.URL+"/First"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TFSAZ_CONFIG", secondPath)
	if err := run("account", "show", "--query", "user.name", "-o", "tsv"); err == nil || !strings.Contains(err.Error(), "has no PAT") || foreignRequests.Load() != 0 {
		t.Fatalf("second config accessed the credential for a different endpoint: %v", err)
	}
	if err := run("profile", "logout", "--profile", "shared"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TFSAZ_CONFIG", firstPath)
	if err := run("account", "show", "--query", "user.name", "-o", "tsv"); err != nil {
		t.Fatalf("second config logout affected the first credential: %v", err)
	}
}

func TestProfileAddRejectsInvalidAndConflictingInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TFSAZ_CONFIG", path)
	store := &memoryStore{}
	a := app{stdout: io.Discard, stderr: io.Discard, store: store}
	const first = "https://ado.example.invalid:8443/tfs/First%20Collection"
	const second = "https://ado.example.invalid:8443/Second"
	for _, args := range [][]string{
		{"init", "--no-login"},
		{"init", "--collection", "http://ado.example.invalid/First", "--no-login"},
		{"init", "--collection", "https://ado.example.invalid", "--no-login"},
		{"init", "--collection", "https://ado.example.invalid:65536/First", "--no-login"},
		{"init", "--collection", "https://private-marker@ado.example.invalid/First", "--no-login"},
		{"init", "--collection", "https://ado.example.invalid/tfs/%252e%252e/First", "--no-login"},
		{"init", "--collection", first, "--organization", second, "--no-login"},
		{"init", "--collection", first, "--id", "invalid/id", "--no-login"},
		{"init", "--collection", first, "--pat=private-marker"},
	} {
		if err := a.run(t.Context(), args); ExitCode(err) != 2 || strings.Contains(err.Error(), "private-marker") {
			t.Fatalf("expected sanitized usage error, got %v", err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || len(store.reads) != 0 {
		t.Fatal("invalid input changed configuration or accessed the keyring")
	}
	if err := a.run(t.Context(), []string{"profile", "add", "--id", "first", "--collection", first,
		"--organization", "https://example-org.visualstudio.com", "--no-login"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--id", "first", "--collection", second},
		{"--id", "FIRST", "--collection", second},
		{"--id", "second", "--collection", first},
		{"--id", "second", "--collection", second, "--organization", "https://dev.azure.com/example-org"},
	} {
		if err := a.run(t.Context(), append([]string{"profile", "add", "--no-login"}, args...)); err == nil {
			t.Fatalf("accepted conflicting profile: %v", args)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || len(store.reads) != 0 {
		t.Fatal("a conflicting profile changed the config or accessed the keyring")
	}
	if err := a.run(t.Context(), []string{"profile", "logout", "--profile", "unknown"}); err == nil {
		t.Fatal("logout with an unknown profile should fail")
	}
}

func TestProfileAddWithoutLoginIgnoresKeyringAvailability(t *testing.T) {
	t.Setenv("TFSAZ_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	store := &memoryStore{lockedProfile: "shared"}
	a := app{stdout: io.Discard, stderr: io.Discard, store: store}
	if err := a.run(t.Context(), []string{"profile", "add", "--id", "shared", "--collection", "https://ado.example.invalid/One", "--no-login"}); err != nil {
		t.Fatal(err)
	}
	if len(store.reads) != 0 || len(store.values) != 0 {
		t.Fatal("config-only onboarding used the keyring")
	}
}

type failingProfileStore struct {
	*memoryStore
	failSet bool
}

func (s *failingProfileStore) Set(service, id, pat string) error {
	if s.failSet {
		return errors.New("fixture write failure")
	}
	return s.memoryStore.Set(service, id, pat)
}

func TestProfileAddCredentialFailureAndRetry(t *testing.T) {
	pat := rand.Text()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, sent, _ := r.BasicAuth(); sent != pat {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"fixture@example.invalid"}}`, repoTestViewerID)
	}))
	defer server.Close()
	p := clientTestProfile(t, server, "/One")
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TFSAZ_CONFIG", path)
	store := &failingProfileStore{memoryStore: &memoryStore{}}
	var out bytes.Buffer
	a := app{stdout: &out, stderr: &out, store: store}
	args := []string{"init", "--collection", p.CollectionURL, "--id", p.ID, "--ca-file", p.CAFile}
	a.stdin = profileTestInput(t, rand.Text())
	if err := a.run(t.Context(), args); !errors.Is(err, errRejectedPAT) {
		t.Fatalf("expected rejected PAT, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || len(store.values) != 0 || out.Len() != 0 {
		t.Fatal("a rejected credential changed the configuration or the keyring")
	}
	store.lockedProfile = p.ID
	if err := a.run(t.Context(), args); !errors.Is(err, errStoreUnavailable) {
		t.Fatalf("expected locked keyring, got %v", err)
	}
	store.lockedProfile = ""
	p.CredentialVersion = collectionCredentialVersion
	if err := store.Set(credentialService(p), p.ID, rand.Text()); err != nil {
		t.Fatal(err)
	}
	if err := a.run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "already has a credential") {
		t.Fatalf("login would overwrite a pre-existing keyring credential: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused profile appears in the configuration")
	}
	delete(store.values, memoryCredentialKey(credentialService(p), p.ID))

	store.failSet = true
	a.stdin = profileTestInput(t, pat)
	if err := a.run(t.Context(), args); !errors.Is(err, errStoreUnavailable) || !strings.Contains(err.Error(), "configuration was saved") {
		t.Fatalf("expected a recoverable keyring write failure, got %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil || len(store.values) != 0 {
		t.Fatal("verified setup did not leave a retryable config-only profile")
	}
	store.failSet = false
	a.stdin = profileTestInput(t, pat)
	if err := a.run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || store.values[memoryCredentialKey(credentialService(p), p.ID)] != pat {
		t.Fatal("the exact retry changed the config or failed to save the credential")
	}
}

func TestProfileAddSerializesConfigurationChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TFSAZ_CONFIG", path)
	lock := flock.New(path + ".lock")
	if locked, err := lock.TryLock(); err != nil || !locked {
		t.Fatalf("test configuration lock: %v", err)
	}
	t.Cleanup(func() { closeQuietly(lock) })
	store := &memoryStore{}
	a := app{stdout: io.Discard, stderr: io.Discard, store: store}
	args := []string{"profile", "add", "--collection", "https://ado.example.invalid/One", "--no-login"}
	if err := a.run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "configuration lock") || len(store.reads) != 0 {
		t.Fatalf("profile write did not honor the configuration lock: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a competing write created a config")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile add left the configuration lock: %v", err)
	}
}
