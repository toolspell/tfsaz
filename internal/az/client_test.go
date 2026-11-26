package az

import (
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

var clientTestPassword = rand.Text()

const (
	clientTestUserID  = "b1111111-2222-4333-8444-555555555555"
	clientTestOtherID = "c1111111-2222-4333-8444-555555555555"
)

// serveTestBody writes a fixture response in a test handler. A failed write
// reaches the client under test as a broken response and fails the test there.
func serveTestBody(w io.Writer, body string) { _, _ = io.WriteString(w, body) }

// serveTestf is serveTestBody with a format.
func serveTestf(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

func clientTestProfile(t *testing.T, server *httptest.Server, collectionPath string) Profile {
	t.Helper()
	ca := filepath.Join(t.TempDir(), "root.pem")
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(ca, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	return Profile{ID: "test-collection", CollectionURL: server.URL + collectionPath, CAFile: ca}
}

func newClientForTest(t *testing.T, profile Profile) *Client {
	t.Helper()
	client, err := newClient(profile, clientTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.http.CloseIdleConnections)
	return client
}

func checkClientTestAuth(t *testing.T, r *http.Request) {
	t.Helper()
	user, password, ok := r.BasicAuth()
	if !ok || user != "" || password != clientTestPassword {
		t.Error("request did not use the configured Basic credential")
	}
}

func TestCollectionClientRequestScope(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		checkClientTestAuth(t, r)
		if got := r.URL.EscapedPath(); got != "/tfs/Second%20Collection/Demo%20Project/_apis/git/repositories/repo%23one" {
			t.Errorf("request path = %q", got)
		}
		if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/json" ||
			r.Header.Get("X-TFS-FedAuthRedirect") != "Suppress" || r.URL.Query().Get("filter") != "branch/feature & more" {
			t.Error("unexpected method, headers, or query")
		}
		serveTestBody(w, `{"name":"repo#one"}`)
	}))
	defer server.Close()
	client := newClientForTest(t, clientTestProfile(t, server, "/tfs/Second Collection/"))
	var result struct{ Name string }
	path := []string{"Demo Project", "_apis", "git", "repositories", "repo#one"}
	if err := client.Get(t.Context(), path, url.Values{"filter": {"branch/feature & more"}}, &result); err != nil || result.Name != "repo#one" {
		t.Fatalf("decoded result = %#v, error = %v", result, err)
	}
	for _, invalid := range []string{"", ".", "..", "%252e%252e", "a%2fb", "a/b", `a\b`, "a\nb"} {
		if err := client.Get(t.Context(), []string{"_apis", invalid}, nil, &result); err == nil {
			t.Errorf("accepted invalid segment %q", invalid)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("sent %d requests, want 1", got)
	}
}

func TestCollectionClientRejectsRedirects(t *testing.T) {
	var followed atomic.Int32
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
	}))
	defer destination.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Collection/_apis/same-host":
			checkClientTestAuth(t, r)
			http.Redirect(w, r, "/OtherCollection/_apis/connectionData", http.StatusTemporaryRedirect)
		case "/Collection/_apis/other-host":
			checkClientTestAuth(t, r)
			http.Redirect(w, r, destination.URL+"/OtherCollection/_apis/connectionData", http.StatusFound)
		default:
			followed.Add(1)
		}
	}))
	defer server.Close()
	client := newClientForTest(t, clientTestProfile(t, server, "/Collection"))
	for _, route := range []string{"same-host", "other-host"} {
		err := client.Get(t.Context(), []string{"_apis", route}, nil, new(any))
		if err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Errorf("%s error = %v, want redirect error", route, err)
		}
	}
	if followed.Load() != 0 {
		t.Fatal("followed a redirect outside the collection")
	}
}

func TestCollectionClientErrorDiagnostics(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		pr     bool
		want   string
	}{
		{"unauthorized", 401, "", false, "az devops login"},
		{"sign-in response", 203, "<html>Sign in</html>", false, "az devops login"},
		{"forbidden", 403, "", false, "403 Forbidden"},
		{"throttled", 429, "", false, "429"},
		{"server throttled", 503, `{"message":"TF400733: The request has been canceled"}`, false, "TF400733"},
		{"PR not found", 404, "", true, "pull request not found"},
		{"server PR error", 400, `{"message":"TF401180: The requested pull request was not found."}`, true, "pull request not found"},
		{"server PR error type", 400, `{"typeKey":"GitPullRequestNotFoundException"}`, true, "pull request not found"},
		{"collection route missing", 404, "", false, "HTTP 404"},
		{"invalid json", 200, "", false, "invalid JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const privateMarker = "private-body-or-query-value"
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				if tc.body != "" {
					serveTestBody(w, tc.body)
					return
				}
				serveTestf(w, "%s %s", privateMarker, clientTestPassword)
			}))
			defer server.Close()
			client := newClientForTest(t, clientTestProfile(t, server, "/Collection"))
			path := []string{"_apis", "connectionData"}
			if tc.pr {
				path = []string{"_apis", "git", "pullRequests", "42"}
			}
			err := client.Get(t.Context(), path, url.Values{"value": {privateMarker}}, new(any))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), privateMarker) || strings.Contains(err.Error(), clientTestPassword) {
				t.Fatal("diagnostic exposed request or response contents")
			}
			if tc.status == http.StatusForbidden && strings.Contains(err.Error(), "az devops login") {
				t.Fatal("forbidden was classified as a rejected PAT")
			}
		})
	}
}

func TestCollectionClientTLSAndAvailability(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveTestBody(w, `{}`)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	profile := clientTestProfile(t, server, "/Collection")
	if _, err := newClient(profile, ""); err == nil || !strings.Contains(err.Error(), "az devops login") {
		t.Fatalf("missing PAT error = %v", err)
	}
	untrusted := profile
	untrusted.CAFile = ""
	client := newClientForTest(t, untrusted)
	path := []string{"_apis", "connectionData"}
	if err := client.Get(t.Context(), path, nil, new(any)); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("untrusted certificate error = %v", err)
	}
	server.Close()
	client = newClientForTest(t, profile)
	if err := client.Get(t.Context(), path, nil, new(any)); err == nil || !strings.Contains(err.Error(), "could not reach") {
		t.Fatalf("unreachable collection error = %v", err)
	}
}

func TestCollectionClientAuthenticatedIdentity(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		fallback string
		login    string
		wantErr  string
		status   int
	}{
		{"qualified account", `"properties":{"Account":{"$type":"System.String","$value":"EXAMPLE\\operator"}}`, "", `EXAMPLE\operator`, "", 0},
		{"domain and account", `"properties":{"Account":{"$type":"System.String","$value":"operator"},"Domain":{"$type":"System.String","$value":"EXAMPLE"}}`, "", `EXAMPLE\operator`, "", 0},
		{"unique name", `"uniqueName":"operator@example.test"`, "", "operator@example.test", "", 0},
		{"fallback by ID", `"providerDisplayName":"Example Operator"`,
			fmt.Sprintf(`{"value":[{"id":%q,"uniqueName":"EXAMPLE\\operator"}]}`, strings.ToUpper(clientTestUserID)), `EXAMPLE\operator`, "", 0},
		{"display only", `"customDisplayName":"operator@example.test"`,
			fmt.Sprintf(`{"value":[{"id":%q,"providerDisplayName":"operator@example.test"}]}`, clientTestUserID), "", "did not return a login", 0},
		{"same descriptor with a different master ID", `"descriptor":"Microsoft.TeamFoundation.Identity;fixture-sid"`,
			fmt.Sprintf(`{"value":[{"id":%q,"descriptor":"Microsoft.TeamFoundation.Identity;fixture-sid","properties":{"Domain":"FIXTURE","Account":"test user"}}]}`, clientTestOtherID), `FIXTURE\test user`, "", 0},
		{"mismatched fallback", `"displayName":"Example Operator"`,
			fmt.Sprintf(`{"value":[{"id":%q,"uniqueName":"different@example.test"}]}`, clientTestOtherID), "", "did not return a login", 0},
		{"qualify bare account", `"properties":{"Account":{"$value":"operator"}},"descriptor":"Microsoft.TeamFoundation.Identity;fixture-sid"`,
			fmt.Sprintf(`{"value":[{"id":%q,"descriptor":"Microsoft.TeamFoundation.Identity;fixture-sid","properties":{"Account":{"$value":"EXAMPLE\\operator"}}}]}`, clientTestOtherID), `EXAMPLE\operator`, "", 0},
		{"keep bare account on mismatched principal", `"properties":{"Account":"operator"}`,
			fmt.Sprintf(`{"value":[{"id":%q,"uniqueName":"different@example.test"}]}`, clientTestOtherID), "operator", "", 0},
		{"keep bare login on unqualified match", `"uniqueName":"operator"`,
			fmt.Sprintf(`{"value":[{"id":%q,"uniqueName":"different"}]}`, clientTestUserID), "operator", "", 0},
		{"keep bare account without identity read access", `"properties":{"Account":"operator"}`,
			"", "operator", "", http.StatusForbidden},
		{"unavailable directory", `"properties":{"Account":"operator"}`, "", "operator", "", http.StatusInternalServerError},
		{"missing directory route", `"properties":{"Account":"operator"}`, "", "operator", "", http.StatusNotFound},
		{"directory timed out", `"properties":{"Account":"operator"}`, "", "operator", "", http.StatusGatewayTimeout},
		{"directory sign-in response", `"properties":{"Account":"operator"}`, "", "", "rejected the PAT", http.StatusNonAuthoritativeInfo},
		{"missing login requires identity read access", `"displayName":"Example Operator"`,
			"", "", "denied access", http.StatusForbidden},
		{"rejected PAT during enrichment", `"properties":{"Account":"operator"}`,
			"", "", "rejected the PAT", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var fallbackRequests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkClientTestAuth(t, r)
				switch r.URL.Path {
				case "/Collection/_apis/connectionData":
					if got := r.URL.Query().Get("api-version"); got != "5.0-preview.1" {
						t.Errorf("connectionData API version = %q", got)
					}
					serveTestf(w, `{"authenticatedUser":{"id":%q,%s},"authorizedUser":{"id":%q,"uniqueName":"different@example.test"}}`,
						clientTestUserID, tc.user, clientTestOtherID)
				case "/Collection/_apis/identities":
					fallbackRequests.Add(1)
					if r.URL.Query().Get("identityIds") != clientTestUserID || r.URL.Query().Get("api-version") != "5.0" {
						t.Error("identity fallback did not query the authenticated user's ID")
					}
					if tc.status != 0 {
						w.WriteHeader(tc.status)
					} else {
						serveTestBody(w, tc.fallback)
					}
				default:
					t.Error("request escaped the collection identity routes")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := newClientForTest(t, clientTestProfile(t, server, "/Collection"))
			id, err := client.Identity(t.Context())
			if tc.wantErr == "" {
				if err != nil || id != (Identity{ID: clientTestUserID, Login: tc.login}) {
					t.Fatalf("identity = %+v, error = %v", id, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) || id != (Identity{}) {
				t.Fatalf("identity = %+v, error = %v, want %q", id, err, tc.wantErr)
			}
			var wantRequests int32
			if tc.fallback != "" || tc.status != 0 {
				wantRequests = 1
			}
			if got := fallbackRequests.Load(); got != wantRequests {
				t.Errorf("identity fallback requests = %d, want %d", got, wantRequests)
			}
		})
	}
}

func TestCollectionClientRejectsUnverifiedIdentity(t *testing.T) {
	for _, id := range []string{"", "not-an-id", "AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA", "00000000-0000-0000-0000-000000000000"} {
		t.Run(id, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Collection/_apis/connectionData" {
					t.Error("queried identities for an unverified user")
				}
				serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"operator@example.test"},"authorizedUser":{"id":%q,"uniqueName":"different@example.test"}}`,
					id, clientTestOtherID)
			}))
			defer server.Close()
			client := newClientForTest(t, clientTestProfile(t, server, "/Collection"))
			got, err := client.Identity(t.Context())
			if err == nil || !strings.Contains(err.Error(), "az devops login") || got != (Identity{}) {
				t.Fatalf("identity = %+v, error = %v, want a credential error", got, err)
			}
		})
	}
}
