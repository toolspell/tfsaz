package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// vmInfo is what vm up prints and saves in the clone's run directory.
type vmInfo struct {
	Name       string `json:"name"`
	OS         string `json:"os"`
	User       string `json:"user"`
	IP         string `json:"ip"`
	Port       int    `json:"port,omitempty"` // SSH port; empty is 22
	BedAddress string `json:"bedAddress"`
	SSHKey     string `json:"sshKey"`
	KnownHosts string `json:"knownHosts"`
	SSH        string `json:"ssh"`
}

func (e *env) runDir(name string) string { return e.path("runs", name) }

func (e *env) loadVM(name string) (vmInfo, error) {
	var info vmInfo
	if _, ok := osOfVM(name); !ok {
		return info, fmt.Errorf("%s is not a clone name (tfsaz-OS-SUFFIX)", name)
	}
	data, err := os.ReadFile(filepath.Join(e.runDir(name), "vm.json"))
	if err != nil {
		return info, fmt.Errorf("no clone %s: %w", name, err)
	}
	return info, json.Unmarshal(data, &info)
}

func (info vmInfo) login() sshTarget {
	o, _ := osOfVM(info.Name)
	return sshTarget{
		user: info.User, host: info.IP, port: info.Port, key: info.SSHKey, knownHosts: info.KnownHosts,
		powershell: o.guest.powershell,
	}
}

func (e *env) saveVM(info vmInfo) ([]byte, error) {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, err
	}
	return data, os.WriteFile(filepath.Join(e.runDir(info.Name), "vm.json"), data, 0o600)
}

var suffixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,19}$`)

func (e *env) vm(args []string) error {
	if len(args) == 0 {
		return errUsage
	}
	if err := requireMac(); err != nil {
		return err
	}
	flags := flag.NewFlagSet("vm "+args[0], flag.ContinueOnError)
	switch args[0] {
	case "up":
		name := flags.String("name", "", "clone name suffix: tfsaz-OS-SUFFIX (default random)")
		if len(args) < 2 || flags.Parse(args[2:]) != nil || flags.NArg() != 0 {
			return errUsage
		}
		info, err := e.vmUp(args[1], *name)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(info, "", "  ")
		if err == nil {
			fmt.Println(string(data))
		}
		return err
	case "ssh":
		if len(args) < 2 || (len(args) > 2 && args[2] != "--") {
			return errUsage
		}
		info, err := e.loadVM(args[1])
		if err != nil {
			return err
		}
		var command []string
		if len(args) > 3 {
			command = args[3:]
		}
		return info.login().attach(command)
	case "stop":
		force := flags.Bool("force", false, "stop without a shutdown; can lose unwritten changes")
		if len(args) < 2 || flags.Parse(args[2:]) != nil || flags.NArg() != 0 {
			return errUsage
		}
		return e.vmStop(args[1], *force)
	case "down", "start":
		if len(args) != 2 {
			return errUsage
		}
		switch args[0] {
		case "start":
			return e.vmStart(args[1])
		}
		return e.vmDown(args[1])
	case "list":
		asJSON := flags.Bool("json", false, "print JSON")
		if flags.Parse(args[1:]) != nil {
			return errUsage
		}
		return e.vmList(*asJSON)
	}
	return errUsage
}

// vmUp clones the base, boots the clone, replaces the build key with a new
// key for this run, and joins the clone to the router.
func (e *env) vmUp(osName, suffix string) (info vmInfo, err error) {
	o, err := lookupOS(osName)
	if err != nil {
		return info, err
	}
	if suffix == "" {
		b := make([]byte, 3)
		_, _ = rand.Read(b)
		suffix = hex.EncodeToString(b)
	}
	if !suffixPattern.MatchString(suffix) || suffix == "base" || suffix == "build" {
		return info, fmt.Errorf("-name must be 1 to 20 lowercase letters, digits or dashes, not base or build")
	}
	name := prefix + o.name + "-" + suffix
	v := e.vms(o)
	if ok, err := v.exists(o.base()); err != nil || !ok {
		return info, errors.Join(err, fmt.Errorf("%s does not exist; run base build %s", o.base(), o.name))
	}
	if ok, err := v.exists(name); err != nil || ok {
		return info, errors.Join(err, fmt.Errorf("%s exists already", name))
	}
	if err := e.requireRouter(); err != nil {
		return info, err
	}
	if err := e.macosSlot(o); err != nil {
		return info, err
	}
	dir := e.runDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return info, err
	}
	defer func() {
		if err != nil {
			step("cleaning up %s after the error", name)
			err = errors.Join(err, e.vmDown(name))
		}
	}()

	step("cloning %s to %s", o.base(), name)
	if err := v.clone(o.base(), name); err != nil {
		return info, err
	}
	if err := v.start(name, filepath.Join(dir, o.provider+".log")); err != nil {
		return info, err
	}
	host, port, err := v.address(name, 3*time.Minute)
	if err != nil {
		return info, err
	}
	info = vmInfo{
		Name: name, OS: o.name, User: o.user, IP: host, Port: port,
		SSHKey: filepath.Join(dir, "id_ed25519"), KnownHosts: filepath.Join(dir, "known_hosts"),
	}
	step("waiting for SSH on %s", host)
	build := info.login()
	build.key = e.baseKey(o)
	if err := build.wait(5 * time.Minute); err != nil {
		return info, err
	}
	if err := newSSHKey(info.SSHKey, "tfsaz-live "+name); err != nil {
		return info, err
	}
	pub, err := os.Open(info.SSHKey + ".pub")
	if err != nil {
		return info, err
	}
	_, err = build.run(pub, o.guest.authorize)
	_ = pub.Close()
	if err != nil {
		return info, err
	}
	login := info.login()
	if _, err := login.run(nil, "exit 0"); err != nil {
		return info, fmt.Errorf("the run key does not work: %w", err)
	}
	if _, err := login.run(nil, fmt.Sprintf(o.guest.hostname, name)); err != nil {
		return info, err
	}
	if o.guest.restart != "" {
		step("restarting %s for its new name", name)
		if err := restart(o, login); err != nil {
			return info, err
		}
	}
	if info.BedAddress, err = e.joinRouter(o, name, login); err != nil {
		return info, err
	}
	// Flush the setup to the clone's disk, so a later stop cannot lose it.
	if _, err := login.run(nil, o.guest.sync); err != nil {
		return info, err
	}
	info.SSH = "go run ./tools/live vm ssh " + name + " -- CMD"
	_, err = e.saveVM(info)
	return info, err
}

// restart reboots the clone and waits until SSH answers again. QEMU keeps
// its port forward, so the address stays the same.
func restart(o osDef, login sshTarget) error {
	_, _ = login.run(nil, o.guest.restart)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if _, err := login.run(nil, "exit 0"); err != nil {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("the clone did not restart within 2 minutes")
		}
		time.Sleep(2 * time.Second)
	}
	return login.wait(5 * time.Minute)
}

// joinRouter gives the clone a WireGuard tunnel to the router and checks
// that it reaches the mock by name.
func (e *env) joinRouter(o osDef, name string, login sshTarget) (string, error) {
	routerKey, err := e.routerKey()
	if err != nil {
		return "", err
	}
	// The router is published on the host, which is the clone's gateway.
	gateway, err := login.run(nil, o.guest.gateway)
	if err != nil || gateway == "" {
		return "", fmt.Errorf("find the clone's gateway: %w", err)
	}
	public, err := login.run(nil, o.guest.wgKey)
	if err != nil {
		return "", err
	}
	address, err := e.registerPeer(name, public)
	if err != nil {
		return "", err
	}
	config := strings.NewReader(wgConfig(address, routerKey, gateway, o.guest))
	if _, err := login.run(config, o.guest.wgUp); err != nil {
		return "", err
	}
	return address.String(), reachesMock(o, login)
}

// reachesMock waits until the clone reaches the mock by name through the router.
func reachesMock(o osDef, login sshTarget) error {
	var last error
	for range 15 {
		if _, last = login.run(nil, o.guest.resolves); last == nil {
			if _, last = login.run(nil, o.guest.reaches); last == nil {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("the clone does not reach the mock through the router: %w", last)
}

// vmStop shuts a clone down but keeps it, its key and its router peer, for
// a clone that its case may still need.
func (e *env) vmStop(name string, force bool) error {
	info, err := e.loadVM(name)
	if err != nil {
		return err
	}
	step("stopping %s", name)
	o, _ := osOfVM(name)
	v := e.vms(o)
	if force {
		return v.stop(name)
	}
	// tart stop ends a Linux guest without a shutdown after its timeout, and
	// writes still in the guest's cache are lost; shut down from inside.
	if _, err := info.login().run(nil, o.guest.poweroff); err != nil && !strings.Contains(err.Error(), "closed by remote host") {
		return fmt.Errorf("%w; %s keeps running. vm stop %s -force stops it without a shutdown and can lose its unwritten changes", err, name, name)
	}
	if err := v.waitStopped(name, 2*time.Minute); err != nil {
		return fmt.Errorf("%w; vm stop %s -force stops it without a shutdown and can lose its unwritten changes", err, name)
	}
	return nil
}

// vmStart boots a stopped clone again. Its NAT address or SSH port can
// change; its tunnel comes back on its own.
func (e *env) vmStart(name string) error {
	info, err := e.loadVM(name)
	if err != nil {
		return err
	}
	if err := e.requireRouter(); err != nil {
		return err
	}
	o, _ := osOfVM(name)
	if err := e.macosSlot(o); err != nil {
		return err
	}
	v := e.vms(o)
	step("starting %s", name)
	if err := v.start(name, filepath.Join(e.runDir(name), o.provider+".log")); err != nil {
		return err
	}
	if info.IP, info.Port, err = v.address(name, 3*time.Minute); err != nil {
		return err
	}
	// Save the new address first, so vm stop and vm ssh use it even if a
	// check below fails.
	data, err := e.saveVM(info)
	if err != nil {
		return err
	}
	login := info.login()
	if err := login.wait(5 * time.Minute); err != nil {
		return err
	}
	if err := reachesMock(o, login); err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

// vmDown removes a clone, its router peer and its run directory; parts
// that are already gone are skipped.
func (e *env) vmDown(name string) error {
	o, ok := osOfVM(name)
	if !ok {
		return fmt.Errorf("%s is not a clone name (tfsaz-OS-SUFFIX)", name)
	}
	step("removing %s", name)
	// Keep the peer and the run directory with the clone's key while the VM
	// still exists, so it stays reachable and vm down can be retried.
	if err := e.vms(o).remove(name); err != nil {
		return fmt.Errorf("%w; kept %s and the router peer", err, e.runDir(name))
	}
	return errors.Join(e.removePeer(name), os.RemoveAll(e.runDir(name)))
}

// cloneNames lists clone names known to a provider, the run directories or
// the router.
func (e *env) cloneNames() ([]string, error) {
	seen := make(map[string]bool)
	vms, err := e.allVMs()
	if err != nil {
		return nil, err
	}
	for _, vm := range vms {
		seen[vm.Name] = true
	}
	runs, _ := os.ReadDir(e.path("runs"))
	for _, r := range runs {
		seen[r.Name()] = true
	}
	peers, err := e.peers()
	if err != nil {
		return nil, err
	}
	for p := range peers {
		seen[p] = true
	}
	var names []string
	for n := range seen {
		if _, ok := osOfVM(n); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (e *env) vmList(asJSON bool) error {
	names, err := e.cloneNames()
	if err != nil {
		return err
	}
	vms, err := e.allVMs()
	if err != nil {
		return err
	}
	state := make(map[string]string)
	for _, vm := range vms {
		state[vm.Name] = vm.State
	}
	type row struct {
		vmInfo
		State string `json:"state"`
	}
	rows := []row{}
	for _, n := range names {
		info, _ := e.loadVM(n)
		info.Name = n
		s := state[n]
		if s == "" {
			s = "missing"
		}
		rows = append(rows, row{info, s})
	}
	if asJSON {
		data, err := json.MarshalIndent(rows, "", "  ")
		if err == nil {
			fmt.Println(string(data))
		}
		return err
	}
	for _, r := range rows {
		address := r.IP
		if r.Port != 0 {
			address = net.JoinHostPort(r.IP, strconv.Itoa(r.Port))
		}
		fmt.Printf("%-24s %-8s %-21s %s\n", r.Name, r.State, address, r.BedAddress)
	}
	return nil
}

func (e *env) clean() error {
	if err := requireMac(); err != nil {
		return err
	}
	names, err := e.cloneNames()
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range names {
		errs = append(errs, e.vmDown(n))
	}
	return errors.Join(errs...)
}

func (e *env) status() error {
	if err := requireMac(); err != nil {
		return err
	}
	fmt.Println("services:")
	if err := e.compose("ps", "--format", "  {{.Service}}\t{{.State}}"); err != nil {
		return err
	}
	vms, err := e.allVMs()
	if err != nil {
		return err
	}
	fmt.Println("base VMs:")
	for _, o := range oses {
		state := "not built"
		for _, vm := range vms {
			if vm.Name == o.base() {
				state = fmt.Sprintf("%s, %.0f GB on disk", vm.State, vm.Size)
			}
		}
		fmt.Printf("  %-8s %s\n", o.name, state)
	}
	fmt.Println("clones:")
	return e.vmList(false)
}
