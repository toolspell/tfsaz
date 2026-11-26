package az

import (
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
	witFixtureOrganization = "https://dev.azure.com/fixture"
	witFixtureCollection   = "/tfs/Fixture Collection"
	witFixtureProject      = "Delivery Project"
	witFixtureCursor       = "opaque/+= token"
)

func witArgs(resource, version string, route, extra []string) []string {
	args := []string{
		"devops", "invoke", "--detect", "false", "--organization", witFixtureOrganization,
		"--area", "wit", "--resource", resource, "--api-version", version,
		"--route-parameters", "project=" + witFixtureProject,
	}
	args = append(args, route...)
	args = append(args, extra...)
	return append(args, "--only-show-errors", "--output", "json")
}

func TestWorkItemReadsWithCollectionFixture(t *testing.T) {
	const (
		item         = `{"id":42,"fields":{"System.TeamProject":"Delivery Project","System.Title":"Fixture"},"url":"https://example.invalid/tfs/Fixture%20Collection/_apis/wit/workItems/42","relations":[{"rel":"ArtifactLink","url":"vstfs:///Git/PullRequestId/example"}]}`
		rewritten    = `{"id":42,"fields":{"System.TeamProject":"Delivery Project","System.Title":"Fixture"},"url":"https://dev.azure.com/fixture/_apis/wit/workItems/42","relations":[{"rel":"ArtifactLink","url":"vstfs:///Git/PullRequestId/example"}]}`
		wiqlBody     = `{"query":"SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = 'Delivery Project' AND [System.Title] CONTAINS 'Nora''s test' ORDER BY [System.Id] DESC"}`
		batchBody    = `{"ids":[42],"fields":["System.Id","System.Title","System.TeamProject","Microsoft.VSTS.Common.ClosedDate"]}`
		types        = `{"count":2,"value":[{"name":"Bug","states":[{"name":"Done","category":"Completed","color":"123456"}],"layout":"fixture"},{"name":"Task","isDisabled":true}]}`
		commentQuery = "continuationToken=opaque%2F%2B%3D+token&order=desc"
	)

	for _, tc := range []struct {
		name                  string
		args                  []string
		method, path, query   string
		input, response, want string
	}{
		{
			name: "show uses collection scope",
			args: []string{"boards", "work-item", "show", "--detect", "false", "--organization", witFixtureOrganization,
				"--id", "42", "--expand", "relations", "--only-show-errors", "--output", "json"},
			method: "GET", path: "/_apis/wit/workItems/42", query: "%24expand=relations&api-version=7.1",
			response: item, want: rewritten,
		},
		{
			name:   "workflow projection",
			args:   witArgs("workitemtypes", "7.1", nil, []string{"--query", workItemTypesProjection}),
			method: "GET", path: "/Delivery Project/_apis/wit/workitemtypes", query: "api-version=7.1",
			response: types,
			want:     `{"value":[{"name":"Bug","isDisabled":false,"states":[{"name":"Done","category":"Completed"}]},{"name":"Task","isDisabled":true,"states":[]}]}`,
		},
		{
			name: "WIQL reads",
			args: witArgs("wiql", "7.1", nil, []string{"--query-parameters", "timePrecision=true", "$top=101",
				"--http-method", "POST", "--in-file", "body.json", "--media-type", "application/json"}),
			method: "POST", path: "/Delivery Project/_apis/wit/wiql", query: "%24top=101&api-version=7.1&timePrecision=true",
			input: wiqlBody, response: `{"workItems":[{"id":42}]}`, want: `{"workItems":[{"id":42}],"continuation_token":null}`,
		},
		{
			name: "work item batch",
			args: witArgs("workitemsbatch", "7.1", nil,
				[]string{"--http-method", "POST", "--in-file", "body.json", "--media-type", "application/json"}),
			method: "POST", path: "/Delivery Project/_apis/wit/workitemsbatch", query: "api-version=7.1",
			input: batchBody, response: `{"count":3,"value":[null,{"id":0},` + item + `]}`,
			want: `{"count":3,"value":[null,{"id":0},` + rewritten + `],"continuation_token":null}`,
		},
		{
			name: "comments preview and cursor",
			args: witArgs("comments", "7.1-preview", []string{"workItemId=42"},
				[]string{"--query-parameters", "$top=100", "order=desc", "$expand=reactions", "api-version=7.1-preview.4",
					"continuationToken=" + witFixtureCursor}),
			method: "GET", path: "/Delivery Project/_apis/wit/workItems/42/comments",
			query:    "%24expand=reactions&%24top=100&api-version=7.1-preview.4&" + commentQuery,
			response: `{"comments":[{"id":4}],"continuationToken":"body-token"}`,
			want:     `{"comments":[{"id":4}],"continuationToken":"body-token","continuation_token":"header-token"}`,
		},
		{
			name: "comments header cursor fallback",
			args: witArgs("comments", "7.1-preview", []string{"workItemId=42"},
				[]string{"--query-parameters", "api-version=7.1-preview.4"}),
			method: "GET", path: "/Delivery Project/_apis/wit/workItems/42/comments", query: "api-version=7.1-preview.4",
			response: `{"comments":[],"continuationToken":null}`,
			want:     `{"comments":[],"continuationToken":"header-token","continuation_token":"header-token"}`,
		},
		{
			name: "updates page",
			args: witArgs("updates", "7.1", []string{"id=42"},
				[]string{"--query-parameters", "$top=100", "$skip=400"}),
			method: "GET", path: "/Delivery Project/_apis/wit/workItems/42/updates", query: "%24skip=400&%24top=100&api-version=7.1",
			response: `{"count":1,"value":[{"id":401}]}`, want: `{"count":1,"value":[{"id":401}],"continuation_token":null}`,
		},
		{
			name: "tag candidates",
			args: witArgs("tags", "7.1", nil, nil), method: "GET", path: "/Delivery Project/_apis/wit/tags", query: "api-version=7.1",
			response: `{"count":1,"value":[{"name":"fixture"}]}`, want: `{"count":1,"value":[{"name":"fixture"}],"continuation_token":null}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkClientTestAuth(t, r)
				if r.Method != tc.method || r.URL.Path != witFixtureCollection+tc.path || r.URL.RawQuery != tc.query {
					t.Errorf("request = %s %s, want %s %s?%s", r.Method, r.URL, tc.method, tc.path, tc.query)
				}
				if r.Header.Get("Accept") != "application/json" {
					t.Error("expected JSON response media type")
				}
				if r.Method == "POST" {
					if r.Header.Get("Content-Type") != "application/json" {
						t.Error("expected JSON request media type")
					}
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					checkWorkItemJSON(t, tc.input, data)
				}
				if strings.HasSuffix(tc.path, "/comments") {
					w.Header().Set("X-MS-ContinuationToken", "header-token")
				}
				w.Header().Set("Content-Type", "application/json")
				serveTestBody(w, tc.response)
			}))
			defer server.Close()
			profile := clientTestProfile(t, server, witFixtureCollection)
			profile.Organization = witFixtureOrganization
			setConfig(t, profile)
			cwd := t.TempDir()
			if tc.input != "" {
				if err := os.WriteFile(filepath.Join(cwd, "body.json"), []byte(tc.input), 0600); err != nil {
					t.Fatal(err)
				}
			}
			store := &memoryStore{values: map[string]string{profile.ID: clientTestPassword}}
			var stdout bytes.Buffer
			a := app{cwd: cwd, stdout: &stdout, store: store}
			if err := a.run(t.Context(), tc.args); err != nil {
				t.Fatal(err)
			}
			checkWorkItemJSON(t, tc.want, stdout.Bytes())
		})
	}
}

func checkWorkItemJSON(t *testing.T, want string, got []byte) {
	t.Helper()
	normalize := func(raw []byte) []byte {
		t.Helper()
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Error(err)
			return nil
		}
		data, _ := json.Marshal(decoded)
		return data
	}
	if a, b := normalize([]byte(want)), normalize(got); !bytes.Equal(a, b) {
		t.Errorf("got %s, want %s", b, a)
	}
}

func TestStockWorkItemInvokeShapes(t *testing.T) {
	cwd := t.TempDir()
	setConfig(t, Profile{ID: "fixture", Organization: witFixtureOrganization, CollectionURL: "https://example.invalid/Collection"})
	store := &memoryStore{}
	a := app{cwd: cwd, stdout: io.Discard, store: store}
	post := []string{"--http-method=POST", "--media-type=application/json", "--in-file=body.json"}
	for _, tc := range []struct {
		name, resource, version string
		route, extra            []string
		body                    string
		diagnostic              string
	}{
		{"write method", "comments", "7.1-preview", []string{"workItemId=42"}, []string{"--http-method=POST"}, "", "JSON GET only"},
		{"write route", "workitems", "7.1", nil, post, "", "unsupported devops invoke work item resource"},
		{"unrecognized route", "tags", "7.1", []string{"repositoryId=a"}, nil, "", "unexpected work item route parameter"},
		{"repeated project", "tags", "7.1", []string{"project=Other"}, nil, "", "duplicate devops invoke route parameter"},
		{"unrecognized query", "updates", "7.1", []string{"id=42"}, []string{"--query-parameters", "order=desc"}, "", "unsupported work item query parameter"},
		{"preview override", "comments", "7.1-preview", []string{"workItemId=42"}, []string{"--query-parameters", "api-version=7.1-preview.1"}, "", "unsupported work item comments preview version"},
		{"file cursor", "comments", "7.1-preview", []string{"workItemId=42"}, []string{"--query-parameters", "continuationToken=@body.json"}, "", "invalid work item comments continuation token"},
		{"arbitrary filter", "workitemtypes", "7.1", nil, []string{"--query=value"}, "", "unsupported work item --query"},
		{"wrong POST body media type", "wiql", "7.1", nil, []string{"--http-method=POST", "--media-type=text/plain", "--in-file=body.json"}, "", "requires POST, JSON"},
		{"unsupported WIQL top", "wiql", "7.1", nil, append(append([]string{}, post...), "--query-parameters", "$top=102"), `{"query":"SELECT [System.Id] FROM WorkItems"}`, "must be an integer"},
		{"non-string WIQL", "wiql", "7.1", nil, post, `{"query":["SELECT [System.Id] FROM WorkItems"]}`, "WIQL query string"},
		{"non-read WIQL", "wiql", "7.1", nil, post, `{"query":"UPDATE WorkItems"}`, "must start with SELECT"},
		{"unknown body field", "wiql", "7.1", nil, post, `{"query":"SELECT [System.Id] FROM WorkItems","other":"fixture"}`, "unexpected JSON field"},
		{"repeated body field", "wiql", "7.1", nil, post, `{"query":"SELECT [System.Id] FROM WorkItems","query":"SELECT [System.Id] FROM WorkItems"}`, "repeated JSON field"},
		{"oversized input", "wiql", "7.1", nil, post, strings.Repeat(" ", maxWitInputBytes+1), "small regular JSON file"},
		{"oversized batch ID", "workitemsbatch", "7.1", nil, post, `{"ids":[2147483648],"fields":["System.Id"]}`, "arrays of 1 to 200"},
		{"null batch ID", "workitemsbatch", "7.1", nil, post, `{"ids":[null],"fields":["System.Id"]}`, "IDs must be positive integers"},
		{"invalid field", "workitemsbatch", "7.1", nil, post, `{"ids":[42],"fields":["System.Id/"]}`, "invalid field name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.body != "" {
				if err := os.WriteFile(filepath.Join(cwd, "body.json"), []byte(tc.body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, opts, err := parse(witArgs(tc.resource, tc.version, tc.route, tc.extra))
			if err == nil {
				_, err = parseStockInvoke(opts, cwd)
			}
			if err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("error = %v, want usage error containing %q", err, tc.diagnostic)
			}
		})
	}
	args := []string{"boards", "work-item", "show", "--organization", witFixtureOrganization, "--id", "42", "--expand", "../../Fixture"}
	if err := a.run(t.Context(), args); ExitCode(err) != 2 || len(store.reads) != 0 {
		t.Fatalf("invalid --expand reached command dispatch: %v", err)
	}
	args = witArgs("tags", "7.1", nil, nil)
	args[slices.Index(args, "project="+witFixtureProject)] = "project=Group%2Fother"
	err := a.run(t.Context(), args)
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "invalid REST route parameter") || len(store.reads) != 0 {
		t.Fatalf("invalid project reached command dispatch: %v", err)
	}
}

func TestWorkItemShowChecksResponseID(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$expand") != "all" {
			t.Error("expected omitted --expand to default to all")
		}
		serveTestBody(w, `{"id":43,"fields":{}}`)
	}))
	defer server.Close()
	profile := clientTestProfile(t, server, witFixtureCollection)
	profile.Organization = witFixtureOrganization
	setConfig(t, profile)
	var stdout bytes.Buffer
	a := app{cwd: t.TempDir(), stdout: &stdout, store: &memoryStore{values: map[string]string{profile.ID: clientTestPassword}}}
	args := []string{"boards", "work-item", "show", "--organization", witFixtureOrganization, "--id", "42"}
	err := a.run(t.Context(), args)
	if err == nil || !strings.Contains(err.Error(), "does not identify work item 42") || stdout.Len() != 0 {
		t.Fatalf("unexpected mismatched work item result: %v, %s", err, &stdout)
	}
}

func TestBoardsReadUnauthorized(t *testing.T) {
	for _, tc := range []struct {
		name   string
		valid  bool
		reason string
	}{
		{"missing permission", true, "authenticated the PAT but denied this operation"},
		{"invalid PAT", false, "rejected the PAT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var probes atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkClientTestAuth(t, r)
				switch r.URL.Path {
				case witFixtureCollection + "/_apis/connectionData":
					probes.Add(1)
					if tc.valid {
						serveTestf(w, `{"authenticatedUser":{"id":%q}}`, repoTestViewerID)
						return
					}
				case witFixtureCollection + "/_apis/wit/workItems/42":
				default:
					t.Errorf("unexpected request path %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			profile := clientTestProfile(t, server, witFixtureCollection)
			profile.Organization = witFixtureOrganization
			setConfig(t, profile)
			var stdout bytes.Buffer
			a := app{cwd: t.TempDir(), stdout: &stdout, store: &memoryStore{values: map[string]string{profile.ID: clientTestPassword}}}
			err := a.run(t.Context(), []string{"boards", "work-item", "show", "--org", witFixtureOrganization, "--id", "42"})
			if ExitCode(err) != 1 || !strings.Contains(err.Error(), tc.reason) ||
				strings.Contains(err.Error(), loginHint) == tc.valid || probes.Load() != 1 || stdout.Len() != 0 {
				t.Fatalf("error %v, identity probes %d, output %q", err, probes.Load(), stdout.String())
			}
		})
	}
}
