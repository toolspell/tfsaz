package az

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchCatalogUsesCollectionAndPreservesEntries(t *testing.T) {
	const entry = `{"id":"00000000-0000-0000-0000-000000000001","area":"core","resourceName":"projects","routeTemplate":"_apis/{resource}/{projectId}","minVersion":"1.0","maxVersion":7.1,"releasedVersion":"7.1","resourceVersion":4,"locationUrl":"https://example.invalid/other-collection","extra":true}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkClientTestAuth(t, r)
		if r.Method != http.MethodOptions || r.URL.EscapedPath() != "/tfs/Local%20Collection/_apis" ||
			r.URL.RawQuery != "allHostTypes=true" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("unexpected OPTIONS request: %s %s", r.Method, r.URL)
		}
		serveTestBody(w, `{"count":1,"value":[`+entry+`]}`)
	}))
	defer server.Close()
	client := newClientForTest(t, clientTestProfile(t, server, "/tfs/Local Collection"))
	_, locations, err := fetchCatalog(t.Context(), client)
	if err != nil || len(locations) != 1 {
		t.Fatalf("fetchCatalog returned %d locations, error = %v", len(locations), err)
	}
	if raw := string(locations[0].raw); raw != entry {
		t.Errorf("raw catalog entry = %s, want %s", raw, entry)
	}
	path, version, err := resolveResource(locations, "CORE", "PROJECTS", "", nil)
	if err != nil || version != "5.0" || !slices.Equal(path, []string{"_apis", "projects"}) {
		t.Fatalf("resolveResource = %q, %q, %v", path, version, err)
	}
}

func TestCatalogCacheEntries(t *testing.T) {
	const served = `{"count":1,"value":[{"area":"served","resourceName":"projects","routeTemplate":"_apis/{resource}","minVersion":"1.0","maxVersion":"7.1","releasedVersion":"7.1","resourceVersion":1}]}`
	const cached = `{"count":1,"value":[{"area":"cached","resourceName":"projects","routeTemplate":"_apis/{resource}","minVersion":"1.0","maxVersion":"7.1","releasedVersion":"7.1","resourceVersion":1}]}`
	var fetches atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkClientTestAuth(t, r)
		fetches.Add(1)
		serveTestBody(w, served)
	}))
	defer server.Close()
	profile := clientTestProfile(t, server, "/tfs/Local Collection")
	entry := func(body string, age time.Duration) func(*testing.T, string, string) {
		return func(t *testing.T, dir, file string) {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			modified := time.Now().Add(-age)
			if err := os.Chtimes(file, modified, modified); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T, dir, file string)
		wantArea string
		// wantFile is the entry afterwards; empty when no entry can be written.
		wantFile string
	}{
		{"missing entry", func(*testing.T, string, string) {}, "served", served},
		{"fresh entry", entry(cached, catalogCacheMaxAge-time.Minute), "cached", cached},
		{"expired entry", entry(cached, catalogCacheMaxAge), "served", served},
		{"future entry", entry(cached, -time.Hour), "served", served},
		{"corrupt entry", entry(`{"value":[{"area":`, 0), "served", served},
		{"entry without locations", entry(`{"count":0}`, 0), "served", served},
		{"directory in place of the entry", func(t *testing.T, _, file string) {
			if err := os.MkdirAll(file, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "served", ""},
		{"file in place of the cache directory", func(t *testing.T, dir, _ string) {
			if err := os.WriteFile(dir, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "served", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetches.Store(0)
			client := newClientForTest(t, profile)
			client.catalogCache = catalogCacheDir(filepath.Join(t.TempDir(), "config.json"))
			file := client.catalogCacheFile()
			tc.setup(t, client.catalogCache, file)
			catalog, err := client.loadCatalog(t.Context())
			if err != nil || len(catalog.locations) != 1 || catalog.locations[0].Area != tc.wantArea {
				t.Fatalf("loadCatalog = %+v, %v; want area %s", catalog, err, tc.wantArea)
			}
			wantFetches := int32(1)
			if catalog.cached {
				wantFetches = 0
			}
			if catalog.cached != (tc.wantArea == "cached") || fetches.Load() != wantFetches {
				t.Errorf("cached = %v after %d fetches", catalog.cached, fetches.Load())
			}
			data, err := os.ReadFile(file)
			if tc.wantFile == "" && err == nil || tc.wantFile != "" && string(data) != tc.wantFile {
				t.Errorf("cache entry = %q, %v; want %q", data, err, tc.wantFile)
			}
			if names, err := os.ReadDir(client.catalogCache); err == nil && (len(names) != 1 || names[0].Name() != filepath.Base(file)) {
				t.Errorf("cache directory holds %v, want only the entry", names)
			}
			if tc.wantFile == "" || runtime.GOOS == "windows" {
				return
			}
			for path, want := range map[string]os.FileMode{client.catalogCache: 0o700, file: 0o600} {
				if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
					t.Errorf("mode of %s = %v, %v; want %v", filepath.Base(path), info.Mode().Perm(), err, want)
				}
			}
		})
	}
}

func catalogTestLocation(template string) resourceLocation {
	return resourceLocation{
		Area: "TestArea", ResourceName: "Resource", RouteTemplate: template,
		MinVersion: "2.0", MaxVersion: "7.1", ReleasedVersion: "7.0", ResourceVersion: 3,
	}
}

func TestResolveResourceRoutes(t *testing.T) {
	tests := []struct {
		name     string
		template string
		params   map[string]string
		want     []string
	}{
		{
			name:     "case insensitive with optional team and id",
			template: "/{project}/{team}/_apis/{AREA}/{ReSoUrCe}/{id}",
			params:   map[string]string{"PROJECT": "A Project"},
			want:     []string{"A Project", "_apis", "TestArea", "Resource"},
		},
		{
			name:     "literal alias route and nested IDs",
			template: "{project}/_apis/another-area/outer/{outerId}/inner/{innerId}",
			params:   map[string]string{"outerid": "one", "InnerId": "two"},
			want:     []string{"_apis", "another-area", "outer", "one", "inner", "two"},
		},
		{
			name:     "explicit optional slot",
			template: "_apis/{area}/{resource}/{type?}/children",
			want:     []string{"_apis", "TestArea", "Resource", "children"},
		},
		{
			name:     "whole star path",
			template: "_apis/{area}/{resource}/{*Path}",
			params:   map[string]string{"path": "/folder name/item#1.txt"},
			want:     []string{"_apis", "TestArea", "Resource", "folder name", "item#1.txt"},
		},
		{
			name:     "analytics",
			template: "_odata/{oDataVersion}/{entityType}",
			params:   map[string]string{"oDataVersion": "v3.0-preview", "entityType": "WorkItems"},
			want:     []string{"_odata", "v3.0-preview", "WorkItems"},
		},
		{
			name:     "package template with literal affixes",
			template: "{project}/_packaging/{feedId}/v3/{resource}-semver2/@{packageScope}/{version}.json",
			params:   map[string]string{"feedId": "Public", "packageScope": "fixture", "version": "1.0"},
			want:     []string{"_packaging", "Public", "v3", "Resource-semver2", "@fixture", "1.0.json"},
		},
		{
			name:     "dollar prefix in catalog",
			template: "_apis/{area}/${resource}/items/${type}",
			params:   map[string]string{"type": "Task"},
			want:     []string{"_apis", "TestArea", "$Resource", "items", "$Task"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, version, err := resolveResource([]resourceLocation{catalogTestLocation(test.template)}, "testarea", "resource", "", test.params)
			if err != nil || version != "5.0" || !slices.Equal(path, test.want) {
				t.Fatalf("resolveResource = %q, %q, %v, want %q with default version", path, version, err, test.want)
			}
		})
	}
}

func TestResolveResourceRejectsMismatchedAndUnsafeRoutes(t *testing.T) {
	const normal = "{project}/_apis/{area}/{resource}/{*path}"
	tests := []struct {
		name     string
		template string
		params   map[string]string
	}{
		{"unused", normal, map[string]string{"unknown": "value"}},
		{"duplicate", normal, map[string]string{"project": "one", "PROJECT": "two"}},
		{"area override", "_apis/area/literal", map[string]string{"AREa": "other"}},
		{"resource override", "_apis/area/literal", map[string]string{"resourcE": "other"}},
		{"traversal", normal, map[string]string{"project": ".."}},
		{"encoded traversal", normal, map[string]string{"project": "%252e%252e"}},
		{"normalized traversal", normal, map[string]string{"project": ".. . "}},
		{"matrix traversal", normal, map[string]string{"project": "..%3bsuffix"}},
		{"encoded separator", normal, map[string]string{"project": "a%2fb"}},
		{"partially encoded separator", normal, map[string]string{"project": "a%2fb%zz"}},
		{"fake API prefix", normal, map[string]string{"project": "%5fapis."}},
		{"star traversal", normal, map[string]string{"path": "a/../b"}},
		{"star empty segment", normal, map[string]string{"path": "a//b"}},
		{"star prefix", "{*path}/_apis/{area}/{resource}", map[string]string{"path": "/x/y"}},
		{"star resource override", "_apis/{*resource}", nil},
		{"star API selector", "_apis/{*path}", map[string]string{"path": "foo/bar"}},
		{"literal API selector order", "_apis/{area}/{resource}/_apis", nil},
		{"literal traversal", "project/../_apis/{area}/{resource}", nil},
		{"absolute URL", "https://example.invalid/_apis/{area}/{resource}", nil},
		{"network path", "//example.invalid/_apis/{area}/{resource}", nil},
		{"query in template", "_apis/{area}/{resource}?a=1", nil},
		{"missing fixed-position identifier", "_apis/{area}/outer/{id}/inner", nil},
		{"missing parent id", "_apis/{area}/{resource}/{parentId}/{id}", map[string]string{"id": "two"}},
		{"missing project for team", "{project}/{team}/_apis/{area}/{resource}", map[string]string{"team": "a"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if path, version, err := resolveResource([]resourceLocation{catalogTestLocation(test.template)}, "testarea", "resource", "", test.params); err == nil {
				t.Fatalf("resolveResource unexpectedly accepted %q, %q", path, version)
			}
		})
	}
}

func TestResolveResourceNegotiatesCatalogVersions(t *testing.T) {
	location := catalogTestLocation("_apis/{area}/{resource}")
	tests := []struct {
		requested string
		want      string
	}{
		{"", "5.0"},
		{"7", "7"},
		{"6.0", "6.0"},
		{"7.1-preview", "7.1-preview"},
		{"7.1-preview.1", "7.1-preview.1"},
		{"7.1-preview.12", "7.1-preview.3"},
		{"7.10", "7.1-preview"},
		{"8.0-preview.2", "7.1-preview"},
		{"1.0", ""},
		{"7.1-preview.1;bad", ""},
		{"7.1-preview.1x", ""},
	}
	for _, test := range tests {
		t.Run(test.requested, func(t *testing.T) {
			_, version, err := resolveResource([]resourceLocation{location}, "testarea", "resource", test.requested, nil)
			if version != test.want || (err != nil) != (test.want == "") {
				t.Fatalf("negotiated version = %q, error = %v, want %q", version, err, test.want)
			}
		})
	}

	location.ReleasedVersion = "7.1"
	_, version, err := resolveResource([]resourceLocation{location}, "testarea", "resource", "8.0-preview.2", nil)
	if err != nil || version != "7.1" {
		t.Fatalf("downgrade to released version = %q, %v", version, err)
	}
}

func TestResolveResourceSelectsMatchingLocation(t *testing.T) {
	old := catalogTestLocation("_apis/{area}/old/{resource}")
	old.MaxVersion, old.ReleasedVersion = "6.0", "6.0"
	newer := catalogTestLocation("_apis/{area}/new/{resource}/{id}")
	newer.MinVersion = "5.0"
	other := catalogTestLocation("_apis/{area}/different/{resource}/{id}")

	path, _, err := resolveResource([]resourceLocation{newer, old}, "testarea", "resource", "4.0", nil)
	if err != nil || path[2] != "old" {
		t.Fatalf("resolve older catalog version = %q, %v", path, err)
	}
	path, _, err = resolveResource([]resourceLocation{old, newer}, "testarea", "resource", "5.0", map[string]string{"id": "42"})
	if err != nil || path[2] != "new" {
		t.Fatalf("resolve compatible catalog version = %q, %v", path, err)
	}
	_, _, err = resolveResource([]resourceLocation{other, newer}, "testarea", "resource", "5.0", map[string]string{"id": "42"})
	if err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("resolve ambiguous catalog version = %v", err)
	}

	list := catalogTestLocation("_apis/{area}/{resource}")
	item := catalogTestLocation("_apis/{area}/{resource}/{id}")
	path, _, err = resolveResource([]resourceLocation{item, list}, "testarea", "resource", "5.0", nil)
	if err != nil || !slices.Equal(path, []string{"_apis", "TestArea", "Resource"}) {
		t.Fatalf("resolve catalog list route = %q, %v", path, err)
	}
}

// serveTestCatalog answers a catalog request with one Git repositories
// location whose maxVersion is level.
func serveTestCatalog(w io.Writer, level string) {
	serveTestf(w, `{"count":1,"value":[{"area":"git","resourceName":"repositories","routeTemplate":"{project}/_apis/{area}/{resource}/{repositoryId}","minVersion":"1.0","maxVersion":%q,"releasedVersion":"6.0","resourceVersion":1}]}`, level)
}

func TestCheckAPIFloor(t *testing.T) {
	location := func(area, resource, maxVersion string) string {
		return `{"area":"` + area + `","resourceName":"` + resource + `","routeTemplate":"_apis/{area}/{resource}","minVersion":"1.0","maxVersion":"` + maxVersion + `","releasedVersion":"5.0","resourceVersion":1}`
	}
	tests := []struct {
		name      string
		locations []string
		want      catalogVersion
		err       string
	}{
		{"floor", []string{location("git", "repositories", "7.1")}, catalogVersion{7, 1}, ""},
		{"preview only counts", []string{location("Git", "Repositories", "7.1")}, catalogVersion{7, 1}, ""},
		{"newer server", []string{location("git", "repositories", "7.2")}, catalogVersion{7, 2}, ""},
		{"highest location wins", []string{location("git", "repositories", "7.1"), location("git", "repositories", "6.0")}, catalogVersion{7, 1}, ""},
		{"other resources are ignored", []string{location("core", "projects", "7.1"), location("git", "repositories", "7.0")}, catalogVersion{7, 0}, "supports API 7.0 at most"},
		{"server 2020", []string{location("git", "repositories", "6.0")}, catalogVersion{6, 0}, "supports API 6.0 at most; TFSaz needs API 7.1"},
		{"no git repositories", []string{location("core", "projects", "7.1")}, catalogVersion{}, "lists no Git repositories resource"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				serveTestBody(w, `{"value":[`+strings.Join(tc.locations, ",")+`]}`)
			}))
			defer server.Close()
			level, err := checkAPIFloor(t.Context(), newClientForTest(t, clientTestProfile(t, server, "/Collection")))
			if level != tc.want || (err == nil) != (tc.err == "") || (err != nil && !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("checkAPIFloor = %v, %v; want %v, %q", level, err, tc.want, tc.err)
			}
		})
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if _, err := checkAPIFloor(t.Context(), newClientForTest(t, clientTestProfile(t, server, "/Collection"))); err == nil ||
		!strings.Contains(err.Error(), "could not read the collection resource catalog") {
		t.Fatalf("checkAPIFloor on a failing catalog = %v", err)
	}
}
