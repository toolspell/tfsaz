// Command live builds and runs the VMs and services of TFSaz's live tests.
// Agents can drive every command; none prompts unless it runs in a terminal
// without -yes. See live-tests/README.md.
//
//	go run ./tools/live setup OS                   install, start and build what is missing, then verify
//	go run ./tools/live verify OS                  check a throwaway clone against the test bed
//	go run ./tools/live doctor [-install]          check the host tools; -install adds missing ones
//	go run ./tools/live services up|down|status    mock, DNS sinkhole and VM router in Docker
//	go run ./tools/live mock ca
//	go run ./tools/live mock requests [-vm NAME] [-json]
//	go run ./tools/live mock reset
//	go run ./tools/live base build OS [-force]     build the base VM from scratch
//	go run ./tools/live base update OS             upgrade the base VM's packages in place
//	go run ./tools/live base rm OS                 delete the base VM
//	go run ./tools/live vm up OS [-name SUFFIX]    clone the base, boot it, join the router; prints JSON
//	go run ./tools/live vm ssh NAME [-- CMD...]    open a shell or run a command in a clone
//	go run ./tools/live vm run NAME [-json] [-timeout D] [-gui] -- CMD...
//	go run ./tools/live vm push|pull NAME FROM TO   copy files to or from a clone
//	go run ./tools/live vm forward NAME PORT[:PORT] forward a host loopback port to the clone
//	go run ./tools/live vm screenshot NAME FILE    save a PNG of the clone's screen
//	go run ./tools/live vm vnc NAME                print the VNC address of the clone's screen
//	go run ./tools/live vm t3 install NAME         install T3 Code at the reference pin in a clone
//	go run ./tools/live vm t3 start NAME           start it with a debugging port for Playwright
//	go run ./tools/live vm stop NAME [-force]      shut a clone down and keep it for later
//	go run ./tools/live vm start NAME              boot a stopped clone again
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
  mock ca                         print the path to the mock CA certificate
  mock requests [-vm NAME] [-json]  read the mock's request journal
  mock reset                      clear the mock's request journal
  base build OS [-force]          build OS's base VM from scratch (-force replaces an existing one)
  base update OS                  upgrade the base VM's packages in place
  base rm OS                      delete the base VM
  vm up OS [-name SUFFIX]         clone the base, boot it, join the router; prints JSON
  vm ssh NAME [-- CMD...]         open a shell or run a command in a clone
  vm run NAME [-json] [-timeout D] [-gui] -- CMD...
                                  run a command; -gui in the test user's desktop session
  vm push NAME LOCAL REMOTE       copy files into a clone
  vm pull NAME REMOTE LOCAL       copy files out of a clone
  vm forward NAME PORT[:PORT]     forward a host loopback port to the clone's loopback
  vm screenshot NAME FILE         save a PNG of the clone's screen
  vm vnc NAME                     print the VNC address of the clone's screen
  vm t3 install NAME              install the T3 Code desktop app of the reference pin
  vm t3 start NAME [-port P]      start it with Chrome's debugging port P (9222); prints its URL in the clone
  vm stop NAME [-force]           shut a clone down but keep it for later
  vm start NAME                   boot a stopped clone again; prints JSON
  vm down NAME                    stop and delete a clone
  vm list [-json]                 list clones
  clean                           delete every clone and its router peer
  status                          services, base VMs and clones

OS: ubuntu, macos or windows. base commands refuse while clones exist.
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
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var installTools bool
	if command == "doctor" {
		install := flags.Bool("install", false, "install missing tools with Homebrew without asking")
		yes := flags.Bool("yes", false, "answer yes to prompts")
		if err := flags.Parse(args); err != nil {
			return errUsage
		}
		installTools = *install || *yes
	}
	ci := ciEnvironment()
	if ci != "" && !ciReadOnly(command, args, installTools) {
		return fmt.Errorf("live test changes are disabled when %s is set", ci)
	}
	e, err := newEnv()
	if err != nil {
		return err
	}
	switch command {
	case "doctor":
		return e.doctor(installTools, ci != "")
	case "services":
		if len(args) != 1 {
			return errUsage
		}
		return e.services(args[0])
	case "mock":
		return e.mock(args, os.Stdout)
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

func ciEnvironment() string {
	for _, name := range []string{"CI", "GITHUB_ACTIONS"} {
		if _, set := os.LookupEnv(name); set {
			return name
		}
	}
	return ""
}

func ciReadOnly(command string, args []string, installTools bool) bool {
	switch command {
	case "status":
		return true
	case "doctor":
		return !installTools
	}
	if len(args) == 0 {
		return false
	}
	switch command {
	case "services":
		return args[0] == "status"
	case "mock":
		return args[0] == "ca" || args[0] == "requests"
	case "vm":
		return args[0] == "list" || args[0] == "vnc"
	}
	return false
}
