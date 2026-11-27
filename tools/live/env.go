package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type env struct {
	root     string
	bed      string
	home     string
	instance string
	network  *bedNetwork
}

func newEnv() (*env, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, errors.New("run from inside the TFSaz repository")
	}
	root, err := filepath.EvalSymlinks(strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "\r"))
	if err != nil {
		return nil, err
	}
	instance, err := instanceID(root, os.Getenv("TFSAZ_LIVE_INSTANCE"))
	if err != nil {
		return nil, err
	}
	home := os.Getenv("TFSAZ_LIVE_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".tfsaz-live")
	}
	if !filepath.IsAbs(home) {
		return nil, errors.New("TFSAZ_LIVE_HOME must be an absolute path")
	}
	return &env{root: root, bed: filepath.Join(root, "live-tests"), home: home, instance: instance}, nil
}

func (e *env) path(parts ...string) string {
	return filepath.Join(append([]string{e.home}, parts...)...)
}

func (e *env) instancePath(parts ...string) string {
	return e.path(append([]string{"instances", e.instance}, parts...)...)
}

// requireMac stops commands that need Tart on hosts it does not support.
func requireMac() error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return fmt.Errorf("live VMs need an Apple Silicon Mac for now (this is %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

// osDef describes one guest OS; its base definition is in live-tests/vms/<name>.
type osDef struct {
	name           string
	provider       string // tart or qemu
	user           string
	guest          guest
	macos          int // for a macOS guest: its major version, which the host needs at least
	cloneMemoryMiB int // applied by Tart when it clones the base
}

const cloneCPUs = 2

var oses = []osDef{
	{name: "ubuntu", provider: "tart", user: "tester", guest: ubuntuGuest, cloneMemoryMiB: 4096},
	{name: "macos", provider: "tart", user: "admin", guest: macosGuest, macos: 27, cloneMemoryMiB: 6144},
	{name: "windows", provider: "qemu", user: "tester", guest: windowsGuest},
}

func guestOS(name string) (osDef, bool) {
	for _, o := range oses {
		if o.name == name {
			return o, true
		}
	}
	return osDef{}, false
}

func lookupOS(name string) (osDef, error) {
	o, ok := guestOS(name)
	if !ok {
		return o, unknownOS(name)
	}
	if o.macos > 0 {
		// Virtualization.framework runs no macOS guest newer than its host.
		out, err := exec.Command("sw_vers", "-productVersion").Output()
		major, _ := strconv.Atoi(strings.SplitN(strings.TrimSpace(string(out)), ".", 2)[0])
		if err != nil || major < o.macos {
			return o, fmt.Errorf("macOS %d guests need a macOS %d host or newer", o.macos, o.macos)
		}
	}
	return o, nil
}

func unknownOS(name string) error {
	return fmt.Errorf("unknown OS %q; use ubuntu, macos or windows", name)
}

const prefix = "tfsaz-"

func (o osDef) base() string  { return prefix + o.name + "-base" }
func (o osDef) build() string { return prefix + o.name + "-build" }

func (o osDef) isClone(name string) bool {
	if _, cloneOS, ok := parseCloneName(name); ok {
		return cloneOS.name == o.name
	}
	// Legacy clones still depend on the shared bases until they are removed.
	suffix, ok := strings.CutPrefix(name, prefix+o.name+"-")
	return ok && validSuffix(suffix)
}

func (e *env) osOfVM(name string) (osDef, bool) {
	id, o, ok := parseCloneName(name)
	return o, ok && id == e.instance
}

// cmd runs a command with output passed through, for long steps whose
// progress the caller should see.
func cmd(dir string, extraEnv []string, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Env = append(os.Environ(), extraEnv...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stderr, os.Stderr
	return c.Run()
}

// output runs a command and returns its trimmed stdout; errors carry stderr.
func output(extraEnv []string, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func step(format string, args ...any) { fmt.Fprintf(os.Stderr, "==> "+format+"\n", args...) }
