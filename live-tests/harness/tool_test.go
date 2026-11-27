package harness

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTool(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go unavailable")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-live")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := tool{binary: "go"}.command(ctx, "build", "-o", bin, "./testdata/fake-live.go")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake live: %v: %s", err, out)
	}
	s := &suite{tool: tool{root: dir, binary: bin}}
	h := &Harness{suite: s, ArtifactDir: dir}
	vm := &VM{Name: "tfsaz-ubuntu-test", harness: h}

	t.Run("artifact directory uses tool state home", func(t *testing.T) {
		h := s.start(t)
		if got, want := filepath.Dir(h.ArtifactDir), filepath.Join(dir, "artifacts"); got != want {
			t.Fatalf("artifact root = %q (want %q)", got, want)
		}
	})
	t.Run("clone info", func(t *testing.T) {
		var info VM
		err := s.json(t.Context(), 5*time.Second, &info, "vm", "up", "ubuntu")
		if err != nil || info.Name != vm.Name || info.OS != "ubuntu" || info.BedAddress != "192.0.2.17" {
			t.Fatalf("clone info = %+v, %v", info, err)
		}
	})
	t.Run("guest exit and streams", func(t *testing.T) {
		script := "printf '%s\\n' 'hello world'; exit 7"
		got := vm.Run(t, script)
		want := Result{ExitCode: 7, Stdout: "hello world\n", Stderr: "guest warning\n", Seconds: 0.25}
		if got != want {
			t.Fatalf("guest result = %+v; want %+v", got, want)
		}
		got = vm.RunGUI(t, script)
		want = Result{ExitCode: 124, TimedOut: true, Seconds: 120}
		if got != want {
			t.Fatalf("GUI guest result = %+v; want %+v", got, want)
		}
	})
	t.Run("request journal", func(t *testing.T) {
		rows := h.Requests(t, vm)
		if len(rows) != 1 || rows[0].Time.IsZero() || rows[0].ClientIP != "192.0.2.17" ||
			rows[0].Method != "GET" || rows[0].Path != "/" || rows[0].Status != 200 {
			t.Fatalf("request journal = %+v", rows)
		}
		h.ResetJournal(t)
	})
	t.Run("malformed JSON", func(t *testing.T) {
		if err := s.json(t.Context(), 5*time.Second, new(Result), "bad-json"); err == nil || !strings.Contains(err.Error(), "read JSON") {
			t.Fatalf("expected a JSON decode error, got %v", err)
		}
	})
	t.Run("tool failure", func(t *testing.T) {
		_, err := s.run(t.Context(), 5*time.Second, "failed", "--", "guest script")
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(err.Error(), "tool error") ||
			strings.Contains(err.Error(), "guest script") {
			t.Fatalf("expected the tool exit and diagnostic, got %v", err)
		}
	})
	t.Run("forward cleanup", func(t *testing.T) {
		var addr string
		if !t.Run("forward", func(t *testing.T) { addr = vm.forward(t, "9222") }) {
			return
		}
		conn, err := net.DialTimeout("tcp4", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			t.Fatal("forward still listens after cleanup")
		}
	})
}
