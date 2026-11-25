package az

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestDoctorChecksOnlyCheckoutCredential(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Second/_apis/connectionData" {
			t.Errorf("unexpected auth endpoint %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"authenticatedUser":{"id":%q,"properties":{"Account":{"$value":"LOCAL\\viewer"}}}}`, repoTestViewerID)
	}))
	defer server.Close()
	first := clientTestProfile(t, server, "/First")
	first.ID, first.Organization = "first", "https://first.visualstudio.com"
	second := clientTestProfile(t, server, "/Second")
	second.ID, second.Organization = "second", "https://second.visualstudio.com"
	setConfig(t, first, second)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "az"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.Link(exe, filepath.Join(bin, name)); err != nil {
		t.Skipf("test requires executable link: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitAt(t, dir, "init", "--quiet")
	gitAt(t, dir, "remote", "add", "origin", second.Organization+"/Example/_git/Repo")
	store := &memoryStore{values: map[string]string{"second": "hidden-test-token"}}
	var out bytes.Buffer
	a := app{cwd: dir, stdout: &out, stderr: &out, store: store}
	if err := a.runDoctor(t.Context(), []string{"--json"}); err != nil {
		t.Fatalf("doctor: %v, %s", err, &out)
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.SelectedProfile != "second" || !slices.Equal(store.reads, []string{"second"}) || strings.Contains(out.String(), "hidden-test-token") {
		t.Fatal("diagnostic failed to isolate checkout credentials")
	}
	delete(store.values, "second")
	out.Reset()
	if err := a.runDoctor(t.Context(), []string{"--json"}); err == nil || !json.Valid(out.Bytes()) {
		t.Fatal("doctor should emit a parseable report and fail on a missing PAT")
	}
}

func TestShimInstallCollision(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fixture")
	if err := os.WriteFile(exe, []byte("first"), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "git-remote-tfsaz")
	az := filepath.Join(dir, "az")
	if runtime.GOOS == "windows" {
		target += ".exe"
		az += ".exe"
	}
	if err := os.WriteFile(target, []byte("other"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := installShimLinks(dir, exe); err == nil {
		t.Fatal("overwrote foreign helper")
	}
	if _, err := os.Lstat(az); !os.IsNotExist(err) {
		t.Fatal("partially installed shims")
	}
}
