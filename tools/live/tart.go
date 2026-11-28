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

func (t tart) list() ([]vmEntry, error) {
	out, err := t.run("list", "--source", "local", "--format", "json")
	if err != nil {
		return nil, err
	}
	var vms []vmEntry
	if err := json.Unmarshal([]byte(out), &vms); err != nil {
		return nil, fmt.Errorf("read tart list: %w", err)
	}
	return vms, nil
}

// macosSlot refuses to start another macOS VM when two run already:
// Apple's license and Virtualization.framework allow two per host.
func (e *env) macosSlot(o osDef) error {
	if o.macos == 0 {
		return nil
	}
	vms, err := e.tart().list()
	if err != nil {
		return err
	}
	var running []string
	for _, vm := range vms {
		if vm.Running && strings.HasPrefix(vm.Name, prefix+o.name+"-") {
			running = append(running, vm.Name)
		}
	}
	if len(running) >= 2 {
		return fmt.Errorf("macOS allows two macOS VMs at a time; running: %s", strings.Join(running, ", "))
	}
	return nil
}

// start boots a VM headless in the background; Tart's output goes to logFile.
// The VM keeps running after this process exits.
func (t tart) start(name, logFile string) error {
	log, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	// Virtualization.framework's VNC server serves screenshots; with
	// --no-graphics Tart only prints its address instead of opening it.
	c := exec.Command("tart", "run", "--no-graphics", "--vnc-experimental", name)
	c.Env = append(os.Environ(), t.env()...)
	c.Stdout, c.Stderr = log, log
	detach(c)
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

func (t tart) address(name string, wait time.Duration) (string, int, error) {
	ip, err := t.run("ip", name, "--wait", strconv.Itoa(int(wait.Seconds())))
	return ip, 0, err
}

func (t tart) clone(base, name string) error {
	if _, err := t.run("clone", base, name); err != nil {
		return err
	}
	_, err := t.run("set", name, "--display", fmt.Sprintf("%dx%dpx", displayWidth, displayHeight))
	return err
}

func (t tart) vnc(_, logFile string) (string, error) {
	address, password, err := tartVNC(logFile)
	if err != nil {
		return "", err
	}
	return "vnc://:" + password + "@" + address, nil
}

func (t tart) screenshot(_, logFile, file string) error {
	address, password, err := tartVNC(logFile)
	if err != nil {
		return err
	}
	return vncScreenshot(address, password, file)
}

func (t tart) rename(from, to string) error {
	_, err := t.run("rename", from, to)
	return err
}

// stop stops a running VM and waits until Tart reports it stopped.
func (t tart) stop(name string) error {
	_, err := t.run("stop", name, "--timeout", "30")
	if err != nil && !strings.Contains(err.Error(), "not running") {
		return err
	}
	return nil
}

// remove stops and deletes a VM; a VM that does not exist is not an error.
func (t tart) remove(name string) error {
	ok, err := vms{t}.exists(name)
	if err != nil || !ok {
		return err
	}
	if err := t.stop(name); err != nil {
		return err
	}
	_, err = t.run("delete", name)
	return err
}
