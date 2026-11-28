package harness_test

import (
	"encoding/json"
	"image/png"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/toolspell/tfsaz/live-tests/harness"
)

func TestMain(m *testing.M) { os.Exit(harness.RunTests(m)) }

func TestSmoke(t *testing.T) {
	for _, osName := range harness.Platforms(t) {
		t.Run(osName, func(t *testing.T) {
			t.Parallel()
			h := harness.Start(t)
			vm := h.Clone(t, osName)

			shell, desktop := "printf shell", "printf desktop"
			query := "smoke=" + vm.Name
			curl := "curl --noproxy '*' --fail --silent --show-error --max-time 15 'http://devops.mock.test:8080/?" + query + "'"
			if osName == "windows" {
				shell, desktop = "Write-Output shell", "Write-Output desktop"
				curl = strings.Replace(curl, "curl ", "curl.exe ", 1) + "; exit $LASTEXITCODE"
			}
			requireOutput(t, vm.Run(t, shell), "shell")
			requireOutput(t, vm.RunGUI(t, desktop), "desktop")

			if result := vm.Run(t, curl); result.ExitCode != 0 || result.TimedOut {
				t.Fatalf("request to mock failed: %+v", result)
			}
			requests := h.Requests(t, vm)
			if !slices.ContainsFunc(requests, func(row harness.Request) bool {
				return row.ClientIP == vm.BedAddress && row.Method == http.MethodGet && row.Host == "devops.mock.test:8080" &&
					row.Path == "/" && row.Query == query && row.Status == http.StatusOK
			}) {
				t.Fatalf("mock journal for %s has no successful request with query %q: %+v", vm.Name, query, requests)
			}

			checkScreenshot(t, vm.Screenshot(t, "desktop.png"))
			vm.InstallT3(t)
			checkCDP(t, vm.StartT3(t))
		})
	}
}

func requireOutput(t *testing.T, result harness.Result, want string) {
	t.Helper()
	if result.ExitCode != 0 || result.TimedOut || strings.TrimSpace(result.Stdout) != want {
		t.Fatalf("guest command returned %+v; want stdout %q and exit code 0", result, want)
	}
}

func checkScreenshot(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := png.DecodeConfig(f); err != nil {
		t.Fatalf("read screenshot %s: %v", path, err)
	}
}

func checkCDP(t *testing.T, url string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url+"/json/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CDP /json/version at %s returned %s", url, resp.Status)
	}
	var version struct {
		WebSocketURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&version); err != nil {
		t.Fatal(err)
	}
	if version.WebSocketURL == "" {
		t.Fatalf("CDP /json/version at %s is missing webSocketDebuggerUrl", url)
	}
}
