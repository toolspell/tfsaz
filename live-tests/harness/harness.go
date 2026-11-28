package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type suite struct {
	config
	tool
	ctx   context.Context
	slots map[string]semaphore
	// The live tool holds the instance lock during clone lifecycle commands.
	cloneMu sync.Mutex
}

var active *suite
var setupErr error

// RunTests is called by TestMain to build live once for the test process.
func RunTests(m *testing.M) int {
	if skipReason(os.LookupEnv) == "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-ctx.Done()
			stop()
		}()
		var dir string
		active, dir, setupErr = prepareSuite(ctx)
		if dir != "" {
			defer func() { _ = os.RemoveAll(dir) }()
		}
	}
	return m.Run()
}

func prepareSuite(ctx context.Context) (*suite, string, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nil, "", fmt.Errorf("live VMs require an Apple Silicon Mac (this is %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	gitCtx, cancelGit := context.WithTimeout(ctx, 15*time.Second)
	defer cancelGit()
	out, err := exec.CommandContext(gitCtx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, "", fmt.Errorf("locate the TFSaz repository: %w", err)
	}
	root := strings.TrimSpace(string(out))
	cfg, err := readConfig(os.LookupEnv, root)
	if err != nil {
		return nil, "", err
	}
	dir, err := os.MkdirTemp("", "tfsaz-live-harness-")
	if err != nil {
		return nil, "", err
	}
	l := tool{root: root, binary: filepath.Join(dir, "live")}
	buildCtx, cancelBuild := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelBuild()
	build := tool{root: root, binary: "go"}.command(buildCtx, "build", "-o", l.binary, "./tools/live")
	if out, err := build.CombinedOutput(); err != nil {
		return nil, dir, fmt.Errorf("build tools/live: %w: %s", err, out)
	}
	s := &suite{config: cfg, tool: l, ctx: ctx, slots: make(map[string]semaphore)}
	for _, osName := range cfg.platforms {
		s.slots[osName] = make(semaphore, cfg.limits[osName])
	}
	return s, dir, nil
}

func liveSuite(t *testing.T) *suite {
	t.Helper()
	if reason := skipReason(os.LookupEnv); reason != "" {
		t.Skip(reason)
	}
	if setupErr != nil {
		t.Fatal(setupErr)
	}
	if active == nil {
		t.Fatal("call harness.RunTests from TestMain")
	}
	return active
}

func Platforms(t *testing.T) []string {
	t.Helper()
	return slices.Clone(liveSuite(t).platforms)
}

type HostPorts struct {
	HTTP      int `json:"http"`
	HTTPS     int `json:"https"`
	SSH       int `json:"ssh"`
	Journal   int `json:"journal"`
	WireGuard int `json:"wireguard"`
}

type Harness struct {
	suite       *suite
	ctx         context.Context
	instance    string
	Ports       HostPorts
	ArtifactDir string
}

func Start(t *testing.T) *Harness {
	t.Helper()
	return liveSuite(t).start(t)
}

func (s *suite) start(t *testing.T) *Harness {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	stop := context.AfterFunc(s.ctx, cancel)
	t.Cleanup(func() {
		stop()
		cancel()
	})
	var status struct {
		Instance  string     `json:"instance"`
		Ports     *HostPorts `json:"ports"`
		Running   []string   `json:"running"`
		StateHome string     `json:"stateHome"`
	}
	if err := s.json(ctx, 30*time.Second, &status, "services", "status", "-json"); err != nil {
		t.Fatalf("check live services: %v\nStart them with go run ./tools/live up", err)
	}
	if status.Ports == nil {
		t.Fatalf("live instance %s has no slot. Run go run ./tools/live up", status.Instance)
	}
	if status.Instance == "" || len(status.Instance) > 16 || strings.Trim(status.Instance, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		t.Fatalf("live services status returned an invalid instance ID: %q", status.Instance)
	}
	for _, name := range []string{"devops-server-mock", "dns", "router"} {
		if !slices.Contains(status.Running, name) {
			t.Fatalf("live service %s is down in instance %s. Run go run ./tools/live up", name, status.Instance)
		}
	}
	root := s.artifacts
	if root == "" {
		if !filepath.IsAbs(status.StateHome) {
			t.Fatalf("live services status returned an invalid state home: %q", status.StateHome)
		}
		root = filepath.Join(status.StateHome, "artifacts", status.Instance)
	}
	dir, err := artifactDir(root, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live artifacts: %s", dir)
	t.Cleanup(func() {
		if !t.Failed() {
			_ = os.Remove(dir)
		}
	})
	return &Harness{suite: s, ctx: ctx, instance: status.Instance, Ports: *status.Ports, ArtifactDir: dir}
}

func artifactDir(root, testName string) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	name := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-").Replace(testName)
	return os.MkdirTemp(root, name+"-")
}

type VM struct {
	Name       string `json:"name"`
	OS         string `json:"os"`
	BedAddress string `json:"bedAddress"`
	harness    *Harness
}

func (h *Harness) Clone(t *testing.T, osName string) *VM {
	t.Helper()
	s := h.suite
	slot, ok := s.slots[osName]
	if !ok {
		if _, known := s.limits[osName]; !known {
			t.Fatalf("unknown VM OS %q", osName)
		}
		t.Skipf("%s is excluded by %s", osName, osEnv)
	}
	if err := slot.acquire(h.ctx); err != nil {
		t.Fatalf("wait for %s clone slot: %v", osName, err)
	}
	t.Cleanup(slot.release)

	var entropy [5]byte
	_, _ = rand.Read(entropy[:])
	suffix := hex.EncodeToString(entropy[:])
	name := "tfsaz-live-" + h.instance + "-" + osName + "-" + suffix
	var created bool
	t.Cleanup(func() { h.cleanClone(t, name, created) })
	t.Logf("live clone: %s", name)

	vm := &VM{harness: h}
	s.cloneMu.Lock()
	err := s.json(h.ctx, 8*time.Minute, vm, "vm", "up", osName, "-name", suffix)
	s.cloneMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if vm.Name != name || vm.OS != osName || vm.BedAddress == "" {
		t.Fatalf("unexpected clone details: name=%q OS=%q bedAddress=%q", vm.Name, vm.OS, vm.BedAddress)
	}
	created = true
	return vm
}

func (h *Harness) cleanClone(t *testing.T, name string, created bool) {
	t.Helper()
	ctx := context.Background()
	if created && t.Failed() {
		if err := h.screenshot(ctx, name, filepath.Join(h.ArtifactDir, name+"-failure.png")); err != nil {
			t.Logf("capture failure screenshot: %v", err)
		}
		out, err := h.suite.run(ctx, 15*time.Second, "mock", "requests", "-vm", name, "-json")
		if err == nil {
			err = os.WriteFile(filepath.Join(h.ArtifactDir, name+"-requests.json"), out, 0o600)
		}
		if err != nil {
			t.Logf("save request journal: %v", err)
		}
	}
	h.suite.cloneMu.Lock()
	defer h.suite.cloneMu.Unlock()
	if h.suite.keep && created {
		t.Logf("keeping clone %s. Remove it with go run ./tools/live vm down %s", name, name)
		if _, err := h.suite.run(ctx, 3*time.Minute, "vm", "stop", name); err != nil {
			// A guest that no longer answers SSH keeps running after a graceful stop fails.
			if _, forced := h.suite.run(ctx, time.Minute, "vm", "stop", name, "-force"); forced != nil {
				t.Error(errors.Join(err, forced))
			}
		}
		return
	}
	if _, err := h.suite.run(ctx, 90*time.Second, "vm", "down", name); err != nil {
		t.Error(err)
	}
}
