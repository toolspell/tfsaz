package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCollectionURLs(t *testing.T) {
	mockProject := &project{ID: "project-id", Name: "Mock Project"}
	mockRepo := &repo{ID: "repo-id", Name: "web app", Project: mockProject}
	coll := &collection{Name: "DefaultCollection", Projects: []*project{mockProject}, Repos: []*repo{mockRepo}}
	s := &server{cfg: config{sshPort: "2222", pat: "test"}, collections: []*collection{coll}, viewer: &identity{}}
	for _, tc := range []struct {
		name, origin, virtualDir, sshHost string
	}{
		{"HTTP", "http://devops.mock.test:8080", "", "devops.mock.test"},
		{"HTTPS", "https://devops.mock.test", "", "devops.mock.test"},
		{"HTTPS with virtual directory", "https://other.mock.test:8443", "/tfs", "other.mock.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := tc.origin + tc.virtualDir + "/DefaultCollection"
			target := base + "/_apis?allHostTypes=true"
			r := httptest.NewRequest(http.MethodOptions, target, nil)
			r.SetBasicAuth("", s.cfg.pat)
			response := httptest.NewRecorder()
			s.ServeHTTP(response, r)
			if response.Code != http.StatusOK || response.Header().Get("Content-Location") != target || !bytes.Equal(response.Body.Bytes(), catalog) {
				t.Fatalf("catalog status = %d, Content-Location = %q; want 200, %q and the recorded catalog", response.Code, response.Header().Get("Content-Location"), target)
			}
			dir, _ := splitCollectionPath(r.URL.Path)
			links := s.links(r, dir, coll)
			if got, want := links.api("_apis", "Identities", "id"), base+"/_apis/Identities/id"; got != want {
				t.Errorf("identity URL = %q; want %q", got, want)
			}
			if got, want := links.gitWeb(mockRepo), base+"/Mock%20Project/_git/web%20app"; got != want {
				t.Errorf("clone URL = %q; want %q", got, want)
			}
			if got, want := links.gitSSH(mockRepo), "ssh://"+tc.sshHost+":2222"+tc.virtualDir+"/DefaultCollection/Mock%20Project/_git/web%20app"; got != want {
				t.Errorf("SSH clone URL = %q; want %q", got, want)
			} else if s.repoForSSHPath(tc.virtualDir+"/DefaultCollection/Mock%20Project/_git/web%20app") != mockRepo {
				t.Error("SSH lookup did not find the repository")
			}

			target = base + "/_apis/connectionData?api-version=5.0-preview.1"
			r = httptest.NewRequest(http.MethodGet, target, nil)
			r.SetBasicAuth("", s.cfg.pat)
			response = httptest.NewRecorder()
			s.ServeHTTP(response, r)
			var data struct{ WebApplicationRelativeDirectory string }
			if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
			wantDir := strings.TrimPrefix(tc.virtualDir+"/DefaultCollection/", "/")
			if response.Code != http.StatusOK || response.Header().Get("Content-Location") != target || data.WebApplicationRelativeDirectory != wantDir {
				t.Errorf("connection data status = %d, Content-Location = %q, relative directory = %q; want 200, %q, %q",
					response.Code, response.Header().Get("Content-Location"), data.WebApplicationRelativeDirectory, target, wantDir)
			}
		})
	}
}
