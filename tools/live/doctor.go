package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// tool is one host program the live tests need, with the Homebrew formula
// that provides it.
type tool struct {
	name    string
	bin     string
	version []string // command printing the version; nil when presence is enough
	min     string
	brew    []string // brew arguments that install it; nil when Homebrew cannot
	trust   []string // formulas from untrusted taps that brew must trust first
	fix     string   // what to do when it is installed but not working
}

var tools = []tool{
	{name: "tart", bin: "tart", version: []string{"tart", "--version"}, min: "2.0.0",
		brew: []string{"install", "openai/tools/tart"}, trust: []string{"openai/tools/tart", "openai/tools/softnet"}},
	{name: "packer", bin: "packer", version: []string{"packer", "--version"}, min: "1.11.0",
		brew: []string{"install", "hashicorp/tap/packer"}},
	{name: "qemu", bin: "qemu-system-aarch64", version: []string{"qemu-system-aarch64", "--version"}, min: "9.0.0",
		brew: []string{"install", "qemu"}},
	{name: "docker", bin: "docker", version: []string{"docker", "version", "--format", "{{.Server.Version}}"},
		brew: []string{"install", "--cask", "orbstack"}, fix: "start OrbStack, or another Docker engine"},
	{name: "docker compose", bin: "docker", version: []string{"docker", "compose", "version", "--short"}, min: "2.20.0",
		fix: "install the Docker Compose plugin; OrbStack and Docker Desktop include it"},
	{name: "curl", bin: "curl", fix: "part of macOS"},
	{name: "hdiutil", bin: "hdiutil", fix: "part of macOS"},
	{name: "ssh", bin: "ssh", fix: "part of macOS"},
	{name: "ssh-keygen", bin: "ssh-keygen", fix: "part of macOS"},
}

var versionPattern = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)

// check reports whether the tool is installed, and if so whether it works
// and is new enough; detail is the version or what is wrong.
func (t tool) check() (installed, ok bool, detail string) {
	if _, err := exec.LookPath(t.bin); err != nil {
		return false, false, "not installed"
	}
	if t.version == nil {
		return true, true, "ok"
	}
	out, err := exec.Command(t.version[0], t.version[1:]...).CombinedOutput()
	if err != nil {
		return true, false, "installed but not working: " + t.fix
	}
	if t.min == "" {
		return true, true, "ok"
	}
	found := versionPattern.FindString(string(out))
	if found == "" || versionLess(found, t.min) {
		return true, false, fmt.Sprintf("version %q is older than %s; upgrade it", found, t.min)
	}
	return true, true, found
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(pa), len(pb)) {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

func (e *env) doctor(install, checkOnly bool) error {
	if err := requireMac(); err != nil {
		return err
	}
	missing, failing := checkTools()
	if !checkOnly && len(missing) > 0 {
		if err := brewInstall(missing, install); err != nil {
			return err
		}
		step("checking again")
		missing, failing = checkTools()
	}
	e.adviseDHCP()
	if n := len(missing) + len(failing); n > 0 {
		return fmt.Errorf("%d tools missing or not working", n)
	}
	fmt.Println("state    " + e.home)
	if checkOnly {
		return nil
	}
	return e.ensureHome()
}

// checkTools prints one line per tool and returns the ones Homebrew can
// install and the ones that need another fix.
func checkTools() (missing, failing []tool) {
	for _, t := range tools {
		installed, ok, detail := t.check()
		switch {
		case ok:
			fmt.Printf("ok       %-15s %s\n", t.name, detail)
		case !installed && t.brew != nil:
			fmt.Printf("missing  %-15s %s\n", t.name, detail)
			missing = append(missing, t)
		default:
			if !installed {
				detail += ": " + t.fix
			}
			fmt.Printf("failing  %-15s %s\n", t.name, detail)
			failing = append(failing, t)
		}
	}
	return missing, failing
}

func brewInstall(ts []tool, install bool) error {
	if _, err := exec.LookPath("brew"); err != nil {
		return fmt.Errorf("install Homebrew (https://brew.sh), then run doctor -install")
	}
	if !install && !confirm(fmt.Sprintf("Install %s with Homebrew?", names(ts))) {
		return fmt.Errorf("run doctor -install to install %s", names(ts))
	}
	for _, t := range ts {
		if len(t.trust) > 0 {
			// Homebrew refuses formulas from untrusted taps; trust only these.
			if err := cmd("", nil, "brew", append([]string{"trust", "--formula"}, t.trust...)...); err != nil {
				return err
			}
		}
		if err := cmd("", []string{"HOMEBREW_NO_AUTO_UPDATE=1"}, "brew", t.brew...); err != nil {
			return err
		}
	}
	return nil
}

func names(ts []tool) string {
	var n []string
	for _, t := range ts {
		n = append(n, t.name)
	}
	return strings.Join(n, ", ")
}

func confirm(question string) bool {
	if info, err := os.Stdin.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Printf("%s [y/N] ", question)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}

// adviseDHCP warns when macOS keeps VM DHCP leases for the default day:
// every clone gets a new MAC address, and the pool of about 250 addresses
// runs out after that many clones a day.
func (e *env) adviseDHCP() {
	out, _ := exec.Command("defaults", "read", "/Library/Preferences/SystemConfiguration/com.apple.InternetSharing.default.plist", "bootpd").Output()
	if strings.Contains(string(out), "DHCPLeaseTimeSecs = 600") {
		return
	}
	fmt.Println("advice   VM DHCP leases last a day; to shorten them once (needs sudo):")
	fmt.Println("         sudo defaults write /Library/Preferences/SystemConfiguration/com.apple.InternetSharing.default.plist bootpd -dict DHCPLeaseTimeSecs -int 600")
}
