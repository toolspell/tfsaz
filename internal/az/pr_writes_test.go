package az

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	testPRWriteRepoID = "10101010-1111-4111-8111-101010101010"
	testPRWritePath   = "/" + repoTestProjectID + "/_apis/git/repositories/" + testPRWriteRepoID + "/pullrequests/42"
	testPRWriteWebURL = "https://fixture.visualstudio.com/DefaultCollection/shared%20project/_git/tool%20kit"
)

type prWriteRequest struct {
	method, path, body, response string
}

func prWriteTestClient(t *testing.T, expected ...prWriteRequest) *Client {
	t.Helper()
	var count atomic.Int32
	t.Cleanup(func() {
		if got := int(count.Load()); got != len(expected) {
			t.Errorf("received %d requests, expected %d", got, len(expected))
		}
	})
	return repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		i := int(count.Add(1)) - 1
		if i >= len(expected) {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		want := expected[i]
		path, query, _ := strings.Cut(want.path, "?")
		if r.Method != want.method || r.URL.EscapedPath() != repoTestPrefix+path {
			t.Errorf("request %d = %s %s, expected %s %s", i+1, r.Method, r.URL.EscapedPath(), want.method, repoTestPrefix+path)
		}
		if query == "" {
			query = "api-version=7.1"
		}
		if r.URL.Query().Encode() != query {
			t.Errorf("request %d query = %s, want %s", i+1, r.URL.Query().Encode(), query)
		}
		gotBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("request %d: %v", i+1, err)
		}
		if want.body != "" {
			var got, expected any
			if err := json.Unmarshal(gotBody, &got); err != nil {
				t.Errorf("request %d invalid JSON: %v", i+1, err)
			}
			if err := json.Unmarshal([]byte(want.body), &expected); err != nil {
				t.Errorf("request %d invalid test expectation: %v", i+1, err)
			}
			if !reflect.DeepEqual(got, expected) {
				t.Errorf("request %d body = %s, want %s", i+1, gotBody, want.body)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("request %d content type = %q", i+1, got)
			}
		} else if len(gotBody) != 0 {
			t.Errorf("request %d has unexpected body %s", i+1, gotBody)
		}
		if want.response == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		serveTestBody(w, want.response)
	})
}

func TestPRWriteCreate(t *testing.T) {
	fixture := repoFixture(t, "pull-request")
	client := prWriteTestClient(t, prWriteRequest{
		method: http.MethodPost, path: "/Shared%20Project/_apis/git/repositories/Tool%20Kit/pullrequests",
		body:     `{"title":"@notes.txt","description":"@literal\n--text","sourceRefName":"refs/heads/topic%23","targetRefName":"refs/heads/@main","isDraft":true}`,
		response: fixture,
	})
	scope := Scope{Organization: "https://fixture.visualstudio.com", Project: "Shared Project", Repository: "Tool Kit", WebURL: testPRWriteWebURL}
	result, err := runPRWrite(context.Background(), client, scope, "repos pr create", map[string]string{
		"project": "Shared Project", "repository": "Tool Kit", "detect": "false",
		"title": "@notes.txt", "description": "@literal\n--text", "source-branch": "topic%23", "target-branch": "refs/heads/@main", "draft": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	row := result.(map[string]any)
	links := row["_links"].(map[string]any)
	if got := links["web"].(map[string]any)["href"]; got != testPRWriteWebURL+"/pullrequest/42" {
		t.Errorf("PR web link = %v", got)
	}
}

func TestPRWriteUpdate(t *testing.T) {
	fixture := repoFixture(t, "pull-request")
	existing := strings.Replace(fixture, `"status": "active"`, `"status": "active", "completionOptions":{"deleteSourceBranch":true,"mergeStrategy":"rebase"}`, 1)
	for _, tc := range []struct {
		name, patch string
		extra       []prWriteRequest
		options     map[string]string
	}{
		{
			name: "merge preserves the source commit and prior completion options",
			patch: `{"status":"completed","completionOptions":{"deleteSourceBranch":true,"mergeStrategy":"squash","squashMerge":true},
				"lastMergeSourceCommit":{"commitId":"000000000000000000000000000000000000abcd"}}`,
			options: map[string]string{"status": "completed", "squash": "true"},
		},
		{
			name:    "enable auto-completion with the collection's authenticated identity",
			patch:   fmt.Sprintf(`{"autoCompleteSetBy":{"id":%q},"completionOptions":{"deleteSourceBranch":true,"mergeStrategy":"noFastForward","squashMerge":false}}`, repoTestViewerID),
			extra:   []prWriteRequest{{method: http.MethodGet, path: "/_apis/connectionData?api-version=5.0-preview.1", response: fmt.Sprintf(`{"authenticatedUser":{"id":%q}}`, repoTestViewerID)}},
			options: map[string]string{"auto-complete": "true", "squash": "false"},
		},
		{
			name:    "disable auto-completion and clear draft and description",
			patch:   `{"autoCompleteSetBy":{"id":"00000000-0000-0000-0000-000000000000"},"isDraft":false,"description":""}`,
			options: map[string]string{"auto-complete": "false", "draft": "false", "description": ""},
		},
		{name: "ID-only update sends the empty patch used by the extension", patch: "{}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := []prWriteRequest{{method: http.MethodGet, path: repoTestPRPath, response: existing}}
			requests = append(requests, tc.extra...)
			if tc.patch != "" {
				requests = append(requests, prWriteRequest{method: http.MethodPatch, path: testPRWritePath, body: tc.patch, response: fixture})
			}
			client := prWriteTestClient(t, requests...)
			scope := Scope{Organization: "https://dev.azure.com/fixture"}
			opts := map[string]string{"id": "42"}
			for k, v := range tc.options {
				opts[k] = v
			}
			result, err := runPRWrite(context.Background(), client, scope, "repos pr update", opts)
			if err != nil {
				t.Fatal(err)
			}
			row := result.(map[string]any)
			if pullRequestNumber(row) != "42" || row["status"] != "active" {
				t.Errorf("expected the server's active pull request 42, got %v", row)
			}
		})
	}
}

func TestPRWriteReviewers(t *testing.T) {
	fixture := repoFixture(t, "pull-request")
	first := prWriteRequest{method: http.MethodGet, path: repoTestPRPath, response: fixture}
	identity := prWriteRequest{
		method: http.MethodGet, path: "/_apis/connectionData?api-version=5.0-preview.1",
		response: fmt.Sprintf(`{"authenticatedUser":{"id":%q,"uniqueName":"FIXTURE\\viewer"}}`, repoTestViewerID),
	}
	const reviewers = testPRWritePath + "/reviewers"
	t.Run("add GUID and email in one request", func(t *testing.T) {
		lookup := prWriteRequest{
			method: http.MethodGet, path: "/_apis/identities?api-version=5.0&filterValue=author%40example.invalid&searchFilter=General",
			response: fmt.Sprintf(`{"value":[{"id":%q}]}`, repoTestAuthorID),
		}
		client := prWriteTestClient(t, first, identity, lookup, prWriteRequest{
			method: http.MethodPost, path: reviewers,
			body:     fmt.Sprintf(`[{"id":%q},{"id":%q}]`, repoTestViewerID, repoTestAuthorID),
			response: fmt.Sprintf(`{"count":1,"value":[{"id":%q}]}`, repoTestAuthorID),
		})
		result, err := runPRWrite(context.Background(), client, Scope{}, "repos pr reviewer add", map[string]string{
			"id": "42", "reviewers": repoTestViewerID + "\x00author@example.invalid",
		})
		if err != nil {
			t.Fatal(err)
		}
		if rows := result.([]json.RawMessage); len(rows) != 1 {
			t.Errorf("reviewers = %s", rows)
		}
	})
	t.Run("remove then read remaining reviewers", func(t *testing.T) {
		client := prWriteTestClient(t, first,
			prWriteRequest{method: http.MethodDelete, path: reviewers + "/" + repoTestAuthorID},
			prWriteRequest{method: http.MethodGet, path: reviewers, response: `{"count":0,"value":[]}`},
		)
		result, err := runPRWrite(context.Background(), client, Scope{}, "repos pr reviewer remove", map[string]string{
			"id": "42", "reviewers": repoTestAuthorID + "\x00" + strings.ToUpper(repoTestAuthorID),
		})
		if err != nil {
			t.Fatal(err)
		}
		if rows := result.([]json.RawMessage); rows == nil || len(rows) != 0 {
			t.Errorf("reviewers = %v", rows)
		}
	})
	t.Run("ambiguous batch leaves reviewers untouched", func(t *testing.T) {
		client := prWriteTestClient(t, first, identity, prWriteRequest{
			method: http.MethodGet, path: "/_apis/identities?api-version=5.0&filterValue=author%40example.invalid&searchFilter=General",
			response: fmt.Sprintf(`{"value":[{"id":%q},{"id":%q}]}`, repoTestAuthorID, repoTestViewerID),
		})
		_, err := runPRWrite(context.Background(), client, Scope{}, "repos pr reviewer remove", map[string]string{
			"id": "42", "reviewers": repoTestViewerID + "\x00author@example.invalid",
		})
		if err == nil || !strings.Contains(err.Error(), "multiple identities match") {
			t.Fatalf("expected the ambiguous identity error, got %v", err)
		}
	})
}

func TestPRWriteLostResponse(t *testing.T) {
	var received atomic.Int32
	client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		// The server received the mutation, then disconnected before responding.
		panic(http.ErrAbortHandler)
	})
	_, err := createPullRequest(t.Context(), client, Scope{Project: "App", Repository: "Kit"}, map[string]string{
		"source-branch": "feature", "target-branch": "main",
	})
	if err == nil || !strings.Contains(err.Error(), "outcome is unknown") || received.Load() != 1 {
		t.Fatalf("lost response after %d requests: %v", received.Load(), err)
	}
}
