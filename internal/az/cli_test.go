package az

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type memoryStore struct {
	values        map[string]string
	reads         []string
	lockedProfile string
}

func memoryCredentialKey(service, id string) string {
	if service == keyringService {
		return id
	}
	return service + "/" + id
}

func (s *memoryStore) Get(service, id string) (string, error) {
	s.reads = append(s.reads, id)
	if s.lockedProfile == id {
		return "", errors.New("unavailable")
	}
	v, ok := s.values[memoryCredentialKey(service, id)]
	if !ok {
		return "", errNoCredential
	}
	return v, nil
}

func (s *memoryStore) Set(service, id, pat string) error {
	if s.lockedProfile == id {
		return errors.New("unavailable")
	}
	if s.values == nil {
		s.values = make(map[string]string)
	}
	s.values[memoryCredentialKey(service, id)] = pat
	return nil
}

func (s *memoryStore) Delete(service, id string) error {
	if s.lockedProfile == id {
		return errors.New("unavailable")
	}
	key := memoryCredentialKey(service, id)
	if _, ok := s.values[key]; !ok {
		return errNoCredential
	}
	delete(s.values, key)
	return nil
}

func TestDiscoveryAndCheckoutIsolation(t *testing.T) {
	// Both collections share an authority, and PR numbers can collide.
	var directoryUnavailable atomic.Bool
	keys := map[string]string{"alpha": rand.Text(), "beta": rand.Text()}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id, base string
		switch {
		case strings.HasPrefix(r.URL.Path, "/First/"):
			id, base = "alpha", "/First"
		case strings.HasPrefix(r.URL.Path, "/tfs/Second Collection/"):
			id, base = "beta", "/tfs/Second Collection"
		default:
			t.Error("unexpected collection")
			http.NotFound(w, r)
			return
		}
		_, key, _ := r.BasicAuth()
		if key != keys[id] {
			if key == keys["alpha"] || key == keys["beta"] {
				t.Error("credential sent to the wrong collection")
			}
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		switch strings.TrimPrefix(r.URL.Path, base) {
		case "/_apis/connectionData":
			serveTestf(w, `{"authenticatedUser":{"id":%q,"properties":{"Account":{"$value":%q}}}}`, repoTestViewerID, id)
		case "/_apis/identities":
			if directoryUnavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if r.URL.Query().Get("identityIds") != repoTestViewerID {
				t.Error("identity lookup must use the authenticated user's collection ID")
			}
			serveTestf(w, `{"value":[{"id":%q,"uniqueName":%q}]}`, repoTestViewerID, id+"@example.invalid")
		case "/_apis/git/pullrequests/42":
			serveTestBody(w, repoFixture(t, "pull-request"))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	alpha := clientTestProfile(t, server, "/First")
	alpha.ID, alpha.Organization = "alpha", "https://alpha.visualstudio.com"
	beta := clientTestProfile(t, server, "/tfs/Second Collection")
	beta.ID, beta.Organization = "beta", "https://dev.azure.com/beta"
	setConfig(t, alpha, beta)
	cwd := t.TempDir()
	gitAt(t, cwd, "init", "-q")
	gitAt(t, cwd, "remote", "add", "origin", beta.Organization+"/Shared%20Project/_git/Tool%20Kit")
	store := &memoryStore{values: map[string]string{"alpha": keys["alpha"], "beta": keys["beta"]}}
	var out, diag bytes.Buffer
	a := app{cwd: cwd, stdout: &out, stderr: &diag, store: store}
	const global = "account show --query user.name -o tsv"
	const viewer = "account show --query user --only-show-errors --output json"
	check := func(args, want, errorText string, reads ...string) {
		t.Helper()
		out.Reset()
		store.reads = nil
		err := a.run(t.Context(), strings.Fields(args))
		if errorText != "" {
			if err == nil || !strings.Contains(err.Error(), errorText) || out.Len() != 0 {
				t.Fatalf("%s: unexpected error %v or output %q", args, err, out.String())
			}
		} else if err != nil || !strings.Contains(out.String(), want) {
			t.Fatalf("%s: %v, output %q", args, err, out.String())
		}
		if !slices.Equal(store.reads, reads) {
			t.Fatalf("%s queried profiles %v, want %v", args, store.reads, reads)
		}
	}
	check(global, "alpha@example.invalid\n", "", "alpha")
	directoryUnavailable.Store(true)
	check(global, "alpha\n", "", "alpha")
	directoryUnavailable.Store(false)
	check(viewer, `"name":"beta@example.invalid"`, "", "beta")
	check("repos pr show --detect true --id 42", "https://dev.azure.com/beta/Shared%20Project/_git/Tool%20Kit/pullrequest/42", "", "beta")
	check("repos pr show --detect false --id 42 -o json --organization "+alpha.Organization, "https://alpha.visualstudio.com/Shared%20Project/_git/Tool%20Kit/pullrequest/42", "", "alpha")
	const repoPath = "/Shared%20Project/_git/Tool%20Kit"
	const alphaCanonical = "https://dev.azure.com/alpha"
	gitAt(t, cwd, "remote", "set-url", "origin", alphaCanonical+repoPath)
	check(viewer, `"name":"alpha@example.invalid"`, "", "alpha")
	check("repos pr show --id 42 --only-show-errors --output json --organization "+alphaCanonical, alphaCanonical+repoPath+"/pullrequest/42", "", "alpha")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := setupRegistryPath()
	if err != nil {
		t.Fatal(err)
	}
	managed := setupRecord{
		Repo: cwd, GitDir: filepath.Join(cwd, ".git"), Phase: setupPhaseActive, ProfileID: beta.ID,
		CollectionURL: cfg.Profiles[1].CollectionURL, Project: "Shared Project", Repository: "Tool Kit",
		RemoteURL: beta.Organization + repoPath, OriginalURL: cfg.Profiles[1].CollectionURL + repoPath, SSHCommand: "fixture",
	}
	managed.Host = setupHost(managed.ProfileID, managed.CollectionURL, managed.Project, managed.Repository)
	managed.SSHConfig = setupSSHPath(registry, managed.GitDir)
	data, err := json.Marshal([]setupRecord{managed})
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, registry, string(data))
	managedCanonical := canonicalAzureOrganization("https://" + managed.Host)
	gitAt(t, cwd, "remote", "set-url", "origin", managedCanonical+repoPath)
	check(viewer, `"name":"beta@example.invalid"`, "", "beta")
	check("repos pr show --id 42 --only-show-errors --output json --organization "+managedCanonical, managedCanonical+repoPath+"/pullrequest/42", "", "beta")
	gitAt(t, cwd, "remote", "set-url", "origin", beta.Organization+repoPath)

	store.values["beta"] = rand.Text() // Simulate revocation in this collection only.
	check(viewer, "", "rejected the PAT", "beta")
	check("repos pr list --detect true --creator me --status all --top 1", "", "rejected the PAT", "beta")
	check("repos pr show --detect true --id 42", "", "rejected the PAT", "beta")
	check(global, "alpha@example.invalid", "", "alpha")
	store.values["alpha"], store.values["beta"] = rand.Text(), keys["beta"]
	check(global, "beta@example.invalid", "", "alpha", "beta")
	check(viewer+" --org=https://alpha.visualstudio.com", "", "rejected the PAT", "alpha")
	store.lockedProfile = "alpha"
	check(global, "beta@example.invalid", "", "alpha", "beta")
	if !strings.Contains(diag.String(), "credential store locked or unavailable") {
		t.Fatal("missing store diagnostic")
	}
	store.lockedProfile = ""
	delete(store.values, "beta")
	check(global, "", "has no PAT", "alpha", "beta")
	check(viewer+" --org=https://unconfigured.visualstudio.com", "", "does not match")
	store.lockedProfile = "beta"
	check(viewer, "", "credential store locked or unavailable", "beta")
}

func TestLoginFromStdinVerifiesBeforeStoring(t *testing.T) {
	pat := rand.Text()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, ok := r.BasicAuth(); !ok || password != pat {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodOptions {
			serveTestCatalog(w, "7.1")
			return
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"fixture@example.invalid"}}`, repoTestViewerID)
	}))
	defer server.Close()
	p := clientTestProfile(t, server, "/One")
	p.Organization = "https://dev.azure.com/one"
	setConfig(t, p)
	store := &memoryStore{values: map[string]string{}}
	run := func(input string) error {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer closeQuietly(r)
		if _, err := io.WriteString(w, input); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		a := app{stdout: &output, stderr: &output, stdin: r, store: store}
		err = a.run(t.Context(), []string{"devops", "login", "--org", p.Organization})
		if output.Len() != 0 || (err != nil && strings.Contains(err.Error(), pat)) {
			t.Fatal("unexpected login output")
		}
		return err
	}
	if err := run(pat + "\r\n"); err != nil || store.values[p.ID] != pat {
		t.Fatalf("login failed: %v", err)
	}
	if err := run(rand.Text()); err == nil || store.values[p.ID] != pat {
		t.Fatal("rejected PAT replaced a working credential")
	}
	store.lockedProfile = p.ID
	if err := run(pat); err == nil || !strings.Contains(err.Error(), "credential store") {
		t.Fatal("write to locked store should fail")
	}
}

func TestCLIUsageAndJoinedFlags(t *testing.T) {
	t.Setenv("TFSAZ_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	var out bytes.Buffer
	a := app{stdout: &out}
	if err := a.run(t.Context(), []string{"--version"}); err != nil || !strings.HasPrefix(out.String(), "tfsaz ") {
		t.Fatal("version requires configuration")
	}
	for _, args := range [][]string{
		{"repos", "pr", "update", "--id", "42"},
		{"devops", "login", "--pat=private-input"},
		{"account", "show", "--query", "user.name", "-o", "json"},
		{"repos", "pr", "list", "--organization", "--detect", "true"},
	} {
		if err := a.run(t.Context(), args); err == nil || strings.Contains(err.Error(), "private-input") {
			t.Fatal("expected sanitized usage error")
		}
	}
	_, opts, err := parse([]string{"repos", "pr", "list", "--project=-P", "--repository=-R", "--include-links", "--detect", "false", "--org=https://dev.azure.com/test", "--only-show-errors", "--output", "json"})
	if err != nil || opts["project"] != "-P" || opts["repository"] != "-R" || opts["include-links"] != "true" {
		t.Fatalf("joined options: %v, %v", opts, err)
	}
}

func TestConfigRejectsUnknownSecretFields(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"pat": "private-input"})
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TFSAZ_CONFIG", path)
	if _, err := loadConfig(); err == nil || strings.Contains(err.Error(), "private-input") {
		t.Fatal("unknown config field accepted or echoed")
	}
}
