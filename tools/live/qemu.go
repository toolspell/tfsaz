package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// qemu runs Windows VMs with QEMU and Apple's Hypervisor.framework. Each VM
// is a directory under home: disk.qcow2, efivars.fd and, while it runs,
// qemu.pid, its SSH port and a QMP socket. A clone's disk is a qcow2 overlay
// on the base's disk.
type qemu struct{ home string }

func (e *env) qemu() qemu { return qemu{home: e.path("qemu")} }

func (q qemu) dir(name string) string { return filepath.Join(q.home, name) }

func (q qemu) list() ([]vmEntry, error) {
	entries, err := os.ReadDir(q.home)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var vms []vmEntry
	for _, d := range entries {
		disk, err := os.Stat(filepath.Join(q.dir(d.Name()), "disk.qcow2"))
		if err != nil {
			continue
		}
		vm := vmEntry{Name: d.Name(), State: "stopped", Size: float64(disk.Size()) / 1e9}
		if q.pid(d.Name()) != 0 {
			vm.State, vm.Running = "running", true
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

// pid returns the process ID of a running VM, or 0.
func (q qemu) pid(name string) int {
	data, err := os.ReadFile(filepath.Join(q.dir(name), "qemu.pid"))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	// A stale pid file can name another process by now.
	out, err := output(nil, "ps", "-p", strconv.Itoa(pid), "-o", "command=")
	if err != nil || !strings.Contains(out, "qemu-system") || !strings.Contains(out, q.dir(name)+string(filepath.Separator)) {
		return 0
	}
	return pid
}

// firmware returns QEMU's UEFI code and the empty variable store that
// every new VM gets a copy of.
func qemuFirmware() (code, vars string, err error) {
	bin, err := exec.LookPath("qemu-system-aarch64")
	if err != nil {
		return "", "", err
	}
	if bin, err = filepath.EvalSymlinks(bin); err != nil {
		return "", "", err
	}
	share := filepath.Join(filepath.Dir(bin), "..", "share", "qemu")
	code, vars = filepath.Join(share, "edk2-aarch64-code.fd"), filepath.Join(share, "edk2-arm-vars.fd")
	for _, f := range []string{code, vars} {
		if _, err := os.Stat(f); err != nil {
			return "", "", fmt.Errorf("QEMU's UEFI firmware is missing: %w", err)
		}
	}
	return code, vars, nil
}

func (q qemu) clone(base, name string) (err error) {
	dir := q.dir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	if _, err := output(nil, "qemu-img", "create", "-q", "-f", "qcow2", "-F", "qcow2",
		"-b", filepath.Join(q.dir(base), "disk.qcow2"), filepath.Join(dir, "disk.qcow2")); err != nil {
		return err
	}
	vars, err := os.ReadFile(filepath.Join(q.dir(base), "efivars.fd"))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "efivars.fd"), vars, 0o600)
}

// start boots a VM headless in the background with the hardware that
// live-tests/vms/windows/windows.pkr.hcl installed Windows on. SSH reaches
// it on a new loopback port each time.
func (q qemu) start(name, logFile string) error {
	dir := q.dir(name)
	code, _, err := qemuFirmware()
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	if err := os.WriteFile(filepath.Join(dir, "ssh-port"), []byte(strconv.Itoa(port)), 0o600); err != nil {
		return err
	}
	log, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	c := exec.Command("qemu-system-aarch64",
		"-name", name,
		"-machine", "virt,gic-version=max,accel=hvf", "-cpu", "host", "-smp", "4", "-m", "8G",
		"-drive", "if=pflash,format=raw,readonly=on,file="+code,
		"-drive", "if=pflash,format=raw,file="+filepath.Join(dir, "efivars.fd"),
		"-drive", "if=none,id=disk,format=qcow2,discard=unmap,file="+filepath.Join(dir, "disk.qcow2"),
		"-device", "nvme,drive=disk,serial=disk0",
		"-device", "qemu-xhci", "-device", "usb-kbd", "-device", "usb-tablet",
		"-device", "ramfb",
		// QEMU's own VNC server, for agents and people; its password comes
		// over QMP once QEMU runs.
		"-vnc", "127.0.0.1:0,to=99,password=on",
		"-device", "virtio-net-pci,netdev=net0",
		// Without IPv6 the NAT offers no second DNS server past the router.
		"-netdev", fmt.Sprintf("user,id=net0,ipv6=off,hostfwd=tcp:127.0.0.1:%d-:22", port),
		"-qmp", "unix:"+filepath.Join(dir, "qmp.sock")+",server=on,wait=off",
		"-display", "none", "-pidfile", filepath.Join(dir, "qemu.pid"),
	)
	// Not -daemonize, which sends QEMU's later errors to /dev/null.
	c.Stdout, c.Stderr = log, log
	detach(c)
	if err := c.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- c.Wait() }()
	for range 50 {
		select {
		case err := <-exited:
			return fmt.Errorf("qemu-system-aarch64 exited at start (%v); see %s", err, logFile)
		case <-time.After(200 * time.Millisecond):
		}
		if q.pid(name) != 0 {
			return q.setVNC(name)
		}
	}
	return fmt.Errorf("qemu-system-aarch64 did not start %s within 10s; see %s", name, logFile)
}

// setVNC gives QEMU's VNC server a new password and saves its address for
// vm vnc. VNC passwords have at most 8 characters.
func (q qemu) setVNC(name string) error {
	s, err := dialQMP(filepath.Join(q.dir(name), "qmp.sock"))
	if err != nil {
		return err
	}
	defer s.close()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	password := hex.EncodeToString(b)
	if _, err := s.execute("set_password", map[string]string{"protocol": "vnc", "password": password}); err != nil {
		return err
	}
	data, err := s.execute("query-vnc", nil)
	if err != nil {
		return err
	}
	var vnc struct{ Host, Service string }
	if err := json.Unmarshal(data, &vnc); err != nil {
		return err
	}
	address := "vnc://:" + password + "@" + net.JoinHostPort(vnc.Host, vnc.Service)
	return os.WriteFile(filepath.Join(q.dir(name), "vnc"), []byte(address+"\n"), 0o600)
}

func (q qemu) vnc(name, _ string) (string, error) {
	if q.pid(name) == 0 {
		return "", fmt.Errorf("%s does not run", name)
	}
	data, err := os.ReadFile(filepath.Join(q.dir(name), "vnc"))
	return strings.TrimSpace(string(data)), err
}

func (q qemu) address(name string, _ time.Duration) (string, int, error) {
	if q.pid(name) == 0 {
		return "", 0, fmt.Errorf("%s does not run", name)
	}
	data, err := os.ReadFile(filepath.Join(q.dir(name), "ssh-port"))
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return "127.0.0.1", port, err
}

// stop ends QEMU without a shutdown of the guest.
func (q qemu) stop(name string) error {
	pid := q.pid(name)
	if pid == 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	for range 30 {
		if q.pid(name) == 0 {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("QEMU of %s (pid %d) still runs 30s after SIGTERM", name, pid)
}

func (q qemu) remove(name string) error {
	if err := q.stop(name); err != nil {
		return err
	}
	return os.RemoveAll(q.dir(name))
}

func (q qemu) screenshot(name, _, file string) error {
	return qmpScreendump(filepath.Join(q.dir(name), "qmp.sock"), file)
}

func (q qemu) rename(from, to string) error {
	return os.Rename(q.dir(from), q.dir(to))
}
