package az

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDoctorChecksOnlyCheckoutCredential(t *testing.T) {
	var level atomic.Value
	level.Store("7.1")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && r.URL.Path == "/Second/_apis" {
			serveTestCatalog(w, level.Load().(string))
			return
		}
		if r.URL.Path != "/Second/_apis/connectionData" {
			t.Errorf("unexpected auth endpoint %s", r.URL.Path)
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"properties":{"Account":{"$value":"LOCAL\\viewer"}}}}`, repoTestViewerID)
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
	if err := installShimLinks(bin, exe); err != nil {
		t.Fatal(err)
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
	statuses := func(report doctorReport) map[string]string {
		status := map[string]string{}
		for _, c := range report.Checks {
			status[c.Name] = c.Status
		}
		return status
	}
	if status := statuses(report); status["az"] != "ok" || status["git-remote-tfsaz"] != "ok" {
		t.Fatalf("shim checks: %v", status)
	}
	if !slices.Contains(report.Checks, doctorCheck{"api version second", "ok", "collection supports API 7.1"}) {
		t.Fatalf("doctor did not report the collection API version: %+v", report.Checks)
	}
	level.Store("6.0")
	out.Reset()
	if err := a.runDoctor(t.Context(), []string{"--json"}); err == nil {
		t.Fatal("doctor should fail on a collection below API 7.1")
	}
	report = doctorReport{}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !slices.ContainsFunc(report.Checks, func(c doctorCheck) bool {
		return c.Name == "api version second" && c.Status == "error" && strings.Contains(c.Message, "supports API 6.0 at most")
	}) {
		t.Fatalf("doctor did not report the server below API 7.1: %v, %s", err, &out)
	}
	delete(store.values, "second")
	stale := shimPath(bin, "git-remote-tfsaz")
	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("older fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := a.runDoctor(t.Context(), []string{"--json"}); err == nil || !json.Valid(out.Bytes()) {
		t.Fatal("doctor should emit a parseable report and fail on a missing PAT")
	}
	report = doctorReport{}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if status := statuses(report); status["az"] != "ok" || status["git-remote-tfsaz"] != "error" ||
		status["authentication second"] != "error" {
		t.Fatalf("stale shim and missing PAT checks: %v", status)
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
