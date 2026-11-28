package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type tartProcess struct {
	exited <-chan struct{}
	kill   func() error
}

var launchMacOSTart = func(t tart, name, logFile string, lock *os.File) (tartProcess, error) {
	c, err := t.startCommand(name, logFile, lock)
	if err != nil {
		return tartProcess{}, err
	}
	exited := make(chan struct{})
	go func() {
		_ = c.Wait()
		close(exited)
	}()
	return tartProcess{exited: exited, kill: c.Process.Kill}, nil
}

var stopMacOSStart = func(ctx context.Context, t tart, name string) error {
	c := exec.CommandContext(ctx, "tart", "stop", name, "--timeout", "30")
	c.Env = append(os.Environ(), t.env()...)
	c.WaitDelay = time.Second
	out, err := c.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "not running") {
		return fmt.Errorf("tart stop %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (e *env) startMacOSVM(o osDef, name string, timeout time.Duration) error {
	lock, err := lockMacOSStart(e.path("macos-boot.lock"))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := e.macosSlot(o); err != nil {
		return err
	}
	t := e.tart()
	logFile := filepath.Join(e.runDir(name), "tart.log")
	step("starting %s", name)
	process, err := launchMacOSTart(t, name, logFile, lock)
	if err != nil {
		return err
	}
	if waitErr := waitForTartStart(process.exited, logFile, timeout); waitErr != nil {
		return errors.Join(fmt.Errorf("%s: %w", name, waitErr), stopPendingMacOSStart(t, name, process))
	}
	// Tart inherits the lock so it stays held even if this command exits
	// before a failed launch has stopped. VNC confirms the VM now counts.
	return unix.Flock(int(lock.Fd()), unix.LOCK_UN)
}

func lockMacOSStart(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	retry := time.NewTicker(100 * time.Millisecond)
	defer retry.Stop()
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			_ = file.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		select {
		case <-retry.C:
		case <-timer.C:
			_ = file.Close()
			return nil, fmt.Errorf("%s is busy; retry after the other live command finishes", path)
		}
	}
}

func waitForTartStart(exited <-chan struct{}, logFile string, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	probe := time.NewTicker(250 * time.Millisecond)
	defer probe.Stop()
	// Polling tart list during boot can lock the ASIF disk.
	for {
		if _, _, err := tartVNC(logFile); err == nil {
			return nil
		}
		select {
		case <-exited:
			return errors.New("tart run exited before startup completed")
		case <-timer.C:
			return fmt.Errorf("did not start within %s", timeout)
		case <-probe.C:
		}
	}
}

func stopPendingMacOSStart(t tart, name string, process tartProcess) error {
	select {
	case <-process.exited:
		return nil
	default:
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	stopErr := stopMacOSStart(ctx, t, name)
	cancel()
	select {
	case <-process.exited:
		return stopErr
	default:
	}
	// Tart stop can report "not running" before tart run takes its VM lock.
	killErr := process.kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-process.exited:
		return errors.Join(stopErr, killErr)
	case <-timer.C:
		return errors.Join(stopErr, killErr, fmt.Errorf("tart run did not exit for %s and still holds the macOS boot lock", name))
	}
}
