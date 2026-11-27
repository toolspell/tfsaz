//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach puts the child in its own session so it outlives this process.
func detach(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
