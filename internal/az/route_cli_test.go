package az

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
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
		{"sink", "--port", "not-a-port"}, {"verify-hosts", "--unknown"},
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
					conn.SetReadDeadline(time.Now().Add(2 * time.Second))
					var b [1]byte
					n, err := conn.Read(b[:])
					conn.Close()
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
				resp.Body.Close()
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
