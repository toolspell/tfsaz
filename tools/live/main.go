// Command live builds and runs the VMs and services of TFSaz's live tests.
// Agents can drive every command; none prompts unless it runs in a terminal
// without -yes. See live-tests/README.md.
//
//	go run ./tools/live setup OS                   install, start and build what is missing, then verify
//	go run ./tools/live verify OS                  check a throwaway clone against the test bed
//	go run ./tools/live doctor [-install]          check the host tools; -install adds missing ones
//	go run ./tools/live services up|down|status    mock, DNS sinkhole and VM router in Docker
//	go run ./tools/live base build OS [-force]     build the base VM from scratch
//	go run ./tools/live base update OS             upgrade the base VM's packages in place
//	go run ./tools/live base rm OS                 delete the base VM
//	go run ./tools/live vm up OS [-name SUFFIX]    clone the base, boot it, join the router; prints JSON
//	go run ./tools/live vm ssh NAME [-- CMD...]    open a shell or run a command in a clone
//	go run ./tools/live vm down NAME               stop and delete a clone
//	go run ./tools/live vm list [-json]
//	go run ./tools/live clean                      delete every clone and its router peer
//	go run ./tools/live status
//
// base commands refuse while clones of that base exist.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

const usage = `usage: go run ./tools/live <command>

  setup OS                        install host tools, start services, build the base if missing, verify
  verify OS                       boot a throwaway clone and check the mock, the router and internet
  doctor [-install] [-yes]        check host tools; offer to install missing ones with Homebrew
  services up|down|status         the test bed in Docker: mock, DNS sinkhole, VM router
  base build OS [-force]          build OS's base VM from scratch (-force replaces an existing one)
  base update OS                  upgrade the base VM's packages in place
  base rm OS                      delete the base VM
  vm up OS [-name SUFFIX]         clone the base, boot it, join the router; prints JSON
  vm ssh NAME [-- CMD...]         open a shell or run a command in a clone
  vm down NAME                    stop and delete a clone
  vm list [-json]                 list clones
  clean                           delete every clone and its router peer
  status                          services, base VMs and clones

OS: ubuntu (windows and macos are planned). base commands refuse while clones exist.
State lives in $TFSAZ_LIVE_HOME (default ~/.tfsaz-live), linked as live-tests/.state.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	err := run(os.Args[1], os.Args[2:])
	var exit exitStatus
	switch {
	case err == nil:
	case errors.As(err, &exit):
		os.Exit(int(exit))
	case errors.Is(err, errUsage):
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	default:
		fmt.Fprintln(os.Stderr, "live:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage")

// exitStatus is the exit code of a command run in a VM, which vm ssh
// passes on without a message.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func run(command string, args []string) error {
	if command == "-h" || command == "help" || command == "--help" {
		fmt.Print(usage)
		return nil
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	switch command {
	case "doctor":
		install := flags.Bool("install", false, "install missing tools with Homebrew without asking")
		yes := flags.Bool("yes", false, "answer yes to prompts")
		if err := flags.Parse(args); err != nil {
			return errUsage
		}
		return e.doctor(*install || *yes)
	case "services":
		if len(args) != 1 {
			return errUsage
		}
		return e.services(args[0])
	case "base":
		force := flags.Bool("force", false, "replace an existing base VM")
		if len(args) < 2 {
			return errUsage
		}
		if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
			return errUsage
		}
		return e.base(args[0], args[1], *force)
	case "vm":
		return e.vm(args)
	case "setup", "verify":
		if len(args) != 1 {
			return errUsage
		}
		if command == "setup" {
			return e.setup(args[0])
		}
		return e.verify(args[0])
	case "clean", "status":
		if len(args) != 0 {
			return errUsage
		}
		if command == "clean" {
			return e.clean()
		}
		return e.status()
	}
	return errUsage
}
