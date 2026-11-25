package az

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGenericRequestMediaAndJSON(t *testing.T) {
	const patch = `[{"op":"add","path":"/fields/System.Title","value":"demo"}]`
	for _, test := range []struct {
		name, method, input, response, want string
		invoke                              bool
	}{
		{"get", "", "", `{"value":[1]}`, `{"continuation_token":"next","value":[1]}`, true},
		{"head", http.MethodHead, "", `{"value":[1]}`, "", true},
		{"options", http.MethodOptions, "", `[{"id":1}]`, `[{"id":1}]`, true},
		{"post", http.MethodPost, `{"name":"repo"}`, `{"id":1}`, `{"id":1}`, false},
		{"put", http.MethodPut, `{"name":"repo"}`, `true`, `true`, true},
		{"patch", http.MethodPatch, patch, `{"id":1}`, `{"id":1}`, false},
		{"delete", http.MethodDelete, "", "", "", true},
		{"empty", http.MethodPost, "", "   ", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				checkClientTestAuth(t, r)
				method := test.method
				if method == "" {
					method = http.MethodGet
				}
				if r.Method != method || r.URL.EscapedPath() != "/proxy/Fixture%20Collection/Shared%20Project/_apis/wit/workitems" || r.URL.Query().Get("api-version") != "7.1-preview.3" {
					t.Errorf("unexpected method or request URL %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Accept") != "application/json; charset=utf-8" || r.Header.Get("X-TFS-FedAuthRedirect") != "Suppress" {
					t.Error("request omitted the expected Accept or redirect suppression header")
				}
				contentType := ""
				if test.input != "" {
					contentType = "application/json"
				}
				if test.method == http.MethodPatch {
					contentType = "application/json-patch+json"
				}
				if got := r.Header.Get("Content-Type"); got != contentType {
					t.Errorf("content type %q, want %q", got, contentType)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != test.input {
					t.Errorf("body %q, error %v, want %q", body, err, test.input)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-MS-ContinuationToken", "next")
				if test.method == http.MethodDelete {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				io.WriteString(w, test.response)
			}))
			defer server.Close()
			client := newClientForTest(t, clientTestProfile(t, server, "/proxy/Fixture Collection"))
			request := genericRequest{method: test.method, accept: "application/json; charset=utf-8"}
			if test.input != "" {
				request.body = []byte(test.input)
			}
			if test.method == http.MethodPatch {
				request.mediaType = "application/json-patch+json"
			}
			var stdout bytes.Buffer
			query := url.Values{"api-version": {"7.1-preview.3"}}
			if err := request.run(t.Context(), client, []string{"Shared Project", "_apis", "wit", "workitems"}, query, &stdout, test.invoke); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(stdout.String()); got != test.want {
				t.Errorf("stdout %q, want %q", got, test.want)
			}
			if got := requests.Load(); got != 1 {
				t.Errorf("sent %d requests, want 1", got)
			}
		})
	}
}

func TestGenericRequestOutputFile(t *testing.T) {
	const raw = "\x00\xff\nraw data\r\n"
	for _, test := range []struct {
		name, want, wantError string
	}{
		{"download", raw, ""},
		{"existing", "existing", "already exists"},
		{"raced", "raced", "already exists"},
		{"truncated", "", "could not write"},
		{"no-content", "", ""},
		{"head", "", ""},
		{"invoke-head", "", ""},
		{"invoke-no-content", "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			out := filepath.Join(dir, "result.bin")
			if test.name == "existing" {
				if err := os.WriteFile(out, []byte("existing"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			client := repoTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch test.name {
				case "existing":
					t.Error("sent a request when the output already existed")
				case "raced":
					if err := os.WriteFile(out, []byte("raced"), 0600); err != nil {
						t.Error(err)
					}
				case "truncated":
					w.Header().Set("Content-Length", "1024")
				case "no-content", "invoke-no-content":
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				io.WriteString(w, raw)
			})
			request := genericRequest{outFile: out, accept: "application/octet-stream"}
			var stdout bytes.Buffer
			if strings.HasSuffix(test.name, "head") {
				request.method = http.MethodHead
			}
			err := request.run(t.Context(), client, []string{"_apis", "fixture"}, nil, &stdout, strings.HasPrefix(test.name, "invoke-"))
			if test.wantError == "" && err != nil || test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("error %v, want %q", err, test.wantError)
			}
			if stdout.Len() != 0 {
				t.Error("wrote to stdout with --out-file")
			}
			wantFile := test.wantError == "" && test.name != "invoke-no-content" || test.want != ""
			if wantFile {
				data, err := os.ReadFile(out)
				if err != nil || string(data) != test.want {
					t.Errorf("output file %q, error %v, want %q", data, err, test.want)
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !wantFile && len(entries) != 0 || wantFile && (len(entries) != 1 || entries[0].Name() != "result.bin") {
				t.Errorf("unexpected output directory entries %v", entries)
			}
		})
	}
}

func TestGenericRequestDoesNotFollowRedirectsOrExposeErrorBody(t *testing.T) {
	const private = "private-request-value"
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		checkClientTestAuth(t, r)
		if r.URL.Path == "/Collection/_apis/redirect" {
			w.Header().Set("Location", "/OtherCollection/_apis/fixture?private="+private)
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "%s %s", private, clientTestPassword)
	}))
	defer server.Close()
	client := newClientForTest(t, clientTestProfile(t, server, "/Collection"))
	request := genericRequest{method: http.MethodPost, body: []byte(`{"fixture":true}`)}
	for path, want := range map[string]string{"redirect": "HTTP 307", "error": "HTTP 400"} {
		var stdout bytes.Buffer
		err := request.run(t.Context(), client, []string{"_apis", path}, url.Values{"secret": {private}}, &stdout, false)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v, want %q", err, want)
		}
		if strings.Contains(err.Error(), private) || strings.Contains(err.Error(), clientTestPassword) || stdout.Len() != 0 {
			t.Error("command exposed request or response contents")
		}
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("sent %d requests, want 2", got)
	}
}
