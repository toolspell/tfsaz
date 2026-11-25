package az

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOfflineCommandHelp(t *testing.T) {
	t.Setenv("TFSAZ_CONFIG", "invalid-relative-path")
	t.Setenv("TFSAZ_TRACE", "1")
	var out, diag bytes.Buffer
	a := app{stdout: &out, stderr: &diag}
	check := func(topic string) {
		t.Helper()
		t.Run(topic, func(t *testing.T) {
			out.Reset()
			args := append(strings.Fields(topic), "--help")
			if err := a.run(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			if text := out.String(); !strings.Contains(text, "Usage: tfsaz ") ||
				!strings.Contains(text, "Example") || diag.Len() != 0 {
				t.Fatalf("stdout = %q, stderr = %q", text, diag.String())
			}
		})
	}
	for topic := range helpGroups {
		check(topic)
	}
	for topic := range azureCommands {
		check(topic)
	}
	for topic := range nativeCommands {
		check(topic)
	}
	for _, invocation := range []struct{ args, want string }{
		{"repo setup ./checkout -h", "--ssh-url URL"},
		{"doctor ./checkout --help", "doctor [PATH] [--json]"},
		{"account show -h", "--query user.name -o tsv"},
		{"account show -h", "--query user -o json --organization"},
		{"repos pr list --help", "--include-links\n"},
		{"repos pr create --help", "--draft true|false"},
		{"boards work-item update --help", "--fields KEY=VALUE [KEY=VALUE ...]"},
		{"devops invoke --area build --help", "--output, -o json|tsv|table|none"},
		{"rest -h", "--url, --uri, -u URL"},
	} {
		out.Reset()
		if err := a.run(t.Context(), strings.Fields(invocation.args)); err != nil ||
			!strings.Contains(out.String(), invocation.want) {
			t.Errorf("%s: stdout = %q, want %q, err = %v", invocation.args, out.String(), invocation.want, err)
		}
	}
	for _, args := range []string{"route run -- git --help", "route run -- git -h", "repo setup -- --help", "profile list unexpected --help", "route verify-node unexpected --help"} {
		if topic, ok := helpTopic(strings.Fields(args)); ok {
			t.Errorf("%q intercepted by tfsaz help for %q", args, topic)
		}
	}
	if err := a.run(t.Context(), strings.Fields("repos unknown-private-command --help")); ExitCode(err) != 2 ||
		strings.Contains(err.Error(), "unknown-private-command") {
		t.Fatalf("unknown help topic error = %v", err)
	}
}

func TestSeparatedShortOptionsRequireValues(t *testing.T) {
	for _, test := range []struct {
		args string
		want string
	}{
		{"repos show -p -r Repo", "--project requires a value"},
		{"repos show --organization -o=json", "--organization requires a value"},
		{"account show --query -r", "--query requires a value"},
		{"repos show --project -x", "--project requires a value"},
		{"repos pr list --top -o json", "--top requires a value"},
		{"rest -u -m GET", "--url requires a value"},
	} {
		t.Run(test.args, func(t *testing.T) {
			_, _, err := parse(strings.Fields(test.args))
			if err == nil || err.Error() != test.want {
				t.Fatalf("parse error = %v, want %q", err, test.want)
			}
		})
	}
	_, opts, err := parse(strings.Fields("repos pr list -p -project --repository=-r --top -1 --source-branch -"))
	if err != nil || opts["project"] != "-project" || opts["repository"] != "-r" ||
		opts["top"] != "-1" || opts["source-branch"] != "-" {
		t.Fatalf("options = %v, err = %v", opts, err)
	}
}

func TestTraceReportsCollectionSelectionAndHTTPStatus(t *testing.T) {
	const (
		pat     = "fixture-credential-marker"
		payload = "private-payload-marker"
		login   = "fixture-identity-marker@example.invalid"
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, ok := r.BasicAuth(); !ok || password != pat {
			t.Error("fixture did not receive the configured credential")
		}
		switch r.URL.Path {
		case "/Denied/_apis/connectionData":
			http.Error(w, "private-error-marker", http.StatusUnauthorized)
		case "/Real/_apis/connectionData":
			fmt.Fprintf(w, `{"authenticatedUser":{"id":%q,"uniqueName":%q}}`, repoTestViewerID, login)
		case "/Real/_apis/private-path-marker":
			http.Error(w, "private-error-marker", http.StatusForbidden)
		default:
			t.Error("unexpected fixture request")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	missing := clientTestProfile(t, server, "/Missing")
	missing.ID, missing.Organization = "missing", "https://missing.visualstudio.com"
	denied := clientTestProfile(t, server, "/Denied")
	denied.ID, denied.Organization = "denied", "https://denied.visualstudio.com"
	real := clientTestProfile(t, server, "/Real")
	real.ID, real.Organization = "real", "https://real.visualstudio.com"
	setConfig(t, missing, denied, real)
	cwd := t.TempDir()
	gitAt(t, cwd, "init", "-q")
	gitAt(t, cwd, "remote", "add", "origin", real.Organization+"/Project/_git/Repo")
	store := &memoryStore{values: map[string]string{denied.ID: pat, real.ID: pat}}
	var out, diag bytes.Buffer
	a := app{cwd: cwd, stdout: &out, stderr: &diag, store: store}
	const discovery = "account show --query user.name -o tsv"

	t.Setenv("TFSAZ_TRACE", "0")
	if err := a.run(t.Context(), strings.Fields(discovery)); err != nil || out.String() != login+"\n" || diag.Len() != 0 {
		t.Fatalf("trace off: err = %v, stdout = %q, stderr = %q", err, out.String(), diag.String())
	}

	t.Setenv("TFSAZ_TRACE", "1")
	check := func(args []string, wantError bool, lines ...string) {
		t.Helper()
		out.Reset()
		diag.Reset()
		if err := a.run(t.Context(), args); (err != nil) != wantError {
			t.Fatalf("unexpected error: %v", err)
		}
		actual := strings.Split(strings.TrimSpace(diag.String()), "\n")
		if len(actual) != len(lines) {
			t.Fatalf("stderr = %q, want %d trace lines", diag.String(), len(lines))
		}
		for i, line := range lines {
			elapsed, ok := strings.CutPrefix(actual[i], "tfsaz trace "+line+" elapsed=")
			if _, err := time.ParseDuration(elapsed); !ok || err != nil {
				t.Errorf("trace line = %q, want %q and duration", actual[i], line)
			}
		}
		for _, value := range []string{server.URL, real.Organization, pat, login, payload, "private-path-marker", "private-error-marker", "private-query-marker"} {
			if strings.Contains(diag.String(), value) {
				t.Error("trace leaked request or response data")
			}
		}
	}
	check(strings.Fields(discovery), false,
		`command="account show" profile=missing selection=discovery status=none`,
		`command="account show" profile=denied selection=discovery status=401`,
		`command="account show" profile=real selection=discovery status=200`)
	if out.String() != login+"\n" {
		t.Fatalf("trace changed discovery output to %q", out.String())
	}
	check(strings.Fields("account show --query user -o json"), false,
		`command="account show" profile=real selection=checkout status=200`)
	check(strings.Fields("account show --query user -o json --org "+denied.Organization), true,
		`command="account show" profile=denied selection=organization status=401`)
	const path = "/_apis/private-path-marker?query=private-query-marker"
	check([]string{"rest", "-u", real.CollectionURL + path, "-m", "POST", "-b", payload}, true,
		`command="rest" profile=real selection=rest-url status=403`)
	check([]string{"rest", "-u", path}, true,
		`command="rest" profile=real selection=checkout status=403`)
}
