package az

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCatalogAndRESTCommands(t *testing.T) {
	const org = "https://fixture.visualstudio.com"
	const route = "_apis/future/{resource}/{id}"
	var catalogs, patches atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkClientTestAuth(t, r)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Fixture Collection/_apis":
			catalogs.Add(1)
			if r.Method != "OPTIONS" || r.URL.Query().Get("allHostTypes") != "true" {
				t.Error("missing OPTIONS discovery")
			}
			fmt.Fprintf(w, `{"value":[{"area":"Future","resourceName":"things","routeTemplate":%q,"minVersion":"1.0","maxVersion":"7.1","releasedVersion":"7.1","resourceVersion":3}]}`, route)
		case "/Fixture Collection/_apis/future/things/7":
			switch r.Method {
			case http.MethodPatch:
				patches.Add(1)
				body, _ := io.ReadAll(r.Body)
				if string(body) != `[{"op":"test","path":"/name","value":"before"}]` || r.Header.Get("Content-Type") != "application/json-patch+json" {
					t.Error("incorrect patch body/media type")
				}
			case http.MethodGet:
			default:
				t.Error("unexpected resource method")
			}
			if (r.Method == http.MethodGet && r.URL.Query().Get("api-version") != "7.1") || (r.Method == http.MethodPatch && !strings.Contains(r.Header.Get("Accept"), "api-version=7.1")) || r.URL.Query().Get("filter") != "a&b" {
				t.Error("incorrect query or negotiated version")
			}
			w.Header().Set("X-MS-ContinuationToken", "page-two")
			io.WriteString(w, `{"name":"sample","id":7}`)
		default:
			t.Error("unexpected request path")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := clientTestProfile(t, srv, "/Fixture Collection")
	p.Organization = org
	setConfig(t, p)
	var out bytes.Buffer
	a := app{cwd: t.TempDir(), stdout: &out, store: &memoryStore{values: map[string]string{p.ID: clientTestPassword}}}
	run := func(want string, args ...string) {
		t.Helper()
		out.Reset()
		if err := a.run(t.Context(), args); err != nil || out.String() != want {
			t.Fatalf("error %v, got %q, want %q", err, out.String(), want)
		}
	}
	run(route+"\n", "devops", "invoke", "--org", org, "--query", "[?area=='Future'].routeTemplate", "-o", "tsv")
	if err := os.WriteFile(filepath.Join(a.cwd, "body.json"), []byte(`[{"op":"test","path":"/name","value":"before"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	run("page-two\n", "devops", "invoke", "--org", org, "--area", "future", "--resource", "things", "--api-version", "9.0", "--route-parameters", "id=7", "--query-parameters", "filter=a&b", "--http-method", "PATCH", "--in-file", "body.json", "--media-type", "application/json-patch+json", "--query", "continuation_token", "-o", "tsv")
	run("sample\t7\n", "rest", "-u", org+"/_apis/future/things/7?api-version=7.1", "--uri-parameters", "filter=a&b", "--query", "[[name,id]]", "-o", "tsv")
	for _, args := range [][]string{
		{"rest", "-u", "https://example.invalid/_apis/projects"},
		{"rest", "-u", org + "/_apis/projects", "-m", "PATCH", "--query", "[", "--body", "{}"},
		{"rest", "-u", org + "/_apis/projects", "--headers", "Authorization=private-marker"},
		{"devops", "invoke", "--org", org, "--http-method", "DELETE"},
		{"devops", "invoke", "--org", org, "--area", "future", "--resource", "absent-private-marker"},
	} {
		out.Reset()
		err := a.run(t.Context(), args)
		if ExitCode(err) != 2 || strings.Contains(err.Error(), "private-marker") || out.Len() != 0 {
			t.Fatalf("unexpected failure %v, %q", err, out.String())
		}
	}
	if catalogs.Load() != 3 || patches.Load() != 1 {
		t.Errorf("got %d catalog and %d patch requests", catalogs.Load(), patches.Load())
	}
}

func TestAPIOutputProjection(t *testing.T) {
	for _, tc := range []struct{ query, format, input, want string }{
		{"[].{name:name,id:id}", "tsv", `[{"id":7,"name":"seven"}]`, "seven\t7\n"},
		{"[?id == `7`].{name:name,id:id}", "table", `[{"id":7,"name":"seven"}]`, "name  id\nseven  7\n"},
		{"", "tsv", `[{"a":false,"b":[9,8],"c":{"d":1}},{"x":9007199254740993}]`, "False\t2\t\n9007199254740993\n"},
		{"", "none", `{"a":true}`, ""},
	} {
		render, err := apiRenderer(tc.query, tc.format)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := render(&out, []byte(tc.input)); err != nil {
			t.Fatal(err)
		}
		if tc.format == "table" {
			// Tabwriter aligns the width of each column.
			if strings.Join(strings.Fields(out.String()), " ") != "name id seven 7" {
				t.Errorf("bad table %q", out.String())
			}
		} else if out.String() != tc.want {
			t.Errorf("got %q, want %q", out.String(), tc.want)
		}
	}
}

func TestGenericResponseAfterWrite(t *testing.T) {
	var sent atomic.Int32
	client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte{0xff})
	})
	for _, discard := range []bool{false, true} {
		r := genericRequest{method: http.MethodPost, discard: discard}
		err := r.run(t.Context(), client, []string{"_apis", "things"}, nil, io.Discard, true)
		if discard && err != nil || !discard && (err == nil || !strings.Contains(err.Error(), "returned HTTP 201 for POST")) {
			t.Fatal(err)
		}
	}
	if sent.Load() != 2 {
		t.Fatalf("expected two explicit requests, got %d", sent.Load())
	}
}

func TestStockResourcesOnCatalogRoute(t *testing.T) {
	const org = "https://dev.azure.com/fixture"
	var catalogs atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/C/_apis":
			catalogs.Add(1)
			io.WriteString(w, `{"value":[
				{"area":"git","resourceName":"items","routeTemplate":"_apis/git/{resource}","minVersion":"1.0","maxVersion":"7.2","releasedVersion":"7.1","resourceVersion":1},
				{"area":"wit","resourceName":"workitemsbatch","routeTemplate":"_apis/wit/{resource}","minVersion":"1.0","maxVersion":"7.2","releasedVersion":"7.1","resourceVersion":1},
				{"area":"wit","resourceName":"comments","routeTemplate":"_apis/wit/{resource}","minVersion":"1.0","maxVersion":"7.2","releasedVersion":"7.1","resourceVersion":1}]}`)
		case "/C/_apis/git/items":
			if r.URL.Query().Get("path") != "/test %2F.md" {
				t.Error("incorrect path decoding")
			}
			io.WriteString(w, `{"content":"fixture"}`)
		case "/C/_apis/wit/workitemsbatch":
			io.WriteString(w, `{"value":[{"id":7,"url":"https://example.invalid/server/path/_apis/wit/workItems/7"}]}`)
		case "/C/_apis/wit/comments":
			w.Header().Set("X-MS-ContinuationToken", "opaque")
			io.WriteString(w, `{"comments":[]}`)
		default:
			t.Error("unexpected resource")
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := clientTestProfile(t, srv, "/C")
	p.Organization = org
	setConfig(t, p)
	var out bytes.Buffer
	a := app{cwd: t.TempDir(), stdout: &out, store: &memoryStore{values: map[string]string{p.ID: clientTestPassword}}}
	for _, tc := range []struct {
		area, resource, query, want string
		extra                       []string
	}{
		{"git", "items", "content", "fixture", []string{"--query-parameters", "path=%2Ftest%20%252F.md"}},
		{"wit", "workitemsbatch", "value[0].url", org + "/_apis/wit/workItems/7", []string{"--http-method", "POST"}},
		{"wit", "comments", "continuationToken", "opaque", nil},
	} {
		out.Reset()
		args := []string{"devops", "invoke", "--org", org, "--area", tc.area, "--resource", tc.resource, "--api-version", "7.2", "--query", tc.query, "-o", "tsv"}
		if err := a.run(t.Context(), append(args, tc.extra...)); err != nil || out.String() != tc.want+"\n" {
			t.Fatalf("got %q, error %v", out.String(), err)
		}
	}
	if catalogs.Load() != 3 {
		t.Fatalf("expected catalog-driven reads, got %d", catalogs.Load())
	}
}
