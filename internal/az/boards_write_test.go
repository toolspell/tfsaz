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
	"testing"
)

func TestBoardsWorkItemWrites(t *testing.T) {
	const markdown = "@mention\n\n## Fixture notes\n\n- first step\n- second step"
	const fields = `"fields":{"System.TeamProject":"Delivery Project","System.Title":"Fixture title","System.CreatedDate":"2026-07-02T00:00:00Z"}`
	for _, tc := range []struct {
		name, method, path string
		args               []string
		stdin              string
		detect             bool
		patch              []workItemPatchField
	}{
		{
			name: "create with explicit project and description on stdin", method: http.MethodPost,
			path: "/Delivery Project/_apis/wit/workItems/$Change Request",
			args: []string{"create", "--project", witFixtureProject, "--type", "Change Request",
				"--title=Fixture title", "--description=@-", "--fields", "System.Tags=frontend; area=writer", "System.AssignedTo=assignee@example.invalid"},
			stdin: markdown,
			patch: []workItemPatchField{
				{"add", "/fields/System.Title", "Fixture title"},
				{"add", "/fields/System.Description", markdown},
				{"add", "/fields/System.Tags", "frontend; area=writer"},
				{"add", "/fields/System.AssignedTo", "assignee@example.invalid"},
			},
		},
		{
			name: "create uses checkout project when omitted", method: http.MethodPost,
			path:   "/Checkout Project/_apis/wit/workItems/$Task",
			args:   []string{"create", "--type", "Task", "--title=Fixture title"},
			detect: true,
			patch: []workItemPatchField{
				{"add", "/fields/System.Title", "Fixture title"},
			},
		},
		{
			name: "update clears labels and assignee and preserves unrelated fields", method: http.MethodPatch,
			path:  "/_apis/wit/workItems/42",
			args:  []string{"update", "--id", "42", "--fields", "System.Tags=", "System.AssignedTo=", "Microsoft.VSTS.TCM.ReproSteps=@-"},
			stdin: markdown,
			patch: []workItemPatchField{
				{"remove", "/fields/System.Tags", nil},
				{"add", "/fields/System.AssignedTo", ""},
				{"add", "/fields/Microsoft.VSTS.TCM.ReproSteps", markdown},
			},
		},
		{
			name: "update sets a custom state", method: http.MethodPatch,
			path: "/_apis/wit/workItems/42",
			args: []string{"update", "--id=42", "--state", "Ready to Ship"},
			patch: []workItemPatchField{
				{"add", "/fields/System.State", "Ready to Ship"},
			},
		},
		{
			name: "update clears the description", method: http.MethodPatch,
			path: "/_apis/wit/workItems/42",
			args: []string{"update", "--id=42", "--description="},
			patch: []workItemPatchField{
				{"add", "/fields/System.Description", ""},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkClientTestAuth(t, r)
				if r.Method != tc.method || r.URL.Path != witFixtureCollection+tc.path || r.URL.RawQuery != "api-version=7.1" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Content-Type") != "application/json-patch+json" || r.Header.Get("Accept") != "application/json" {
					t.Errorf("unexpected request headers %s, %s", r.Header.Get("Content-Type"), r.Header.Get("Accept"))
				}
				var got []workItemPatchField
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !slices.Equal(got, tc.patch) {
					t.Errorf("patch = %#v, error = %v; want %#v", got, err, tc.patch)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":42,`+fields+`,"url":"https://example.invalid/_apis/wit/workItems/42"}`)
			}))
			defer server.Close()
			profile := clientTestProfile(t, server, witFixtureCollection)
			profile.Organization = witFixtureOrganization
			setConfig(t, profile)
			cwd := t.TempDir()
			if tc.detect {
				gitAt(t, cwd, "init", "--quiet")
				gitAt(t, cwd, "remote", "add", "origin", witFixtureOrganization+"/Checkout%20Project/_git/Fixture")
			}
			var input *os.File
			if tc.stdin != "" {
				inputPath := filepath.Join(t.TempDir(), "stdin")
				if err := os.WriteFile(inputPath, []byte(tc.stdin), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				input, err = os.Open(inputPath)
				if err != nil {
					t.Fatal(err)
				}
				defer input.Close()
			}
			var stdout bytes.Buffer
			a := app{cwd: cwd, stdout: &stdout, stdin: input, store: &memoryStore{values: map[string]string{profile.ID: clientTestPassword}}}
			args := append([]string{"boards", "work-item"}, tc.args...)
			if !tc.detect {
				args = append(args, "--organization", witFixtureOrganization, "--detect", "false")
			}
			args = append(args, "--only-show-errors", "--output", "json")
			if err := a.run(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			checkWorkItemJSON(t, `{"id":42,`+fields+`,"url":"`+witFixtureOrganization+`/_apis/wit/workItems/42"}`, stdout.Bytes())
		})
	}
}

func TestBoardsWriteRejectsInvalidInputBeforeRequest(t *testing.T) {
	for _, tc := range []struct {
		command string
		opts    map[string]string
		want    string
	}{
		{"boards work-item create", map[string]string{"type": "Task", "title": "Fixture"}, "valid project"},
		{"boards work-item create", map[string]string{"project": "Delivery Project", "type": "group%2ftask", "title": "Fixture"}, "valid --type"},
		{"boards work-item create", map[string]string{"project": "Delivery Project", "type": "Task"}, "requires --title"},
		{"boards work-item update", map[string]string{"id": "0", "title": "Fixture"}, "positive work item number"},
		{"boards work-item update", map[string]string{"id": "42", "fields": "System.Tags=test\x00System.State/invalid=ignored"}, "valid field names"},
	} {
		_, err := (app{}).boardsWrite(t.Context(), nil, Scope{}, tc.command, tc.opts)
		if ExitCode(err) != 2 || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s with %v returned %v, want usage error containing %q", tc.command, tc.opts, err, tc.want)
		}
	}
}
