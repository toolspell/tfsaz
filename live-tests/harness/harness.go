package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

type suite struct {
	config
	tool
	slots map[string]semaphore
}

var active *suite
var setupErr error

// RunTests is called by TestMain to build live once for the test process.
func RunTests(m *testing.M) int {
	if skipReason(os.LookupEnv) == "" {
		var dir string
		active, dir, setupErr = prepareSuite()
		if dir != "" {
			defer func() { _ = os.RemoveAll(dir) }()
		}
	}
	return m.Run()
}

func prepareSuite() (*suite, string, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nil, "", fmt.Errorf("live VMs require an Apple Silicon Mac (this is %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	gitCtx, cancelGit := context.WithTimeout(context.Background(), 15*time.Second)
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
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelBuild()
	build := tool{root: root, binary: "go"}.command(buildCtx, "build", "-o", l.binary, "./tools/live")
	if out, err := build.CombinedOutput(); err != nil {
		return nil, dir, fmt.Errorf("build tools/live: %w: %s", err, out)
	}
	s := &suite{config: cfg, tool: l, slots: make(map[string]semaphore)}
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

type Harness struct {
	suite       *suite
	ArtifactDir string
}

func Start(t *testing.T) *Harness {
	t.Helper()
	return liveSuite(t).start(t)
}

func (s *suite) start(t *testing.T) *Harness {
	t.Helper()
	var status struct {
		Running   []string `json:"running"`
		StateHome string   `json:"stateHome"`
	}
	if err := s.json(t.Context(), 30*time.Second, &status, "services", "status", "-json"); err != nil {
		t.Fatalf("check live services: %v\nStart them with go run ./tools/live services up", err)
	}
	for _, name := range []string{"devops-server-mock", "dns", "router"} {
		if !slices.Contains(status.Running, name) {
			t.Fatalf("live service %s is down. Run go run ./tools/live services up", name)
		}
	}
	root := s.artifacts
	if root == "" {
		if !filepath.IsAbs(status.StateHome) {
			t.Fatalf("live services status returned an invalid state home: %q", status.StateHome)
		}
		root = filepath.Join(status.StateHome, "artifacts")
	}
	dir, err := artifactDir(root, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live artifacts: %s", dir)
	return &Harness{suite: s, ArtifactDir: dir}
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
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	if err := slot.acquire(ctx); err != nil {
		t.Fatalf("wait for %s clone slot: %v", osName, err)
	}
	t.Cleanup(slot.release)

	var entropy [5]byte
	_, _ = rand.Read(entropy[:])
	suffix := hex.EncodeToString(entropy[:])
	name := "tfsaz-" + osName + "-" + suffix
	var created bool
	t.Cleanup(func() { h.cleanClone(t, name, created) })
	t.Logf("live clone: %s", name)

	vm := &VM{harness: h}
	err := s.json(t.Context(), 8*time.Minute, vm, "vm", "up", osName, "-name", suffix)
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
	if h.suite.keep && created {
		t.Logf("keeping clone %s. Remove it with go run ./tools/live vm down %s", name, name)
		if _, err := h.suite.run(ctx, 3*time.Minute, "vm", "stop", name); err != nil {
			t.Error(err)
		}
		return
	}
	if _, err := h.suite.run(ctx, 90*time.Second, "vm", "down", name); err != nil {
		t.Error(err)
	}
}
