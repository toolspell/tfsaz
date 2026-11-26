package az

import (
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestTeamReadRejectsInvalidScopeBeforeRequest(t *testing.T) {
	for _, tc := range []struct {
		command string
		opts    map[string]string
	}{
		{"devops team list", map[string]string{"detect": "false"}},
		{"devops team list", map[string]string{"project": "a%252fb"}},
		{"devops team list-member", map[string]string{}},
		{"devops team list-member", map[string]string{"team": ".."}},
		{"devops team list-member", map[string]string{"team": "Team", "top": "-1"}},
	} {
		if _, err := parseTeamRead(tc.command, tc.opts); err == nil {
			t.Errorf("parseTeamRead(%s, %v) accepted invalid input", tc.command, tc.opts)
		}
	}
}

func TestTeamRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		opts    map[string]string
		scope   Scope
		path    string
		query   url.Values
		body    string
		want    string
		usage   bool
	}{
		{
			name: "project from the checkout", command: "devops team list",
			opts: map[string]string{"top": "2", "skip": "1"}, scope: Scope{Project: "Checkout Project"},
			path:  "/_apis/projects/Checkout%20Project/teams",
			query: url.Values{"api-version": {"7.1"}, "$top": {"2"}, "$skip": {"1"}},
			body:  `{"count":1,"value":[{"name":"Team A"}]}`, want: `[{"name":"Team A"}]`,
		},
		{
			name: "explicit project over the checkout", command: "devops team list",
			opts: map[string]string{"project": "Shared Project"}, scope: Scope{Project: "Checkout Project"},
			path: "/_apis/projects/Shared%20Project/teams", query: url.Values{"api-version": {"7.1"}},
			body: `{"value":[]}`, want: `[]`,
		},
		{
			name: "members", command: "devops team list-member",
			opts: map[string]string{"project": "Shared Project", "team": "Team A"},
			path: "/_apis/projects/Shared%20Project/teams/Team%20A/members", query: url.Values{"api-version": {"7.1"}},
			body: `{"value":[{"identity":{"uniqueName":"fixture"}}]}`, want: `[{"identity":{"uniqueName":"fixture"}}]`,
		},
		{
			name: "no project outside a checkout", command: "devops team list", usage: true,
		},
		{
			name: "response without a value array", command: "devops team list", scope: Scope{Project: "P"},
			path: "/_apis/projects/P/teams", query: url.Values{"api-version": {"7.1"}}, body: `{"count":0}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				checkRepoRequest(t, r, tc.path, tc.query)
				serveTestBody(w, tc.body)
			})
			read, err := parseTeamRead(tc.command, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			result, err := read.run(t.Context(), client, tc.scope)
			if tc.usage {
				if ExitCode(err) != 2 || requests != 0 {
					t.Fatalf("got %v after %d requests, want a usage error before any request", err, requests)
				}
				return
			}
			if tc.want == "" {
				if err == nil || !strings.Contains(err.Error(), "missing the result array") {
					t.Fatalf("got %v, want a missing result array error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var gotValue, wantValue any
			if err := json.Unmarshal(got, &gotValue); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &wantValue); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotValue, wantValue) {
				t.Fatalf("result %s, want %s", got, tc.want)
			}
		})
	}
}
