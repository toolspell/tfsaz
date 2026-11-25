package az

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type memoryStore struct {
	values        map[string]string
	reads         []string
	lockedProfile string
}

func (s *memoryStore) Get(id string) (string, error) {
	s.reads = append(s.reads, id)
	if s.lockedProfile == id {
		return "", errors.New("unavailable")
	}
	v, ok := s.values[id]
	if !ok {
		return "", errNoCredential
	}
	return v, nil
}

func (s *memoryStore) Set(id, pat string) error {
	if s.lockedProfile == id {
		return errors.New("unavailable")
	}
	s.values[id] = pat
	return nil
}

func TestDiscoveryAndCheckoutIsolation(t *testing.T) {
	// Both collections share an authority, and PR numbers can collide.
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
			fmt.Fprintf(w, `{"authenticatedUser":{"id":%q,"uniqueName":%q}}`, repoTestViewerID, id+"@example.invalid")
		case "/_apis/git/pullrequests/42":
			io.WriteString(w, repoFixture(t, "pull-request"))
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
	check(viewer, `"name":"beta@example.invalid"`, "", "beta")
	check("repos pr show --detect true --id 42", "https://dev.azure.com/beta/Shared%20Project/_git/Tool%20Kit/pullrequest/42", "", "beta")
	check("repos pr show --detect false --id 42 -o json --organization "+alpha.Organization, "https://alpha.visualstudio.com/Shared%20Project/_git/Tool%20Kit/pullrequest/42", "", "alpha")
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
		fmt.Fprintf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"fixture@example.invalid"}}`, repoTestViewerID)
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
		defer r.Close()
		if _, err := io.WriteString(w, input); err != nil {
			t.Fatal(err)
		}
		w.Close()
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
