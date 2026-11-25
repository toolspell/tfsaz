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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"
)

// This opt-in test runs T3's actual process-spawning adapter. It writes one
// disposable, random loopback credential to the OS keyring under a fresh ID.
func TestStockT3Adapter(t *testing.T) {
	root := os.Getenv("TFSAZ_T3_ROOT")
	if root == "" {
		t.Skip("set TFSAZ_T3_ROOT to a T3 checkout with its dependencies installed")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	pat := rand.Text()
	pr := repoFixture(t, "pull-request")
	repo := repoFixture(t, "repository")
	var policies struct{ Value []json.RawMessage }
	if err := json.Unmarshal([]byte(repoFixture(t, "policy-evaluations")), &policies); err != nil {
		t.Fatal(err)
	}
	// A full first page of filtered-out rows must not hide the next policy.
	firstPage := bytes.ReplaceAll(policies.Value[0], []byte(`"isEnabled": true`), []byte(`"isEnabled": false`))
	var first, second atomic.Int32
	const prefix = "/Fixture Collection"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, ok := r.BasicAuth(); !ok || password != pat {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			t.Error("wrong collection")
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, prefix) {
		case "/_apis/connectionData":
			fmt.Fprintf(w, `{"authenticatedUser":{"id":%q,"descriptor":"Microsoft.TeamFoundation.Identity;fixture-sid","properties":{"Account":{"$value":"author"}}}}`, repoTestViewerID)
		case "/_apis/identities":
			if r.URL.Query().Get("identityIds") != repoTestViewerID {
				t.Error("identity lookup must use the authenticated user's collection ID")
			}
			fmt.Fprintf(w, `{"value":[{"id":%q,"descriptor":"Microsoft.TeamFoundation.Identity;fixture-sid","properties":{"Account":{"$value":"author@example.invalid"}}}]}`, clientTestOtherID)
		case "/_apis/git/pullrequests/42":
			io.WriteString(w, pr)
		case "/Shared Project/_apis/git/repositories/Tool Kit":
			io.WriteString(w, repo)
		case "/Shared Project/_apis/git/repositories/Tool Kit/pullrequests":
			if r.URL.Query().Get("searchCriteria.creatorId") != repoTestViewerID {
				t.Error("missing scoped creator filter")
			}
			fmt.Fprintf(w, `{"value":[%s]}`, pr)
		case "/" + repoTestProjectID + "/_apis/policy/evaluations":
			if r.URL.Query().Get("$top") != "100" {
				t.Error("unexpected policy page size")
			}
			switch r.URL.Query().Get("$skip") {
			case "0":
				first.Add(1)
				rows := make([]json.RawMessage, 100)
				for i := range rows {
					rows[i] = firstPage
				}
				json.NewEncoder(w).Encode(map[string]any{"value": rows})
			case "100":
				last := policies.Value[0]
				if second.Add(1) > 1 {
					last = bytes.ReplaceAll(last, []byte(`"queued"`), []byte(`"approved"`))
				}
				fmt.Fprintf(w, `{"value":[%s]}`, last)
			default:
				t.Error("unexpected policy offset")
				http.NotFound(w, r)
			}
		default:
			t.Errorf("unexpected fixture path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := clientTestProfile(t, server, prefix)
	p.ID = "offline-" + rand.Text()
	p.Organization = "https://fixture.visualstudio.com"
	setConfig(t, p)
	bin := t.TempDir()
	exe := "az"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	exe = filepath.Join(bin, exe)
	build := exec.Command("go", "build", "-o", exe, "../..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		if err := keyring.Delete(keyringService, p.ID); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			t.Errorf("remove fixture keyring entry %s: %v", p.ID, err)
		}
	})
	login := exec.Command(exe, "devops", "login", "--organization", p.Organization)
	login.Stdin = strings.NewReader(pat)
	if out, err := login.CombinedOutput(); err != nil {
		t.Fatalf("fixture login: %v: %s", err, out)
	}
	cwd := t.TempDir()
	gitAt(t, cwd, "init", "-q")
	gitAt(t, cwd, "remote", "add", "origin", p.Organization+"/DefaultCollection/Shared%20Project/_git/Tool%20Kit")
	probe := exec.Command(exe, "account", "show", "--query", "user.name", "-o", "tsv")
	if out, err := probe.Output(); err != nil || string(out) != "author@example.invalid\n" {
		t.Fatalf("discovery probe: %v, %q", err, out)
	}
	run := exec.CommandContext(t.Context(), "node", "--experimental-import-meta-resolve", "testdata/t3-adapter.mjs", root, cwd)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("T3 adapter: %v: %s", err, out)
	}
	if first.Load() != 2 || second.Load() != 2 {
		t.Errorf("expected two reads of both pages, got %d and %d", first.Load(), second.Load())
	}
}
