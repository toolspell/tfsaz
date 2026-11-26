package az

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"
)

// This opt-in test runs T3's actual process-spawning adapter. It writes one
// disposable, random loopback credential to the OS keyring under a fresh ID.
func TestStockT3Adapter(t *testing.T) {
	root := os.Getenv("TFSAZ_T3_ROOT")
	if root == "" {
		t.Skip("set TFSAZ_T3_ROOT to a T3 checkout with its dependencies installed")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	pat := rand.Text()
	pr := repoFixture(t, "pull-request")
	repo := repoFixture(t, "repository")
	var policies struct{ Value []json.RawMessage }
	if err := json.Unmarshal([]byte(repoFixture(t, "policy-evaluations")), &policies); err != nil {
		t.Fatal(err)
	}
	// A full first page of filtered-out rows must not hide the next policy.
	firstPage := bytes.ReplaceAll(policies.Value[0], []byte(`"isEnabled": true`), []byte(`"isEnabled": false`))
	var first, second atomic.Int32
	var blobBatches, escapedItemReads, extraItemReads atomic.Int32
	var prUpdates atomic.Int32
	invoke := newInvokeFixture(t)
	boards := newT3BoardsFixture(t)
	const prefix = "/tfs/Fixture Collection"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, ok := r.BasicAuth(); !ok || password != pat {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		if !strings.HasPrefix(r.URL.Path, prefix+"/") {
			t.Error("wrong collection")
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, prefix) {
		case "/_apis/connectionData":
			serveTestf(w, `{"authenticatedUser":{"id":%q,"descriptor":"Microsoft.TeamFoundation.Identity;fixture-sid","properties":{"Account":{"$value":"author"}}}}`, repoTestViewerID)
		case "/_apis/identities":
			if r.URL.Query().Get("identityIds") != repoTestViewerID {
				t.Error("identity lookup must use the authenticated user's collection ID")
			}
			serveTestf(w, `{"value":[{"id":%q,"descriptor":"Microsoft.TeamFoundation.Identity;fixture-sid","properties":{"Account":{"$value":"author@example.invalid"}}}]}`, clientTestOtherID)
		case "/_apis/git/pullrequests/42":
			serveTestBody(w, pr)
		case "/" + repoTestProjectID + "/_apis/git/repositories/10101010-1111-4111-8111-101010101010/pullrequests/42":
			if r.Method != http.MethodPatch || r.Header.Get("Content-Type") != "application/json" || r.URL.Query().Get("api-version") != "7.1" {
				t.Error("unexpected PR update request")
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			} else if len(body) != 1 || body["description"] != "@fixture body — Grüße\n第二行" {
				t.Errorf("unexpected PR update payload: %#v", body)
			}
			prUpdates.Add(1)
			serveTestBody(w, pr)
		case "/Shared Project/_apis/git/repositories/Tool Kit":
			var details map[string]any
			if err := json.Unmarshal([]byte(repo), &details); err != nil {
				t.Error(err)
				return
			}
			details["sshUrl"] = "ssh://git@" + r.Host + "/tfs/Fixture%20Collection/Shared%20Project/_git/Tool%20Kit"
			if err := json.NewEncoder(w).Encode(details); err != nil {
				t.Error(err)
			}
		case "/Shared Project/_apis/git/repositories/Tool Kit/pullrequests":
			if r.URL.Query().Get("searchCriteria.creatorId") != repoTestViewerID {
				t.Error("missing scoped creator filter")
			}
			serveTestf(w, `{"value":[%s]}`, pr)
		case "/" + repoTestProjectID + "/_apis/policy/evaluations":
			if r.URL.Query().Get("$top") != "100" {
				t.Error("unexpected policy page size")
			}
			switch r.URL.Query().Get("$skip") {
			case "0":
				first.Add(1)
				rows := make([]json.RawMessage, 100)
				for i := range rows {
					rows[i] = firstPage
				}
				if err := json.NewEncoder(w).Encode(map[string]any{"value": rows}); err != nil {
					t.Error(err)
				}
			case "100":
				last := policies.Value[0]
				if second.Add(1) > 1 {
					last = bytes.ReplaceAll(last, []byte(`"queued"`), []byte(`"approved"`))
				}
				serveTestf(w, `{"value":[%s]}`, last)
			default:
				t.Error("unexpected policy offset")
				http.NotFound(w, r)
			}
		default:
			if boards.serve(t, w, r, prefix) {
				return
			}
			if invoke.serve(t, w, r, strings.TrimPrefix(r.URL.Path, prefix)) {
				switch strings.TrimPrefix(r.URL.Path, prefix+invokeFixtureRepo) {
				case "/blobs":
					blobBatches.Add(1)
				case "/items":
					switch r.URL.Query().Get("path") {
					case invokeFixtureItem:
						escapedItemReads.Add(1)
					case "/extra.md":
						extraItemReads.Add(1)
					}
				}
				return
			}
			t.Errorf("unexpected fixture path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	p := clientTestProfile(t, server, prefix)
	p.ID = "offline-" + rand.Text()
	p.Organization = "https://fixture.visualstudio.com"
	setConfig(t, p)
	bin := t.TempDir()
	exe := testExecutable(filepath.Join(bin, "az"))
	build := exec.Command("go", "build", "-o", exe, "../..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		if err := keyring.Delete(keyringService, p.ID); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			t.Errorf("remove fixture keyring entry %s: %v", p.ID, err)
		}
	})
	login := exec.Command(exe, "devops", "login", "--organization", p.Organization)
	login.Stdin = strings.NewReader(pat)
	if out, err := login.CombinedOutput(); err != nil {
		t.Fatalf("fixture login: %v: %s", err, out)
	}
	cwd := t.TempDir()
	gitAt(t, cwd, "init", "-q")
	probe := exec.Command(exe, "account", "show", "--query", "user.name", "-o", "tsv")
	if out, err := probe.Output(); err != nil || string(out) != "author@example.invalid\n" {
		t.Fatalf("discovery probe: %v, %q", err, out)
	}
	// A read-only empty repository speaks the Git packet protocol. This
	// keeps the production Git/SSH helper and stock T3 entry points in the test.
	setupTestSSH(t, bin, "#!/bin/sh\nprintf '0000'\n")
	gitAt(t, cwd, "remote", "add", "origin", p.CollectionURL+"/Shared%20Project/_git/Tool%20Kit")
	setup := exec.CommandContext(t.Context(), exe, "repo", "setup", cwd, "--managed-ssh")
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("managed setup: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command(exe, "repo", "restore", cwd).CombinedOutput(); err != nil {
			t.Errorf("restore: %v: %s", err, out)
		}
	})
	records, err := readSetupRecords()
	if err != nil || len(records) != 1 {
		t.Fatalf("setup records: count %d, error %v", len(records), err)
	}
	web := "https://" + records[0].Host + "/Shared%20Project/_git/Tool%20Kit"
	run := exec.CommandContext(t.Context(), "node", "--experimental-import-meta-resolve", "testdata/t3-adapter.mjs", root, cwd, web, t.TempDir(), p.Organization)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("T3 adapter: %v: %s", err, out)
	}
	if first.Load() != 2 || second.Load() != 2 {
		t.Errorf("expected two reads of both pages, got %d and %d", first.Load(), second.Load())
	}
	if blobBatches.Load() != 1 || escapedItemReads.Load() != 2 || extraItemReads.Load() != 1 {
		t.Errorf("want one blob batch, two explicit item sides and one fallback item; got %d, %d, %d",
			blobBatches.Load(), escapedItemReads.Load(), extraItemReads.Load())
	}
	if got := boards.batchReads.Load(); got != 2 {
		t.Errorf("want batches for both search pages and none for an empty result, got %d", got)
	}
	if prUpdates.Load() != 1 || boards.commentWrites.Load() != 1 {
		t.Errorf("want one PR update and one work-item comment, got %d and %d", prUpdates.Load(), boards.commentWrites.Load())
	}
}

const (
	t3BoardsProject = "Shared Project"
	t3BoardsCursor  = "next +/&=%2F"
)

type t3BoardsFixture struct {
	responses     map[string]json.RawMessage
	batchReads    atomic.Int32
	commentWrites atomic.Int32
}

func newT3BoardsFixture(t *testing.T) *t3BoardsFixture {
	t.Helper()
	fixture := new(t3BoardsFixture)
	if err := json.Unmarshal([]byte(repoFixture(t, "t3-boards-reads")), &fixture.responses); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (f *t3BoardsFixture) workItem(t *testing.T, r *http.Request, collection string, id int, detail bool) map[string]any {
	t.Helper()
	var item map[string]any
	if err := json.Unmarshal(f.responses["workItem"], &item); err != nil {
		t.Error(err)
		return nil
	}
	// Server URLs can include both the virtual directory and the project. T3's
	// issue decoder needs the equivalent URL under the Azure-style organization.
	item["url"] = (&url.URL{
		Scheme: "https", Host: r.Host,
		Path: fmt.Sprintf("%s/%s/_apis/wit/workItems/%d", collection, t3BoardsProject, id),
	}).String()
	item["id"] = id
	fields := item["fields"].(map[string]any)
	fields["System.Id"] = id
	if !detail {
		delete(item, "relations")
		delete(fields, "System.Description")
		delete(fields, "System.WorkItemType")
		fields["System.State"] = "Active"
		if id != 7 {
			fields["System.Title"] = fmt.Sprintf("Reader's focus %d", id)
		}
		if id == 3 {
			fields["System.ChangedDate"] = "2026-07-02T00:00:00Z"
		}
	}
	return item
}

func (f *t3BoardsFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request, collection string) bool {
	t.Helper()
	path := strings.TrimPrefix(r.URL.Path, collection)
	projectWIT := "/" + t3BoardsProject + "/_apis/wit/"
	query := r.URL.Query()
	method := http.MethodGet
	var answer any
	switch path {
	case "/_apis":
		method = http.MethodOptions
		if query.Get("allHostTypes") != "true" {
			t.Error("catalog request did not select all host types")
		}
		answer = json.RawMessage(`{"value":[{"area":"wit","resourceName":"comments","routeTemplate":"{project}/_apis/wit/workItems/{workItemId}/comments","minVersion":"1.0","maxVersion":"7.1","releasedVersion":"7.0","resourceVersion":4}]}`)
	case projectWIT + "workitemtypes":
		answer = f.responses["types"]
	case projectWIT + "wiql":
		method = http.MethodPost
		if query.Get("timePrecision") != "true" || query.Get("$top") != "3" {
			t.Errorf("unexpected WIQL query parameters %v", query)
		}
		var body struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !strings.Contains(body.Query, "[System.TeamProject] = 'Shared Project'") ||
			!strings.HasSuffix(body.Query, "ORDER BY [System.ChangedDate] DESC, [System.Id] DESC") {
			t.Errorf("unexpected WIQL scope or order: %q", body.Query)
		}
		ids := []map[string]int{}
		if !strings.Contains(body.Query, "[System.Title] CONTAINS 'no match'") {
			for _, clause := range []string{
				"[System.State] NOT IN ('shipped')", "[System.AssignedTo] = @Me",
				"[System.Title] CONTAINS 'reader''s focus'", "[System.Tags] CONTAINS 'interface'",
			} {
				if !strings.Contains(body.Query, clause) {
					t.Errorf("WIQL misses %q: %s", clause, body.Query)
				}
			}
			if strings.Contains(body.Query, "[System.Id] <") {
				if !strings.Contains(body.Query, "([System.ChangedDate] < '2026-07-03T00:00:00Z' OR ([System.ChangedDate] = '2026-07-03T00:00:00Z' AND [System.Id] < 7))") {
					t.Errorf("unexpected WIQL cursor: %q", body.Query)
				}
				ids = []map[string]int{{"id": 3}}
			} else {
				ids = []map[string]int{{"id": 9}, {"id": 7}, {"id": 3}}
			}
		}
		answer = map[string]any{"workItems": ids}
	case projectWIT + "workitemsbatch":
		method = http.MethodPost
		var body struct {
			IDs    []int
			Fields []string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !slices.Equal(body.Fields, []string{
			"System.Id", "System.Title", "System.State", "System.CreatedDate", "System.ChangedDate", "System.CreatedBy",
			"System.AssignedTo", "System.CommentCount", "System.Tags", "System.TeamProject", "Microsoft.VSTS.Common.ClosedDate",
		}) {
			t.Errorf("unexpected batch fields: %v", body.Fields)
		}
		switch reads := f.batchReads.Add(1); {
		case reads == 1 && slices.Equal(body.IDs, []int{9, 7}):
			// Return the first batch reversed so stock T3 must restore WIQL order.
			slices.Reverse(body.IDs)
		case reads == 2 && slices.Equal(body.IDs, []int{3}):
		default:
			t.Errorf("unexpected batch %d with IDs %v", reads, body.IDs)
		}
		rows := make([]map[string]any, 0, len(body.IDs))
		for _, id := range body.IDs {
			rows = append(rows, f.workItem(t, r, collection, id, false))
		}
		answer = map[string]any{"count": len(rows), "value": rows}
	case "/_apis/wit/workItems/7", "/_apis/wit/workitems/7":
		if query.Get("$expand") != "relations" {
			t.Errorf("work item detail did not request relations: %v", query)
		}
		answer = f.workItem(t, r, collection, 7, true)
	case projectWIT + "workItems/7/comments", projectWIT + "workitems/7/comments":
		if r.Method == http.MethodPost {
			method = http.MethodPost
			if query.Get("format") != "markdown" || query.Get("api-version") != "7.1-preview.4" {
				t.Errorf("unexpected comment write query parameters %v", query)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			} else if len(body) != 1 || body["text"] != "@fixture comment — café\n再現手順" {
				t.Errorf("unexpected comment payload: %#v", body)
			}
			f.commentWrites.Add(1)
			answer = map[string]int{"id": 12}
			break
		}
		if !slices.Equal(query["api-version"], []string{"7.1-preview.4"}) ||
			query.Get("$top") != "100" || query.Get("order") != "desc" || query.Get("$expand") != "reactions" {
			t.Errorf("unexpected comments query parameters %v", query)
		}
		switch query.Get("continuationToken") {
		case "":
			answer = f.responses["comments"]
		case t3BoardsCursor:
			answer = f.responses["olderComments"]
		default:
			t.Errorf("unexpected comments cursor %q", query.Get("continuationToken"))
		}
	case projectWIT + "workItems/7/updates", projectWIT + "workitems/7/updates":
		if query.Get("$top") != "100" {
			t.Errorf("unexpected updates page size %v", query)
		}
		switch query.Get("$skip") {
		case "", "0":
			rows := make([]map[string]any, 100)
			for index := range rows {
				rows[index] = map[string]any{"id": index + 1, "revisedDate": "2026-07-01T00:00:00Z"}
			}
			rows[99]["revisedDate"] = "2026-07-02T00:00:00Z"
			rows[99]["fields"] = map[string]any{
				"System.Title": map[string]string{"oldValue": "Focus issue", "newValue": "Reader's focus"},
			}
			answer = map[string]any{"count": len(rows), "value": rows}
		case "100":
			answer = f.responses["updates"]
		default:
			t.Errorf("unexpected updates offset %q", query.Get("$skip"))
		}
	case projectWIT + "tags":
		answer = f.responses["tags"]
	case "/_apis/projects/Shared Project/teams":
		if query.Get("$top") != "10" {
			t.Errorf("unexpected team page size: %v", query)
		}
		answer = f.responses["teams"]
	case "/_apis/projects/Shared Project/teams/30303030-3333-4333-8333-303030303030/members",
		"/_apis/projects/Shared Project/teams/40404040-4444-4444-8444-404040404040/members":
		if query.Get("$top") != "100" {
			t.Errorf("unexpected team member page size: %v", query)
		}
		answer = f.responses["members"+filepath.Base(filepath.Dir(path))]
	default:
		return false
	}
	if r.Method != method || method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
		t.Errorf("unexpected %s request with content type %q at %s", r.Method, r.Header.Get("Content-Type"), path)
	}
	if err := json.NewEncoder(w).Encode(answer); err != nil {
		t.Error(err)
	}
	return true
}
