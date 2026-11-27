package main

import (
	"fmt"
	"time"
)

// vmEntry is one VM a provider knows.
type vmEntry struct {
	Name    string
	State   string
	Running bool
	Size    float64 // GB on disk
}

// provider runs the VMs of one hypervisor: Tart for Ubuntu and macOS,
// QEMU for Windows.
type provider interface {
	list() ([]vmEntry, error)
	clone(base, name string) error
	start(name, logFile string) error
	// address is where SSH reaches the VM from the host; port 0 is 22.
	address(name string, wait time.Duration) (host string, port int, err error)
	stop(name string) error // without a shutdown
	remove(name string) error
	rename(from, to string) error
}

// vms adds what every provider needs on top of the interface.
type vms struct{ provider }

func (e *env) vms(o osDef) vms {
	if o.provider == "qemu" {
		return vms{e.qemu()}
	}
	return vms{e.tart()}
}

func (v vms) exists(name string) (bool, error) {
	list, err := v.list()
	if err != nil {
		return false, err
	}
	for _, vm := range list {
		if vm.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// clones lists the clones of o's base.
func (v vms) clones(o osDef) ([]string, error) {
	list, err := v.list()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, vm := range list {
		if o.isClone(vm.Name) {
			names = append(names, vm.Name)
		}
	}
	return names, nil
}

// waitStopped waits for a VM that shuts itself down; it never stops it.
func (v vms) waitStopped(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		list, err := v.list()
		if err != nil {
			return err
		}
		running := false
		for _, vm := range list {
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

// allVMs lists the VMs of every provider.
func (e *env) allVMs() ([]vmEntry, error) {
	tartVMs, err := e.tart().list()
	if err != nil {
		return nil, err
	}
	qemuVMs, err := e.qemu().list()
	return append(tartVMs, qemuVMs...), err
}
