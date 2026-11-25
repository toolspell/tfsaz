//go:build unix

package az

import (
	"os/exec"
	"syscall"
)

func detachPreserveSSHProbe(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
