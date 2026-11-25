package az

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSystemBrowserTargets(t *testing.T) {
	cfg := httpsRouteTestConfig()
	file := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TFSAZ_CONFIG", file)
	// Ordinary links keep working even if the user removes the config.
	const ordinary = "https://example.org/search?q=hello%20there#part"
	if got, err := browserTarget(ordinary); err != nil || got != ordinary {
		t.Fatalf("ordinary link got %q, %v", got, err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	const native = "https://server.invalid:8443/tfs/Main%20Collection/P/_git/R/pullrequest/17?view=files#details"
	for source, want := range map[string]string{
		"http://internal_host.intranet/":                                              "http://internal_host.intranet/",
		"https://dev.azure.com/example/P/_git/R/pullrequest/17?view=files#details":    native,
		"https://example.visualstudio.com/P/_git/R/pullrequest/17?view=files#details": native,
		"https://dev.azure.com/other/P/_git/R":                                        "https://dev.azure.com/other/P/_git/R",
		"http://café.example/path":                                                    "http://café.example/path",
		"http://[::1]:9090/":                                                          "http://[::1]:9090/",
	} {
		if got, err := browserTarget(source); err != nil || got != want {
			t.Errorf("%q got %q, %v; want %q", source, got, err, want)
		}
	}
	for _, source := range []string{
		"javascript:alert(1)", "file:///tmp/foo", "https://u:p@example.org/",
		"https://example.visualstudio.com/_apis/projects",
		"http://example.visualstudio.com/P/_git/R",
		"https://example.visualstudio.com/P/_git/R/git-upload-pack",
		"https://dev.azure.com/other/%2e%2e/example/P/_git/R",
		"https://dev.azure.com/%2565xample/P/_git/R",
		"https://dev.azure.com/tfsaz-old/P/_git/R",
		"https://tfsaz-old.visualstudio.com/P/_git/R",
		"https://ｅxample.visualstudio.com/P/_git/R",
	} {
		if got, err := browserTarget(source); err == nil || got != "" {
			t.Errorf("%q unexpectedly passed as %q", source, got)
		}
	}
}
