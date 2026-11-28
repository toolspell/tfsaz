package main

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

const usagePrefix = "usage: go run ./tools/live "

type commandHelp struct {
	path, operands, options, description string
}

var liveCommands = []commandHelp{
	{"", "", "<command>", "Build and run the TFSaz live-test bed."},
	{"up", "", "[OS...]", "Start this instance and print its subnet, host ports and clones as JSON. With OS arguments, boot or reuse one clone per OS in parallel. OS is ubuntu, macos or windows."},
	{"down", "", "", "Delete this instance's clones, remove its services and locally built images, and release its slot."},
	{"prune", "", "", "Remove instances whose worktree paths are missing and release their slots."},
	{"setup", "OS", "", "Install missing host tools, start services, build the base if missing, then run verify. OS is ubuntu, macos or windows."},
	{"verify", "OS", "", "Boot a throwaway clone, check the mock, router and internet access, then remove the clone. OS is ubuntu, macos or windows."},
	{"doctor", "", "[-install] [-yes]", "Check host tools and offer to install missing ones with Homebrew."},
	{"services", "", "<action>", "Manage the mock, DNS sinkhole and VM router in Docker."},
	{"services up", "", "", "Start this instance's testbed services and wait for the router and mock."},
	{"services down", "", "", "Stop and remove this instance's testbed services, including their locally built images, keeping its slot and keys."},
	{"services status", "", "[-json]", "Show this instance's service status, subnet and published ports."},
	{"mock", "", "<action>", "Inspect the Azure DevOps Server mock."},
	{"mock ca", "", "", "Print the path to this instance's mock CA certificate."},
	{"mock requests", "", "[-vm NAME] [-json]", "Read this instance's mock request journal."},
	{"mock reset", "", "", "Clear this instance's mock request journal."},
	{"base", "", "<action>", "Manage base VMs. Base commands refuse while clones exist."},
	{"base build", "OS", "[-force]", "Build a base VM from scratch. OS is ubuntu, macos or windows. Remove existing clones first."},
	{"base update", "OS", "", "Upgrade packages in a base VM in place. OS is ubuntu or macos. Remove existing clones first."},
	{"base rm", "OS", "", "Delete a base VM and its build key. OS is ubuntu, macos or windows. Remove existing clones first."},
	{"vm", "", "<action>", "Manage clones and run commands in them."},
	{"vm up", "OS", "[-name SUFFIX]", "Clone the base, boot it and join the router. Print clone details as JSON. OS is ubuntu, macos or windows."},
	{"vm ssh", "NAME", "[-- CMD...]", "Open an SSH shell or run a command in the clone."},
	{"vm run", "NAME", "[-json] [-timeout D] [-gui] -- CMD...", "Run CMD in the clone and pass through its exit code. Arguments after -- go to the guest."},
	{"vm push", "NAME LOCAL REMOTE", "", "Copy files or directories into a clone."},
	{"vm pull", "NAME REMOTE LOCAL", "", "Copy files or directories out of a clone."},
	{"vm forward", "NAME PORT[:PORT]", "", "Forward a host loopback port to the clone's loopback until stopped. Use PORT for both ends or LOCAL:REMOTE. Each port must be 1 to 65535."},
	{"vm screenshot", "NAME FILE", "", "Save a PNG of the clone's screen."},
	{"vm vnc", "NAME", "", "Print the VNC address of the clone's screen."},
	{"vm t3", "", "<action>", "Install and start T3 Code in a clone."},
	{"vm t3 install", "NAME", "", "Install T3 Code at the reference pin and print its release tag."},
	{"vm t3 start", "NAME", "[-port P]", "Start T3 Code in the desktop session. Wait up to a minute for its debugging port to answer, then print the URL in the clone."},
	{"vm browser", "", "<action>", "Manage browsers in a clone."},
	{"vm browser start", "NAME BROWSER", "[-port P] [URL]", "Start Chrome or Edge in the desktop session and print its debugging URL in the clone. Use chrome on any guest or edge on Windows."},
	{"vm stop", "NAME", "[-force]", "Shut down a clone and keep it for later."},
	{"vm start", "NAME", "", "Boot a stopped clone and print its updated details as JSON."},
	{"vm down", "NAME", "", "Stop and delete a clone and its router peer. Accepts legacy clone names."},
	{"vm list", "", "[-json]", "List this instance's clones and shared legacy clones with their status."},
	{"clean", "", "", "Delete this instance's clones and router peers, leaving its services and slot in place."},
	{"status", "", "", "Show shared base VMs, this instance's services and clones, and orphaned slots."},
}

func (h commandHelp) synopsis() string {
	return strings.TrimSpace(h.path + " " + strings.TrimSpace(h.operands+" "+h.options))
}

func (h commandHelp) group() bool { return h.options == "<action>" }

func (h commandHelp) printHelp(out io.Writer, flags *flag.FlagSet) {
	_, _ = fmt.Fprintln(out, usagePrefix+h.synopsis())
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, h.description)
	switch {
	case h.path == "":
		_, _ = fmt.Fprintln(out, "\nCommands:")
		for _, c := range liveCommands[1:] {
			if !c.group() {
				summary, _, _ := strings.Cut(c.description, ". ")
				_, _ = fmt.Fprintf(out, "  %-53s %s\n", c.synopsis(), strings.TrimSuffix(summary, "."))
			}
		}
		_, _ = fmt.Fprintln(out, "\nRun go run ./tools/live help CMD [SUB...] for details.")
		_, _ = fmt.Fprintln(out, "State lives in $TFSAZ_LIVE_HOME (default ~/.tfsaz-live). $TFSAZ_LIVE_INSTANCE overrides the worktree ID.")
	case h.group():
		_, _ = fmt.Fprintln(out, "\nActions:")
		for _, c := range liveCommands[1:] {
			action, ok := strings.CutPrefix(c.path, h.path+" ")
			if ok && !strings.Contains(action, " ") {
				_, _ = fmt.Fprintf(out, "  %-12s %s\n", action, c.description)
			}
		}
	}
	_, _ = fmt.Fprintln(out, "\nFlags:")
	flags.SetOutput(out)
	flags.PrintDefaults()
	_, _ = fmt.Fprintln(out, "  -h, --help\n    \tShow this help and exit.")
}

type liveInvocation struct {
	commandHelp
	args, guest            []string
	asJSON, force, gui     bool
	install, yes           bool
	suffix, vm, browserURL string
	localPort, remotePort  string
	port                   int
	timeout                time.Duration
}

func (c *liveInvocation) flagSet() *flag.FlagSet {
	flags := flag.NewFlagSet(c.path, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	switch c.path {
	case "doctor":
		flags.BoolVar(&c.install, "install", false, "Install missing tools with Homebrew without asking.")
		flags.BoolVar(&c.yes, "yes", false, "Equivalent to -install.")
	case "base build":
		flags.BoolVar(&c.force, "force", false, "Replace an existing base VM.")
	case "vm up":
		flags.StringVar(&c.suffix, "name", "", "Clone suffix in tfsaz-live-ID-OS-SUFFIX. Use 1 to 20 lowercase letters, digits or dashes, starting with a letter or digit. The suffixes base, build and up are reserved. Defaults to six random hex digits.")
	case "vm stop":
		flags.BoolVar(&c.force, "force", false, "Stop without shutting down the guest. This can lose unwritten changes.")
	case "vm t3 start", "vm browser start":
		flags.IntVar(&c.port, "port", 9222, "Debugging port inside the clone, from 1 to 65535.")
	case "vm run":
		flags.BoolVar(&c.asJSON, "json", false, "Print exit code, output and timing as JSON.")
		flags.DurationVar(&c.timeout, "timeout", 10*time.Minute, "Stop waiting after this duration (for example 30s or 2m). The timeout exit code is 124.")
		flags.BoolVar(&c.gui, "gui", false, "Run in the test user's desktop session.")
	case "vm list", "mock requests", "services status":
		flags.BoolVar(&c.asJSON, "json", false, "Print JSON.")
		if c.path == "mock requests" {
			flags.StringVar(&c.vm, "vm", "", "Filter by clone name.")
		}
	}
	return flags
}

func (c *liveInvocation) readArgs(args []string, flags *flag.FlagSet) error {
	before, hasGuest := args, false
	if i := slices.Index(args, "--"); i >= 0 && (c.path == "vm run" || c.path == "vm ssh") {
		before, c.guest, hasGuest = args[:i], args[i+1:], true
	}
	operands := strings.Fields(c.operands)
	for len(c.args) < len(operands) && len(before) > 0 {
		arg, operand := before[0], operands[len(c.args)]
		if arg == "--" || strings.HasPrefix(arg, "-") && arg != "-" && operand != "PORT[:PORT]" {
			break
		}
		c.args = append(c.args, arg)
		before = before[1:]
	}
	if err := flags.Parse(before); err != nil {
		return err
	}
	if c.path == "up" {
		c.args = flags.Args()
		for _, name := range c.args {
			if _, ok := guestOS(name); !ok {
				return unknownOS(name)
			}
		}
		return nil
	}
	if len(c.args) < len(operands) {
		return fmt.Errorf("missing %s", operands[len(c.args)])
	}
	allowed := 0
	if c.path == "vm browser start" {
		allowed = 1
		c.browserURL = flags.Arg(0)
	}
	if flags.NArg() > allowed {
		return fmt.Errorf("unexpected operand %q", flags.Arg(allowed))
	}
	if c.path == "vm run" {
		if !hasGuest {
			return fmt.Errorf("missing -- CMD after NAME and flags")
		}
		if len(c.guest) == 0 {
			return fmt.Errorf("missing CMD after --")
		}
	}
	return c.validate(operands)
}

func (c *liveInvocation) validate(operands []string) error {
	if len(operands) > 0 {
		switch operands[0] {
		case "OS":
			o, ok := guestOS(c.args[0])
			if !ok {
				return unknownOS(c.args[0])
			}
			if c.path == "base update" && o.guest.update == "" {
				return fmt.Errorf("base update supports ubuntu and macos; rebuild %s with base build %s -force", o.name, o.name)
			}
		case "NAME":
			_, _, modern := parseCloneName(c.args[0])
			_, legacy := legacyCloneOS(c.args[0])
			if !modern && (c.path != "vm down" || !legacy) {
				return fmt.Errorf("invalid clone name %q; use tfsaz-live-ID-OS-SUFFIX from vm list", c.args[0])
			}
		}
	}
	switch c.path {
	case "mock requests":
		if _, _, ok := parseCloneName(c.vm); c.vm != "" && !ok {
			return fmt.Errorf("invalid clone name %q in -vm; use tfsaz-live-ID-OS-SUFFIX from vm list", c.vm)
		}
	case "vm up":
		if c.suffix == upSuffix {
			return fmt.Errorf("-name up is reserved for go run ./tools/live up")
		}
		if c.suffix != "" && !validSuffix(c.suffix) {
			return fmt.Errorf("invalid -name suffix %q; use 1 to 20 lowercase letters, digits or dashes starting with a letter or digit, excluding base and build", c.suffix)
		}
	case "vm t3 start", "vm browser start":
		if !validPort(c.port) {
			return fmt.Errorf("invalid port %d in -port; use 1 to 65535", c.port)
		}
		if c.path == "vm browser start" {
			_, o, _ := parseCloneName(c.args[0])
			switch c.args[1] {
			case "chrome":
			case "edge":
				if o.name != "windows" {
					return fmt.Errorf("only Windows clones come with Edge")
				}
			default:
				return fmt.Errorf("unsupported browser %q. Use mock requests to check navigation for this browser", c.args[1])
			}
		}
	case "vm forward":
		local, remote, pair := strings.Cut(c.args[1], ":")
		if !pair {
			remote = local
		}
		for _, p := range []string{local, remote} {
			if n, err := strconv.Atoi(p); err != nil || !validPort(n) {
				return fmt.Errorf("invalid port %q; use 1 to 65535", p)
			}
		}
		c.localPort, c.remotePort = local, remote
	}
	return nil
}

func validPort(port int) bool { return port >= 1 && port <= 65535 }

func findCommand(path string) (commandHelp, bool) {
	for _, c := range liveCommands {
		if c.path == path {
			return c, true
		}
	}
	return commandHelp{}, false
}

func resolveHelp(args []string) (commandHelp, []string, bool, error) {
	c := liveCommands[0]
	if len(args) == 0 {
		return c, nil, false, fmt.Errorf("missing command")
	}
	explicitHelp := args[0] == "help"
	if explicitHelp {
		args = args[1:]
	}
	for c.path == "" || c.group() {
		kind := "action"
		if c.path == "" {
			kind = "command"
		}
		if len(args) == 0 {
			if explicitHelp {
				return c, nil, true, nil
			}
			return c, nil, false, fmt.Errorf("missing %s", kind)
		}
		if isHelpFlag(args[0]) {
			return c, nil, true, nil
		}
		path := args[0]
		if c.path != "" {
			path = c.path + " " + path
		}
		next, ok := findCommand(path)
		if !ok || args[0] == "" || strings.Contains(args[0], " ") {
			return c, nil, false, fmt.Errorf("unknown %s %q", kind, args[0])
		}
		c, args = next, args[1:]
	}
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if isHelpFlag(arg) {
			return c, nil, true, nil
		}
	}
	if explicitHelp && len(args) != 0 {
		return c, nil, false, fmt.Errorf("unexpected operand %q", args[0])
	}
	return c, args, explicitHelp, nil
}

func isHelpFlag(arg string) bool { return arg == "-h" || arg == "--help" }

// FAST A3 verification
