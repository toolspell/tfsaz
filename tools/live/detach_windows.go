package main

import "os/exec"

// detach is a no-op: VMs run only on an Apple Silicon Mac for now, and
// requireMac stops every command that would start one.
func detach(*exec.Cmd) {}
