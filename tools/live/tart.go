package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// tart drives the Tart CLI with its own TART_HOME, so the live-test VMs
// never mix with the user's other Tart VMs.
type tart struct{ home string }

func (e *env) tart() tart { return tart{home: e.path("tart")} }

func (t tart) env() []string { return []string{"TART_HOME=" + t.home} }

func (t tart) run(args ...string) (string, error) { return output(t.env(), "tart", args...) }

type tartVM struct {
	Name    string
	State   string
	Running bool
	Size    float64
}

func (t tart) list() ([]tartVM, error) {
	out, err := t.run("list", "--source", "local", "--format", "json")
	if err != nil {
		return nil, err
	}
	var vms []tartVM
	if err := json.Unmarshal([]byte(out), &vms); err != nil {
		return nil, fmt.Errorf("read tart list: %w", err)
	}
	return vms, nil
}

func (t tart) exists(name string) (bool, error) {
	vms, err := t.list()
	if err != nil {
		return false, err
	}
	for _, vm := range vms {
		if vm.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// clones lists the clones of o's base.
func (t tart) clones(o osDef) ([]string, error) {
	vms, err := t.list()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, vm := range vms {
		if o.isClone(vm.Name) {
			names = append(names, vm.Name)
		}
	}
	return names, nil
}

// start boots a VM headless in the background; Tart's output goes to logFile.
// The VM keeps running after this process exits.
func (t tart) start(name, logFile string) error {
	log, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	c := exec.Command("tart", "run", "--no-graphics", name)
	c.Env = append(os.Environ(), t.env()...)
	c.Stdout, c.Stderr = log, log
	detach(c)
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

func (t tart) ip(name string, wait time.Duration) (string, error) {
	return t.run("ip", name, "--wait", strconv.Itoa(int(wait.Seconds())))
}

// stop stops a running VM and waits until Tart reports it stopped.
func (t tart) stop(name string) error {
	_, err := t.run("stop", name, "--timeout", "30")
	if err != nil && !strings.Contains(err.Error(), "not running") {
		return err
	}
	return nil
}

// waitStopped waits for a VM that shuts itself down; it never stops it.
func (t tart) waitStopped(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		vms, err := t.list()
		if err != nil {
			return err
		}
		running := false
		for _, vm := range vms {
			if vm.Name == name && vm.Running {
				running = true
			}
		}
		if !running {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("%s still runs %s after its shutdown began", name, timeout)
}

// remove stops and deletes a VM; a VM that does not exist is not an error.
func (t tart) remove(name string) error {
	ok, err := t.exists(name)
	if err != nil || !ok {
		return err
	}
	if err := t.stop(name); err != nil {
		return err
	}
	_, err = t.run("delete", name)
	return err
}
