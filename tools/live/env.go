package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// env holds the paths every command uses. Large or secret files (VM disks,
// ISOs, keys) live in home, outside the repository; bed/.state links to it.
type env struct {
	root string // repository root
	bed  string // live-tests
	home string // $TFSAZ_LIVE_HOME or ~/.tfsaz-live
}

func newEnv() (*env, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, errors.New("run from inside the TFSaz repository")
	}
	root := strings.TrimSpace(string(out))
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
	return &env{root: root, bed: filepath.Join(root, "live-tests"), home: home}, nil
}

func (e *env) path(parts ...string) string {
	return filepath.Join(append([]string{e.home}, parts...)...)
}

// ensureHome creates the state directory and the live-tests/.state link to it.
func (e *env) ensureHome() error {
	if err := os.MkdirAll(e.path("router", "peers"), 0o700); err != nil {
		return err
	}
	link := filepath.Join(e.bed, ".state")
	target, err := os.Readlink(link)
	switch {
	case err == nil && target == e.home:
		return nil
	case err == nil:
		if err := os.Remove(link); err != nil {
			return err
		}
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%s exists and is not a link; move it away", link)
	}
	return os.Symlink(e.home, link)
}

// requireMac stops commands that need Tart on hosts it does not support.
func requireMac() error {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return fmt.Errorf("live VMs need an Apple Silicon Mac for now (this is %s/%s)", runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

// osDef describes one guest OS. Only ready ones have a base definition in
// live-tests/vms/<name>; the others are planned with the provider shown.
type osDef struct {
	name     string
	provider string
	ready    bool
	user     string
}

var oses = []osDef{
	{name: "ubuntu", provider: "tart", ready: true, user: "tester"},
	{name: "windows", provider: "utm"},
	{name: "macos", provider: "tart"},
}

func lookupOS(name string) (osDef, error) {
	for _, o := range oses {
		if o.name != name {
			continue
		}
		if !o.ready {
			return o, fmt.Errorf("%s VMs (%s) are not set up yet", name, o.provider)
		}
		return o, nil
	}
	return osDef{}, fmt.Errorf("unknown OS %q; use ubuntu", name)
}

const prefix = "tfsaz-"

func (o osDef) base() string  { return prefix + o.name + "-base" }
func (o osDef) build() string { return prefix + o.name + "-build" }

// isClone reports whether a VM name is a clone of o's base.
func (o osDef) isClone(name string) bool {
	return strings.HasPrefix(name, prefix+o.name+"-") && name != o.base() && name != o.build()
}

// osOfVM returns the OS of a clone name such as tfsaz-ubuntu-a1b2c3.
func osOfVM(name string) (osDef, bool) {
	for _, o := range oses {
		if o.ready && o.isClone(name) {
			return o, true
		}
	}
	return osDef{}, false
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
