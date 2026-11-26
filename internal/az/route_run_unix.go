//go:build !windows

package az

import (
	"errors"
	"syscall"
)

// A reused PID only keeps a leftover overlay longer.
func routeRunProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
