package az

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRoutingCommandsValidateBeforeConfig(t *testing.T) {
	t.Setenv("TFSAZ_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	a := app{stdout: io.Discard, stderr: io.Discard}
	for _, args := range [][]string{
		{"proxy", "--port", "0"}, {"pac", "--port", "65536"},
		{"proxy", "--include-dev-azure", "--include-dev-azure"},
		{"sink", "--port", "not-a-port"}, {"sink", "--port"}, {"verify-hosts", "--unknown"},
		{"handler-export", "destination"}, {"handler-open", "config", "browser"}, {"mac-handler-export"},
		{"browser-target"}, {"verify-node", "extra"}, {"run", "git"}, {"run", "--"},
		{"redirect", "--cert", "c.pem"}, {"resolve"}, {"explain", "a", "b"}, {"browser-export"}, {"unknown"},
	} {
		err := a.run(t.Context(), append([]string{"route"}, args...))
		if ExitCode(err) != 2 {
			t.Errorf("route %v: got %v, want usage error", args, err)
		}
	}
}

// The listener prints its ready message only after binding all of its sockets.
type routeTestStatus chan string

func (s routeTestStatus) Write(b []byte) (int, error) {
	s <- string(b)
	return len(b), nil
}

func TestLocalRoutingServices(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	preserveTestConfig(t, cfgPath, "https://server.invalid/Collection")
	t.Setenv("TFSAZ_CONFIG", cfgPath)
	for _, service := range []string{"sink", "proxy"} {
		t.Run(service, func(t *testing.T) {
			free, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := free.Addr().String()
			_, port, err := net.SplitHostPort(address)
			if closeErr := free.Close(); err != nil || closeErr != nil {
				t.Fatal(errors.Join(err, closeErr))
			}
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			status := make(routeTestStatus, 2)
			a := app{stdout: io.Discard, stderr: status}
			done := make(chan error, 1)
			args := []string{"route", service, "--port", port}
			if service == "proxy" {
				args = append(args, "--include-dev-azure")
			}
			go func() { done <- a.run(ctx, args) }()
			select {
			case <-status:
			case err := <-done:
				t.Fatalf("listener did not start: %v", err)
			case <-ctx.Done():
				t.Fatal("timed out waiting for listener")
			}
			switch service {
			case "sink":
				for _, host := range []string{"127.0.0.1", "::1"} {
					conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), time.Second)
					if err != nil {
						t.Fatal(err)
					}
					if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
						t.Fatal(err)
					}
					var b [1]byte
					n, err := conn.Read(b[:])
					closeQuietly(conn)
					if n != 0 || !errors.Is(err, io.EOF) {
						t.Fatalf("sink read returned %d bytes, %v", n, err)
					}
				}
			case "proxy":
				request, err := http.NewRequestWithContext(ctx, "GET", "http://"+address+"/proxy.pac", nil)
				if err != nil {
					t.Fatal(err)
				}
				client := http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
				resp, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				closeQuietly(resp.Body)
				if err != nil || resp.StatusCode != 200 || !bytes.Contains(body, []byte("dev.azure.com")) ||
					!bytes.Contains(body, []byte("PROXY "+address)) {
					t.Fatalf("PAC status %d, body %q, error %v", resp.StatusCode, body, err)
				}
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("listener did not stop on cancellation")
			}
			if service == "sink" && !strings.Contains(<-status, "refused 2 TCP connection(s)") {
				t.Error("sink did not report both loopback connections")
			}
		})
	}
}

func TestRouteResolveAndExplain(t *testing.T) {
	const collection = "https://server.invalid:8443/tfs/Main%20Collection"
	setConfig(t, Profile{ID: "fixture", Organization: "https://fixture.visualstudio.com", CollectionURL: collection})
	const source = "https://fixture.visualstudio.com/My%20Project/_git/Repo/pullrequest/42"
	var out bytes.Buffer
	a := app{stdout: &out, stderr: io.Discard}
	if err := a.run(t.Context(), []string{"route", "resolve", source}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), collection+"/My%20Project/_git/Repo/pullrequest/42\n"; got != want {
		t.Fatalf("resolve printed %q, want %q", got, want)
	}
	out.Reset()
	if err := a.run(t.Context(), []string{"route", "explain", source}); err != nil {
		t.Fatal(err)
	}
	var route httpsRoute
	if err := json.Unmarshal(out.Bytes(), &route); err != nil || route.ProfileID != "fixture" ||
		route.Destination != collection+"/My%20Project/_git/Repo/pullrequest/42" || route.Action != httpsActionRedirect {
		t.Fatalf("explain printed %q, %v", &out, err)
	}
	if err := a.run(t.Context(), []string{"route", "resolve", "https://native.invalid/P/_git/R"}); err == nil {
		t.Fatal("resolve accepted a URL that is not an Azure-style alias")
	}
}

func TestRouteRunRequiresGitHTTPSTransport(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TFSAZ_CONFIG", cfgPath)
	notRegular := t.TempDir()
	if err := os.Mkdir(filepath.Join(notRegular, gitHTTPSHelperName()), 0700); err != nil {
		t.Fatal(err)
	}
	for _, core := range []string{"relative", t.TempDir(), notRegular} {
		t.Setenv("TFSAZ_GIT_CORE", core)
		a := app{stdout: io.Discard, stderr: io.Discard}
		err := a.run(t.Context(), []string{"route", "run", "--", "git", "--version"})
		if err == nil || !strings.Contains(err.Error(), "native HTTPS transport") {
			t.Errorf("TFSAZ_GIT_CORE=%s: got %v, want a missing transport error", core, err)
		}
		if _, err := os.Lstat(cfgPath + ".run"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("TFSAZ_GIT_CORE=%s: route run created an overlay: %v", core, err)
		}
	}
}

func TestPopulateGitExecPath(t *testing.T) {
	core, overlay := t.TempDir(), t.TempDir()
	helper := gitHTTPSHelperName()
	for name, content := range map[string]string{
		helper: "helper", "git-sh-setup": "library", filepath.Join("mergetools", "vimdiff"): "tool",
		"git.exe": "binary", "libcurl.dll": "library binary",
	} {
		path := filepath.Join(core, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		managerTestWrite(t, path, content)
	}
	if err := populateGitExecPath(core, overlay, helper); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		helper: "", "git-sh-setup": "library", filepath.Join("mergetools", "vimdiff"): "tool",
		"git.exe": "binary", "libcurl.dll": "library binary",
	}
	if runtime.GOOS == "windows" {
		// Windows binaries stay beside their DLLs and run from Git's own directory.
		want["git.exe"], want["libcurl.dll"] = "", ""
	}
	for name, want := range want {
		data, err := os.ReadFile(filepath.Join(overlay, name))
		if want == "" {
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("overlay has %s: %v", name, err)
			}
			continue
		}
		if err != nil || string(data) != want {
			t.Errorf("overlay %s = %q, %v; want %q", name, data, err, want)
		}
	}
}
