package az

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
)

const (
	repoTestPrefix    = "/proxy/Fixture%20Collection"
	repoTestProjectID = "20202020-2222-4222-8222-202020202020"
	repoTestViewerID  = "30303030-3333-4333-8333-303030303030"
	repoTestAuthorID  = "40404040-4444-4444-8444-404040404040"
	repoTestPRPath    = "/_apis/git/pullrequests/42"
)

func repoFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func repoTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	collection, err := url.Parse(server.URL + repoTestPrefix)
	if err != nil {
		t.Fatal(err)
	}
	return &Client{collection: *collection, http: server.Client()}
}

func checkRepoRequest(t *testing.T, r *http.Request, path string, query url.Values) {
	t.Helper()
	if r.Method != http.MethodGet || r.URL.EscapedPath() != repoTestPrefix+path || !reflect.DeepEqual(r.URL.Query(), query) {
		t.Errorf("request = %s %s %v, want GET %s %v", r.Method, r.URL.EscapedPath(), r.URL.Query(), repoTestPrefix+path, query)
	}
}

func TestReposShow(t *testing.T) {
	fixture := strings.ReplaceAll(repoFixture(t, "repository"), "Fixture%20Collection", "Fixture Collection")
	client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		checkRepoRequest(t, r, "/Shared%20Project/_apis/git/repositories/Tool%20Kit", url.Values{"api-version": {"7.1"}})
		serveTestBody(w, fixture)
	})
	// The adapter can send explicit flags for a repository other than the checkout.
	scope := Scope{Project: "Checkout Project", Repository: "Checkout Repo"}
	result, err := runRepos(context.Background(), client, scope, "repos show", map[string]string{
		"project": "Shared Project", "repository": "Tool Kit",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]json.RawMessage)
	var want map[string]json.RawMessage
	if err := json.Unmarshal([]byte(repoFixture(t, "repository")), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Error("repository output differs from the encoded clone fixture")
	}
}

func TestReposPullRequestShowLinks(t *testing.T) {
	fixture := repoFixture(t, "pull-request")
	for _, tc := range []struct {
		name, organization, checkout, project, want string
	}{
		{
			name: "matching legacy checkout", organization: "https://fixture.visualstudio.com",
			checkout: "https://fixture.visualstudio.com/DefaultCollection/shared%20project/_git/tool%20kit",
			project:  "Shared Project",
			want:     "https://fixture.visualstudio.com/DefaultCollection/shared%20project/_git/tool%20kit",
		},
		{
			name: "shortened modern checkout", organization: "https://dev.azure.com/fixture",
			checkout: "https://dev.azure.com/fixture/_git/Tool%20Kit",
			project:  "Tool Kit",
			want:     "https://dev.azure.com/fixture/_git/Tool%20Kit",
		},
		{
			name: "shortened legacy checkout", organization: "https://fixture.visualstudio.com",
			checkout: "https://fixture.visualstudio.com/DefaultCollection/_git/Tool%20Kit",
			project:  "Tool Kit",
			want:     "https://fixture.visualstudio.com/DefaultCollection/_git/Tool%20Kit",
		},
		{
			name: "same repo name in another project", organization: "https://dev.azure.com/fixture",
			checkout: "https://dev.azure.com/fixture/Another%20Project/_git/Tool%20Kit",
			project:  "Shared Project",
			want:     "https://dev.azure.com/fixture/Shared%20Project/_git/Tool%20Kit",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				checkRepoRequest(t, r, repoTestPRPath, url.Values{"api-version": {"7.1"}})
				serveTestBody(w, strings.ReplaceAll(fixture, `"Shared Project"`, fmt.Sprintf("%q", tc.project)))
			})
			checkoutProject := tc.project
			if strings.Contains(tc.checkout, "Another%20Project") {
				checkoutProject = "Another Project"
			}
			result, err := runRepos(context.Background(), client, Scope{Organization: tc.organization, WebURL: tc.checkout, Project: checkoutProject, Repository: "Tool Kit"},
				"repos pr show", map[string]string{"id": "42"})
			if err != nil {
				t.Fatal(err)
			}
			row := result.(map[string]any)
			repository := row["repository"].(map[string]any)
			links := row["_links"].(map[string]any)
			if got := repository["webUrl"]; got != tc.want {
				t.Errorf("repository web URL = %v, want %s", got, tc.want)
			}
			if got := links["web"].(map[string]any)["href"]; got != tc.want+"/pullrequest/42" {
				t.Errorf("pull request web URL = %v, want %s/pullrequest/42", got, tc.want)
			}
			if row["title"] != "Document fixture setup" || row["sourceRefName"] != "refs/heads/user/feat/1-(#2)-slug" ||
				row["createdBy"].(map[string]any)["uniqueName"] != "author@example.invalid" || links["self"] == nil {
				t.Errorf("pull request payload changed: %v", row)
			}
		})
	}
}

func TestReposPolicyListPagedByPRProject(t *testing.T) {
	pr := repoFixture(t, "pull-request")
	var fixture struct{ Value []json.RawMessage }
	if err := json.Unmarshal([]byte(repoFixture(t, "policy-evaluations")), &fixture); err != nil {
		t.Fatal(err)
	}
	for index, status := range []string{"queued", "rejected"} {
		t.Run(status, func(t *testing.T) {
			client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.EscapedPath() == repoTestPrefix+repoTestPRPath {
					checkRepoRequest(t, r, repoTestPRPath, url.Values{"api-version": {"7.1"}})
					serveTestBody(w, pr)
					return
				}
				checkRepoRequest(t, r, "/"+repoTestProjectID+"/_apis/policy/evaluations", url.Values{
					"api-version": {"7.1-preview.1"}, "$top": {"1"}, "$skip": {fmt.Sprint(index)},
					"artifactId": {"vstfs:///CodeReview/CodeReviewId/" + repoTestProjectID + "/42"},
				})
				serveTestf(w, `{"count":1,"value":[%s]}`, fixture.Value[index])
			})
			scope := Scope{Organization: "https://dev.azure.com/fixture", Project: "Unrelated checkout"}
			result, err := runRepos(context.Background(), client, scope, "repos pr policy list", map[string]string{
				"id": "42", "top": "1", "skip": fmt.Sprint(index),
			})
			if err != nil {
				t.Fatal(err)
			}
			rows := result.([]json.RawMessage)
			if len(rows) != 1 || !reflect.DeepEqual(rows[0], fixture.Value[index]) {
				t.Errorf("policy page changed: %s", rows)
			}
		})
	}
}

func TestReposPullRequestListFilters(t *testing.T) {
	fixture := repoFixture(t, "pull-request")
	client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.EscapedPath() {
		case repoTestPrefix + "/_apis/connectionData":
			serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"FIXTURE\\reviewer"}}`, repoTestViewerID)
		case repoTestPrefix + "/_apis/identities":
			checkRepoRequest(t, r, "/_apis/identities", url.Values{
				"api-version": {"5.0"}, "searchFilter": {"General"}, "filterValue": {"author@example.invalid"},
			})
			serveTestf(w, `{"value":[{"id":%q}]}`, repoTestAuthorID)
		default:
			checkRepoRequest(t, r, "/Shared%20Project/_apis/git/repositories/Tool%20Kit/pullrequests", url.Values{
				"api-version": {"7.1"}, "$top": {"3"}, "$skip": {"20"},
				"searchCriteria.includeLinks": {"true"}, "searchCriteria.status": {"active"},
				"searchCriteria.creatorId": {repoTestAuthorID}, "searchCriteria.reviewerId": {repoTestViewerID},
				"searchCriteria.sourceRefName": {"refs/heads/user/feat/1-(#2)-slug"},
				"searchCriteria.targetRefName": {"refs/heads/main"},
			})
			serveTestf(w, `{"count":3,"value":[{"pullRequestId":"bad"},{"pullRequestId":41,"title":"Missing repository"},%s]}`, fixture)
		}
	})
	scope := Scope{
		Organization: "https://dev.azure.com/fixture", Project: "Shared Project", Repository: "Tool Kit",
		WebURL: "https://dev.azure.com/fixture/Shared%20Project/_git/Tool%20Kit",
	}
	result, err := runRepos(context.Background(), client, scope, "repos pr list", map[string]string{
		"source-branch": "user%2Ffeat%2F1-(%232)-slug", "target-branch": "refs/heads/main",
		"skip": "20", "top": "3", "status": "active", "creator": "author@example.invalid", "reviewer": `fixture\reviewer`, "include-links": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := result.([]json.RawMessage)
	if len(rows) != 3 || string(rows[0]) != "null" || string(rows[1]) != "null" {
		t.Fatalf("list failed to preserve raw offsets: %s", rows)
	}
	row, err := decodePullRequestRow(rows[2])
	if err != nil {
		t.Fatal(err)
	}
	if got := row["_links"].(map[string]any)["web"].(map[string]any)["href"]; got != scope.WebURL+"/pullrequest/42" {
		t.Errorf("list pull request web link = %v", got)
	}
}

func TestReposPullRequestListMeAndEmptyPage(t *testing.T) {
	client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == repoTestPrefix+"/_apis/connectionData" {
			serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"FIXTURE\\reviewer"}}`, repoTestViewerID)
			return
		}
		checkRepoRequest(t, r, "/Shared%20Project/_apis/git/pullrequests", url.Values{
			"api-version": {"7.1"}, "$top": {"1"}, "searchCriteria.status": {"all"},
			"searchCriteria.creatorId": {repoTestViewerID}, "searchCriteria.includeLinks": {"false"},
		})
		serveTestBody(w, `{"count":0,"value":[]}`)
	})
	result, err := runRepos(context.Background(), client, Scope{Project: "Shared Project"}, "repos pr list", map[string]string{
		"creator": "me", "status": "all", "top": "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if encoded, err := json.Marshal(result); err != nil || string(encoded) != "[]" {
		t.Fatalf("empty list JSON = %s, %v", encoded, err)
	}
}

func TestReposErrors(t *testing.T) {
	for _, tc := range []struct {
		command, body, want string
		status              int
		opts                map[string]string
	}{
		{"repos pr show", `{"message":"missing"}`, "pull request not found", 404, map[string]string{"id": "42"}},
		{"repos pr show", `{}`, "rejected the PAT", 401, map[string]string{"id": "42"}},
		{"repos pr list", `{}`, "HTTP 500", 500, nil},
		{"repos pr list", `{"value":null}`, "missing the result array", 200, nil},
		{"repos pr policy list", `{"pullRequestId":42,"repository":{"project":{"name":"Shared Project"}}}`, "missing its project GUID", 200, map[string]string{"id": "42"}},
	} {
		t.Run(tc.want, func(t *testing.T) {
			client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				serveTestBody(w, tc.body)
			})
			_, err := runRepos(context.Background(), client, Scope{Project: "Shared Project"}, tc.command, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
