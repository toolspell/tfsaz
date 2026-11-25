package az

import (
	"archive/zip"
	"bytes"
	"encoding/json"
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

const (
	invokeFixtureCollection = "/tfs/Fixture Collection"
	invokeFixtureRepo       = "/Shared Project/_apis/git/repositories/Tool Kit"
	invokeFixtureItem       = "/docs/notes #1&+%2F.md"
	invokeFixtureHead       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	invokeFixtureBase       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	invokeFixtureNewBlob    = "cccccccccccccccccccccccccccccccccccccccc"
	invokeFixtureOldBlob    = "dddddddddddddddddddddddddddddddddddddddd"
)

type invokeFixture struct {
	reads map[string]json.RawMessage
	zip   []byte
}

func newInvokeFixture(t *testing.T) invokeFixture {
	t.Helper()
	var fixture invokeFixture
	if err := json.Unmarshal([]byte(repoFixture(t, "invoke-pr-reads")), &fixture.reads); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, blob := range []struct{ id, content string }{
		{invokeFixtureOldBlob, "original line\n"},
		{invokeFixtureNewBlob, "updated line\n"},
	} {
		entry, err := writer.Create(blob.id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, blob.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.zip = archive.Bytes()
	return fixture
}

func (f invokeFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request, path string) bool {
	t.Helper()
	suffix, ok := strings.CutPrefix(path, invokeFixtureRepo+"/")
	if !ok {
		return false
	}
	query := r.URL.Query()
	if query.Get("api-version") != "7.1" {
		t.Error("unexpected invoke API version")
	}
	if suffix == "blobs" {
		if len(query) != 1 || r.Method != http.MethodPost || r.Header.Get("Accept") != "application/zip" ||
			r.Header.Get("Content-Type") != "application/json" {
			t.Error("unexpected blob ZIP request")
		}
		var ids []string
		if err := json.NewDecoder(r.Body).Decode(&ids); err != nil ||
			!slices.Equal(ids, []string{invokeFixtureOldBlob, invokeFixtureNewBlob}) {
			t.Error("unexpected blob IDs")
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Write(f.zip)
		return true
	}
	if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/json" {
		t.Error("unexpected invoke JSON request")
	}
	var name string
	switch suffix {
	case "pullRequests/42/threads", "pullRequests/42/labels", "pullRequests/42/iterations":
		name = strings.TrimPrefix(suffix, "pullRequests/42/")
		if len(query) != 1 {
			t.Error("unexpected list query")
		}
	case "pullRequests/42/iterations/2/changes":
		if len(query) != 3 || query.Get("$top") != "2000" {
			t.Error("unexpected changes query")
		}
		switch query.Get("$skip") {
		case "0":
			name = "changes-first"
			w.Header().Set("X-MS-ContinuationToken", "fixture-token")
		case "1":
			name = "changes-second"
		}
	case "items":
		if len(query) != 7 || query.Get("versionDescriptor.versionType") != "commit" ||
			query.Get("includeContent") != "true" || query.Get("includeContentMetadata") != "true" ||
			query.Get("$format") != "json" {
			t.Error("unexpected item query")
		}
		switch {
		case query.Get("path") == invokeFixtureItem && query.Get("versionDescriptor.version") == invokeFixtureHead:
			name = "item-main"
		case query.Get("path") == invokeFixtureItem && query.Get("versionDescriptor.version") == invokeFixtureBase:
			name = "item-base"
		case query.Get("path") == "/extra.md" && query.Get("versionDescriptor.version") == invokeFixtureHead:
			name = "item-extra"
		}
	}
	data, ok := f.reads[name]
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
	return true
}

func invokeArgs(resource string, routeTail, extra []string) []string {
	args := []string{"devops", "invoke", "--detect", "true", "--area", "git", "--resource", resource,
		"--api-version", "7.1", "--route-parameters", "project=Shared Project", "repositoryId=Tool Kit"}
	args = append(args, routeTail...)
	return append(args, extra...)
}

func invokeItemArgs(path string) []string {
	return []string{"--query-parameters", "path=" + path, "versionDescriptor.versionType=commit",
		"versionDescriptor.version=" + invokeFixtureHead, "includeContent=true", "includeContentMetadata=true",
		"$format=json", "--only-show-errors", "-o", "json"}
}

func TestInvokeWithCollectionFixture(t *testing.T) {
	fixture := newInvokeFixture(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		checkClientTestAuth(t, r)
		path, ok := strings.CutPrefix(r.URL.Path, invokeFixtureCollection)
		if !ok || !fixture.serve(t, w, r, path) {
			t.Errorf("unexpected invoke fixture request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := clientTestProfile(t, server, invokeFixtureCollection)
	p.Organization = "https://fixture.visualstudio.com"
	setConfig(t, p)
	cwd := t.TempDir()
	gitAt(t, cwd, "init", "-q")
	gitAt(t, cwd, "remote", "add", "origin", p.Organization+"/Shared%20Project/_git/Tool%20Kit")
	store := &memoryStore{values: map[string]string{p.ID: clientTestPassword}}
	var stdout bytes.Buffer
	a := app{cwd: cwd, stdout: &stdout, store: store}

	pr := []string{"pullRequestId=42"}
	changes := []string{"pullRequestId=42", "iterationId=2"}
	for _, tc := range []struct {
		resource       string
		route, extra   []string
		fixture, token string
	}{
		{"pullRequestThreads", pr, nil, "threads", "null"},
		{"pullRequestLabels", pr, []string{"--org", p.Organization, "--detect=false"}, "labels", "null"},
		{"pullRequestIterations", pr, nil, "iterations", "null"},
		{"pullRequestIterationChanges", changes, []string{"--query-parameters", "$top=2000", "$skip=0"}, "changes-first", `"fixture-token"`},
		{"pullRequestIterationChanges", changes, []string{"--query-parameters=$skip=1", "$top=2000"}, "changes-second", "null"},
		{"items", nil, invokeItemArgs("%2Fdocs%2Fnotes%20%231%26+%252F.md"), "item-main", "null"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			stdout.Reset()
			args := invokeArgs(tc.resource, tc.route, tc.extra)
			if tc.fixture == "labels" {
				args = slices.Delete(args, 2, 4) // Use explicit organization alone.
			}
			if err := a.run(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(fixture.reads[tc.fixture], &want); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(got["continuation_token"])
			if string(data) != tc.token {
				t.Errorf("continuation token = %s, want %s", data, tc.token)
			}
			delete(got, "continuation_token")
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("got %s, want %s", gotJSON, wantJSON)
			}
		})
	}
	stdout.Reset()
	input, err := json.Marshal([]string{invokeFixtureOldBlob, invokeFixtureNewBlob})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "ids.json"), input, 0600); err != nil {
		t.Fatal(err)
	}
	args := invokeBlobArgs("ids.json", "result.zip")
	if err := a.run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Error("ZIP invoke wrote to standard output")
	}
	got, err := os.ReadFile(filepath.Join(cwd, "result.zip"))
	if err != nil || !bytes.Equal(got, fixture.zip) {
		t.Fatalf("ZIP output differs: %v", err)
	}
	if err := a.run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("overwrite error = %v", err)
	}
	if requests.Load() != 7 {
		t.Errorf("unexpected fixture request count %d", requests.Load())
	}
}

func invokeBlobArgs(in, out string) []string {
	return invokeArgs("blobs", nil, []string{"--http-method", "POST", "--in-file", in, "--out-file", out,
		"--accept-media-type", "application/zip", "--only-show-errors"})
}

func TestStockGitInvokeShapes(t *testing.T) {
	pr := []string{"pullRequestId=42"}
	for _, tc := range []struct {
		name, resource string
		route, extra   []string
		match          string
	}{
		{"other API", "projects", nil, nil, "unsupported devops invoke resource"},
		{"write", "pullRequestThreads", pr, []string{"--http-method=POST"}, "JSON GET only"},
		{"arbitrary query", "pullRequestLabels", pr, []string{"--query-parameters", "api-version=99"}, "takes no query parameters"},
		{"unexpected route", "pullRequestLabels", append(slices.Clone(pr), "threadId=1"), nil, "unexpected devops invoke route parameter"},
		{"invalid ID", "pullRequestLabels", []string{"pullRequestId=2147483648"}, nil, "integer"},
		{"route traversal", "pullRequestLabels", []string{"pullRequestId=42/../../.."}, nil, "integer"},
		{"duplicate route", "pullRequestLabels", append(slices.Clone(pr), "repositoryId=Other"), nil, "duplicate"},
		{"oversized page", "pullRequestIterationChanges", []string{"pullRequestId=42", "iterationId=2"},
			[]string{"--query-parameters", "$top=2001"}, "integer"},
		{"invalid escape", "items", nil, invokeItemArgs("%2Fdocs%"), "valid absolute Git path"},
		{"oversized path", "items", nil, invokeItemArgs("/" + strings.Repeat("x", maxInvokeItemPathBytes)), "valid absolute Git path"},
		{"control in path", "items", nil, invokeItemArgs("%2Fdocs%00.md"), "valid absolute Git path"},
		{"wrong blob method", "blobs", nil, []string{"--http-method=GET"}, "blobs requires POST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := invokeArgs(tc.resource, tc.route, tc.extra)
			_, opts, err := parse(args)
			if err == nil {
				_, err = parseStockInvoke(opts, "")
			}
			if err == nil || !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("error = %v, want %q", err, tc.match)
			}
		})
	}
}

func TestInvokeBlobFailureLeavesDestinationUntouched(t *testing.T) {
	fixture := newInvokeFixture(t)
	for _, tc := range []struct {
		name, input, match string
	}{
		{"invalid input", `{"id":"invalid"}`, "array"},
		{"oversized input", strings.Repeat(" ", maxBlobInputBytes+1), "small regular JSON file"},
		{"redirect", `["` + invokeFixtureOldBlob + `"]`, "redirect"},
		{"invalid zip", `["` + invokeFixtureOldBlob + `"]`, "invalid ZIP archive"},
		{"concurrent creation", `["` + invokeFixtureOldBlob + `"]`, "already exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			out := filepath.Join(cwd, "result.zip")
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				checkClientTestAuth(t, r)
				if r.URL.Path != invokeFixtureCollection+invokeFixtureRepo+"/blobs" || r.Method != http.MethodPost {
					t.Error("unexpected request for blob fixture")
					http.NotFound(w, r)
					return
				}
				switch tc.name {
				case "redirect":
					w.Header().Set("Location", "/another-collection")
					w.WriteHeader(http.StatusTemporaryRedirect)
				case "invalid zip":
					w.Header().Set("Content-Type", "application/zip")
					io.WriteString(w, "not a zip")
				case "concurrent creation":
					if err := os.WriteFile(out, []byte("keep this"), 0600); err != nil {
						t.Error(err)
					}
					w.Header().Set("Content-Type", "application/zip")
					w.Write(fixture.zip)
				default:
					t.Error("invalid file reached the server")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			p := clientTestProfile(t, server, invokeFixtureCollection)
			p.Organization = "https://fixture.visualstudio.com"
			setConfig(t, p)
			a := app{cwd: cwd, stdout: io.Discard, store: &memoryStore{values: map[string]string{p.ID: clientTestPassword}}}
			if err := os.WriteFile(filepath.Join(cwd, "ids.json"), []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			args := append(invokeBlobArgs("ids.json", "result.zip"), "--org", p.Organization)
			err := a.run(t.Context(), args)
			if err == nil || !strings.Contains(err.Error(), tc.match) {
				t.Fatalf("error = %v, want %q", err, tc.match)
			}
			data, err := os.ReadFile(out)
			if tc.name == "concurrent creation" {
				if err != nil || string(data) != "keep this" {
					t.Fatalf("existing destination changed: %v", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("unexpected output file state: %v", err)
			}
			if tc.name == "redirect" && calls.Load() != 1 {
				t.Errorf("POST redirect produced %d requests", calls.Load())
			}
		})
	}
}

func TestBlobArchiveAboveT3DiffBudget(t *testing.T) {
	// Stock T3 checks the file size to select its per-item fallback.
	const megabytes = 65
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		archive := zip.NewWriter(w)
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: invokeFixtureOldBlob, Method: zip.Store})
		if err != nil {
			t.Error(err)
			return
		}
		chunk := make([]byte, 1<<20)
		for range megabytes {
			if _, err := entry.Write(chunk); err != nil {
				t.Error(err)
				return
			}
		}
		if err := archive.Close(); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ids"), []byte(`["`+invokeFixtureOldBlob+`"]`), 0600); err != nil {
		t.Fatal(err)
	}
	client := newClientForTest(t, clientTestProfile(t, server, invokeFixtureCollection))
	read := invokeRead{path: []string{"_apis", "git", "blobs"}, inFile: "ids", outFile: "batch.zip"}
	if err := read.downloadBlobs(t.Context(), client, dir); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(filepath.Join(dir, "batch.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	if len(archive.File) != 1 || archive.File[0].UncompressedSize64 != megabytes<<20 {
		t.Fatal("incomplete large blob archive")
	}
}
