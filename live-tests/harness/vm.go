package harness

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type Result struct {
	ExitCode int     `json:"exitCode"`
	Stdout   string  `json:"stdout"`
	Stderr   string  `json:"stderr"`
	TimedOut bool    `json:"timedOut"`
	Seconds  float64 `json:"seconds"`
}

func (v *VM) Run(t *testing.T, script string) Result {
	t.Helper()
	return v.run(t, script, false)
}

func (v *VM) RunGUI(t *testing.T, script string) Result {
	t.Helper()
	return v.run(t, script, true)
}

func (v *VM) run(t *testing.T, script string, gui bool) Result {
	t.Helper()
	const guestTimeout = 2 * time.Minute
	args := []string{"vm", "run", v.Name, "-json", "-timeout", guestTimeout.String()}
	if gui {
		args = append(args, "-gui")
	}
	args = append(args, "--", script)
	var result *Result
	if err := v.harness.suite.json(v.harness.ctx, guestTimeout+time.Minute, &result, args...); err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("live vm run returned null instead of a command result")
	}
	return *result
}

func (v *VM) Push(t *testing.T, local, remote string) {
	t.Helper()
	if _, err := v.harness.suite.run(v.harness.ctx, 2*time.Minute, "vm", "push", v.Name, local, remote); err != nil {
		t.Fatal(err)
	}
}

func (v *VM) Pull(t *testing.T, remote, local string) {
	t.Helper()
	if _, err := v.harness.suite.run(v.harness.ctx, 2*time.Minute, "vm", "pull", v.Name, remote, local); err != nil {
		t.Fatal(err)
	}
}

func (v *VM) Screenshot(t *testing.T, name string) string {
	t.Helper()
	if !filepath.IsLocal(name) || filepath.Base(name) != name || name == "." {
		t.Fatalf("screenshot name must be a filename: %q", name)
	}
	path := filepath.Join(v.harness.ArtifactDir, name)
	if err := v.harness.screenshot(v.harness.ctx, v.Name, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func (h *Harness) screenshot(ctx context.Context, clone, path string) error {
	_, err := h.suite.run(ctx, 45*time.Second, "vm", "screenshot", clone, path)
	return err
}

func (v *VM) InstallT3(t *testing.T) {
	t.Helper()
	out, err := v.harness.suite.run(v.harness.ctx, 5*time.Minute, "vm", "t3", "install", v.Name)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("installed T3 Code %s in %s", strings.TrimSpace(string(out)), v.Name)
}

func (v *VM) StartT3(t *testing.T) string {
	t.Helper()
	const guestPort = "9222"
	if _, err := v.harness.suite.run(v.harness.ctx, 3*time.Minute, "vm", "t3", "start", v.Name, "-port", guestPort); err != nil {
		t.Fatal(err)
	}
	return "http://" + v.forward(t, guestPort)
}

func (v *VM) forward(t *testing.T, guestPort string) string {
	t.Helper()
	// SSH needs a port number, so release the OS-assigned port before handing it over.
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(v.harness.ctx)
	cmd := v.harness.suite.command(ctx, "vm", "forward", v.Name, port+":"+guestPort)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("port forward for %s did not stop", v.Name)
		}
	})

	probe, stop := context.WithTimeout(v.harness.ctx, 15*time.Second)
	defer stop()
	dialer := net.Dialer{Timeout: time.Second}
	// Polling is the only option: ssh -L gives no readiness event when it binds the port.
	for {
		select {
		case <-done:
			t.Fatalf("port forward for %s exited: %v: %s", v.Name, waitErr, stderr.String())
		default:
		}
		conn, err := dialer.DialContext(probe, "tcp4", addr)
		if err == nil {
			_ = conn.Close()
			return addr
		}
		select {
		case <-done:
			t.Fatalf("port forward for %s exited: %v: %s", v.Name, waitErr, stderr.String())
		case <-probe.Done():
			t.Fatalf("wait for port forward for %s: %v", v.Name, err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type Request struct {
	Time       time.Time `json:"time"`
	ClientIP   string    `json:"clientIP"`
	Method     string    `json:"method"`
	Scheme     string    `json:"scheme"`
	Host       string    `json:"host"`
	Path       string    `json:"path"`
	Query      string    `json:"query"`
	Status     int       `json:"status"`
	UserAgent  string    `json:"userAgent"`
	AuthScheme string    `json:"authScheme"`
}

func (h *Harness) Requests(t *testing.T, vm *VM) []Request {
	t.Helper()
	var rows []Request
	if err := h.suite.json(h.ctx, 15*time.Second, &rows, "mock", "requests", "-vm", vm.Name, "-json"); err != nil {
		t.Fatal(err)
	}
	return rows
}

func (h *Harness) ResetJournal(t *testing.T) {
	t.Helper()
	if _, err := h.suite.run(h.ctx, 15*time.Second, "mock", "reset"); err != nil {
		t.Fatal(fmt.Errorf("reset the shared request journal: %w", err))
	}
}
