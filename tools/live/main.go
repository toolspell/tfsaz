// Command live builds and runs the VMs and services of TFSaz's live tests.
// See live-tests/README.md.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	os.Exit(liveMain(os.Args[1:], os.Stdout, os.Stderr))
}

func liveMain(args []string, stdout, stderr io.Writer) int {
	help, args, showHelp, err := resolveHelp(args)
	call := liveInvocation{commandHelp: help}
	flags := call.flagSet()
	if err == nil {
		if showHelp {
			help.printHelp(stdout, flags)
			return 0
		}
		err = call.readArgs(args, flags)
	}
	if err != nil {
		message := strings.NewReplacer("\n", "\\n", "\r", "\\r").Replace(err.Error())
		_, _ = fmt.Fprintln(stderr, "live:", message)
		_, _ = fmt.Fprintln(stderr, usagePrefix+help.synopsis())
		return 2
	}

	err = run(call, stdout)
	var exit exitStatus
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit):
		return int(exit)
	default:
		_, _ = fmt.Fprintln(stderr, "live:", err)
		return 1
	}
}

// exitStatus passes on a guest command's exit code without a message.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func run(call liveInvocation, stdout io.Writer) error {
	ci := ciEnvironment()
	if ci != "" && !ciReadOnly(call) {
		return fmt.Errorf("live test changes are disabled when %s is set", ci)
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	command, action, _ := strings.Cut(call.path, " ")
	if usesInstance(command) {
		loadNetwork := true
		if command != "vm" || vmChangesState(action) {
			unlock, err := e.lockInstance(ciReadOnly(call))
			if err != nil {
				return err
			}
			loadNetwork = unlock != nil
			if unlock != nil {
				defer unlock()
			}
		}
		if loadNetwork {
			if err := e.loadNetwork(); err != nil {
				return err
			}
		}
	}
	switch command {
	case "doctor":
		return e.doctor(call.install || call.yes, ci != "")
	case "services":
		return e.services(action)
	case "mock":
		return e.mock(action, call.vm, call.asJSON, stdout)
	case "base":
		return e.base(action, call.args[0], call.force)
	case "vm":
		return e.vm(action, call)
	case "setup":
		return e.setup(call.args[0])
	case "verify":
		return e.verify(call.args[0])
	case "up":
		return e.up(call.args, stdout)
	case "clean", "down":
		return e.down()
	case "status":
		return e.status()
	}
	return fmt.Errorf("unknown command %q", command)
}

func ciEnvironment() string {
	for _, name := range []string{"CI", "GITHUB_ACTIONS"} {
		if _, set := os.LookupEnv(name); set {
			return name
		}
	}
	return ""
}

func ciReadOnly(call liveInvocation) bool {
	switch call.path {
	case "status", "services status", "mock ca", "mock requests", "vm list", "vm vnc":
		return true
	case "doctor":
		return !call.install && !call.yes
	}
	return false
}

func usesInstance(command string) bool {
	switch command {
	case "up", "down", "clean", "status", "setup", "verify", "services", "mock", "vm":
		return true
	}
	return false
}

func vmChangesState(action string) bool {
	switch action {
	case "up", "down", "start", "stop":
		return true
	}
	return false
}
