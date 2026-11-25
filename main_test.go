package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAzCompatibilityDiagnostics(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "az")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build az: %v\n%s", err, output)
	}
	t.Setenv("TFSAZ_CONFIG", filepath.Join(dir, "missing.json"))

	for _, test := range []struct {
		name   string
		args   []string
		status int
		reason string
	}{
		{"command", []string{"repos", "private-marker"}, 2, "unknown command"},
		{"generic REST", []string{"rest", "--url", "https://example.invalid/private-marker"}, 1, "read tfsaz configuration"},
		{"long option", []string{"devops", "login", "--private-marker=private-marker"}, 2, "unsupported option"},
		{"short option", []string{"repos", "show", "-private-marker"}, 2, "unsupported option"},
		{"dash-prefixed repository value", []string{"repos", "show", "--repository", "-repo"}, 1, "read tfsaz configuration"},
		{"query", []string{"account", "show", "--query", "private-marker", "-o", "json"}, 2, "supported account queries"},
		{"output", []string{"repos", "show", "-o", "private-marker"}, 2, "require JSON output"},
		{"status", []string{"repos", "pr", "list", "--status", "private-marker"}, 2, "--status expects"},
		{"discovery", []string{"devops", "invoke"}, 1, "read tfsaz configuration"},
		{"scoped discovery", []string{"devops", "invoke", "--area", "git", "--org=https://example.invalid/private-marker"}, 1, "read tfsaz configuration"},
		{"invoke area", []string{"devops", "invoke", "--area", "private-marker"}, 1, "read tfsaz configuration"},
		{"invoke resource", []string{"devops", "invoke", "--area", "git", "--resource", "private-marker"}, 1, "read tfsaz configuration"},
		{"invoke version", []string{"devops", "invoke", "--area", "git", "--resource", "items", "--api-version", "private-marker"}, 2, "invalid --api-version"},
		{"invoke shape", []string{"devops", "invoke", "--area", "git", "--resource", "items", "--api-version", "7.1", "--http-method", "private-marker"}, 2, "unsupported HTTP method"},
		{"runtime", []string{"account", "show", "--query", "user.name", "-o", "tsv"}, 1, "read tfsaz configuration"},
		{"bare command", nil, 0, "Azure DevOps Server for T3 Code"},
		{"help", []string{"--help"}, 0, "Azure DevOps Server for T3 Code"},
		{"version", []string{"--version"}, 0, "tfsaz "},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), binary, test.args...)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdout, err := cmd.Output()
			if err != nil && cmd.ProcessState == nil {
				t.Fatalf("start az: %v", err)
			}
			if got := cmd.ProcessState.ExitCode(); got != test.status {
				t.Fatalf("exit %d, want %d; stderr %q", got, test.status, stderr.String())
			}
			if test.status == 0 {
				if !strings.Contains(string(stdout), test.reason) || stderr.Len() != 0 {
					t.Fatalf("stdout = %q, stderr = %q", stdout, stderr.String())
				}
				return
			}
			diagnostic := stderr.String()
			if len(stdout) != 0 || !strings.HasPrefix(diagnostic, "tfsaz: ") ||
				!strings.Contains(diagnostic, test.reason) || strings.Contains(diagnostic, "private-marker") {
				t.Fatalf("stdout = %q, stderr = %q", stdout, diagnostic)
			}
			for _, marker := range []string{"unsupported invocation", "https://github.com/toolspell/tfsaz#readme", "tfsaz --help"} {
				if strings.Contains(diagnostic, marker) != (test.status == 2) {
					t.Errorf("unexpected presence of %q in %q", marker, diagnostic)
				}
			}
		})
	}
}
