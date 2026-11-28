package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestMacOSBoot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T)
	}{
		{"new VNC announcement", checkMacOSBootWaitsForTart},
		{"startup timeout", checkMacOSBootTimeout},
		{"lock inherited by Tart", checkMacOSBootLockInheritance},
	} {
		t.Run(tc.name, tc.check)
	}
}

func checkMacOSBootWaitsForTart(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	o := oses[1]
	name := e.cloneName(o, "up")
	if err := os.MkdirAll(e.tart().home, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(e.runDir(name), "tart.log")
	putLiveFile(t, logFile)
	if err := os.WriteFile(logFile, []byte("VNC server is running at vnc://:old@127.0.0.1:5000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldOutput, oldLaunch := output, launchMacOSTart
	t.Cleanup(func() { output, launchMacOSTart = oldOutput, oldLaunch })
	listed := false
	output = func(_ []string, program string, args ...string) (string, error) {
		if program == "tart" && strings.Join(args, " ") == "list --source local --format json" && !listed {
			listed = true
			if log := readLiveFile(t, logFile); !strings.Contains(log, "127.0.0.1:5000") {
				t.Fatal("Tart list ran after the old startup log was replaced")
			}
			return "[]", nil
		}
		t.Fatalf("unexpected command: %s %q", program, args)
		return "", nil
	}
	launchMacOSTart = func(_ tart, _ string, logFile string, _ *os.File) (tartProcess, error) {
		err := os.WriteFile(logFile, []byte("VNC server is running at vnc://:new@127.0.0.1:5001\n"), 0o600)
		return tartProcess{exited: make(chan struct{})}, err
	}
	if err := e.startVMProcess(o, name); err != nil {
		t.Fatal(err)
	}
	if !listed {
		t.Fatal("Tart list was not called for the capacity check")
	}
	if log := readLiveFile(t, logFile); !strings.Contains(log, "127.0.0.1:5001") || strings.Contains(log, "127.0.0.1:5000") {
		t.Error("startup log did not contain only the new VNC announcement")
	}
}

func checkMacOSBootTimeout(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	o := oses[1]
	name := e.cloneName(o, "up")
	logFile := filepath.Join(e.runDir(name), "tart.log")
	putLiveFile(t, logFile)
	oldLaunch, oldStop := launchMacOSTart, stopMacOSStart
	t.Cleanup(func() { launchMacOSTart, stopMacOSStart = oldLaunch, oldStop })
	exited := make(chan struct{})
	killing := make(chan struct{})
	stopped := false
	stopMacOSStart = func(ctx context.Context, _ tart, vm string) error {
		_, bounded := ctx.Deadline()
		if vm != name || !bounded {
			t.Errorf("stop called for %s with bounded context = %t", vm, bounded)
		}
		stopped = true
		return nil // Tart has yet to take its VM lock and reports "not running".
	}
	launchMacOSTart = func(_ tart, _ string, _ string, _ *os.File) (tartProcess, error) {
		return tartProcess{exited: exited, kill: func() error {
			if !stopped {
				t.Error("Tart was killed before trying to stop the VM")
			}
			close(killing)
			return nil
		}}, nil
	}

	result := make(chan error, 1)
	go func() { result <- e.startMacOSVM(o, name, 0) }()
	probe := flock.New(e.path("macos-boot.lock"))
	defer func() { _ = probe.Close() }()
	select {
	case err := <-result:
		t.Fatalf("startup returned before terminating pending Tart: %v", err)
	case <-time.After(5 * time.Second):
		close(exited)
		select {
		case err := <-result:
			t.Logf("startup returned after test cleanup: %v", err)
		case <-time.After(5 * time.Second):
		}
		t.Fatal("startup did not terminate pending Tart")
	case <-killing:
	}
	if ok, err := probe.TryLock(); ok || err != nil {
		t.Errorf("boot lock while Tart exits: TryLock = %t, %v; want contention", ok, err)
	}
	close(exited)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "did not start within") {
			t.Errorf("startup error = %v; want timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not return after Tart exited")
	}
	if ok, err := probe.TryLock(); !ok || err != nil {
		t.Errorf("boot lock after Tart exit: TryLock = %t, %v; want acquired", ok, err)
	}
}

func checkMacOSBootLockInheritance(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tart"), []byte("#!/bin/sh\nexec /bin/sleep 60\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	dir := t.TempDir()
	path := filepath.Join(dir, "macos-boot.lock")
	lock, err := lockMacOSStart(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	process, err := launchMacOSTart(tart{home: dir}, "pending", filepath.Join(dir, "tart.log"), lock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.kill() })
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	probe := flock.New(path)
	defer func() { _ = probe.Close() }()
	if ok, err := probe.TryLock(); ok || err != nil {
		t.Fatalf("lock after launcher closes it: TryLock = %t, %v; want contention", ok, err)
	}
	if err := process.kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("pending process did not exit")
	}
	if ok, err := probe.TryLock(); !ok || err != nil {
		t.Errorf("lock after pending process exits: TryLock = %t, %v; want acquired", ok, err)
	}
}
