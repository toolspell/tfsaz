package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func (e *env) runDir(name string) string { return e.instancePath("runs", name) }

func (e *env) loadVM(name string) (vmInfo, error) {
	var info vmInfo
	o, ok := e.osOfVM(name)
	if !ok {
		return info, fmt.Errorf("%s is not a clone of instance %s (use vm list)", name, e.instance)
	}
	dir := e.runDir(name)
	file := filepath.Join(dir, "vm.json")
	data, err := os.ReadFile(file)
	const hint = "check with go run ./tools/live vm list -json"
	if errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Stat(dir); errors.Is(statErr, os.ErrNotExist) {
			return info, fmt.Errorf("no clone %s in this testbed; %s: %w", name, hint, err)
		}
	}
	if err != nil {
		return info, fmt.Errorf("read metadata for %s; %s: %w", name, hint, err)
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return info, fmt.Errorf("decode metadata for %s in %s; %s: %w", name, file, hint, err)
	}
	if info.Name != name || info.OS != o.name {
		return info, fmt.Errorf("clone record does not match %s; %s", name, hint)
	}
	return info, nil
}

func (info vmInfo) login() sshTarget {
	return sshTarget{
		user: info.User, host: info.IP, port: info.Port, key: info.SSHKey, knownHosts: info.KnownHosts,
		powershell: info.OS == "windows",
	}
}

func (e *env) saveVM(info vmInfo) error {
	return writeJSON(filepath.Join(e.runDir(info.Name), "vm.json"), info)
}

var suffixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,19}$`)

func validSuffix(s string) bool {
	return suffixPattern.MatchString(s) && s != "base" && s != "build"
}

func (e *env) vm(action string, call liveInvocation) error {
	if err := requireMac(); err != nil {
		return err
	}
	args := call.args
	switch action {
	case "up":
		info, err := e.vmUp(args[0], call.suffix)
		if err != nil {
			return err
		}
		return printVM(info)
	case "ssh":
		info, err := e.loadVM(args[0])
		if err != nil {
			return err
		}
		return info.login().attach(call.guest)
	case "run":
		return e.vmRun(args[0], call.guest, call.asJSON, call.gui, call.timeout)
	case "push":
		return e.vmCopy(args[0], args[1], args[2], true)
	case "pull":
		return e.vmCopy(args[0], args[2], args[1], false)
	case "forward":
		return e.vmForward(args[0], call.localPort, call.remotePort)
	case "screenshot":
		return e.vmScreenshot(args[0], args[1])
	case "vnc":
		return e.vmVNC(args[0])
	case "t3 install":
		return e.vmT3Install(args[0])
	case "t3 start":
		return e.vmT3Start(args[0], call.port)
	case "stop":
		return e.vmStop(args[0], call.force)
	case "start":
		info, err := e.startVM(args[0])
		if err != nil {
			return err
		}
		return printVM(info)
	case "down":
		return e.vmDown(args[0])
	case "list":
		return e.vmList(call.asJSON)
	}
	return fmt.Errorf("unknown vm action %q", action)
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
	if !validSuffix(suffix) {
		return info, fmt.Errorf("-name must be 1 to 20 lowercase letters, digits or dashes, not base or build")
	}
	if err := e.requireRouter(); err != nil {
		return info, err
	}
	name := e.cloneName(o, suffix)
	if err := e.cloneVM(o, name); err != nil {
		return info, err
	}
	defer func() {
		if err != nil {
			step("cleaning up %s after the error", name)
			err = e.cleanupFailedVM(o, name, err)
		}
	}()
	dir := e.runDir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return info, err
	}
	host, port, err := e.bootVM(o, name)
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
	if _, err := login.run(nil, fmt.Sprintf(o.guest.hostname, guestHostname(o, name))); err != nil {
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
	if err := waitSession(o, login); err != nil {
		return info, err
	}
	// Flush the setup to the clone's disk, so a later stop cannot lose it.
	if _, err := login.run(nil, o.guest.sync); err != nil {
		return info, err
	}
	info.SSH = "go run ./tools/live vm ssh " + name + " -- CMD"
	return info, e.saveVM(info)
}

func (e *env) cleanupFailedVM(o osDef, name string, cause error) error {
	logFile := filepath.Join(e.runDir(name), o.provider+".log")
	if _, err := os.Stat(logFile); errors.Is(err, os.ErrNotExist) {
		return errors.Join(cause, e.vmDown(name))
	}
	kept := e.instancePath("failed-starts", name+".log")
	err := os.MkdirAll(filepath.Dir(kept), 0o700)
	if err == nil {
		err = os.Rename(logFile, kept)
	}
	if err != nil {
		diagnostic := fmt.Errorf("%w%s", cause, startupLogTail(logFile))
		return errors.Join(diagnostic, fmt.Errorf("save startup log: %w", err), e.vmDown(name))
	}
	cleanupErr := e.vmDown(name)
	return errors.Join(vmStartError(cause, kept), cleanupErr)
}

func vmStartError(cause error, logFile string) error {
	return fmt.Errorf("%w\nStartup log: %s%s", cause, logFile, startupLogTail(logFile))
}

func startupLogTail(logFile string) string {
	data, err := os.ReadFile(logFile)
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(vncPattern.ReplaceAllString(string(data), "[VNC address omitted]")), "\n")
	return "\n" + strings.Join(lines[max(0, len(lines)-5):], "\n")
}

func printVM(info vmInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err == nil {
		fmt.Println(string(data))
	}
	return err
}

func guestHostname(o osDef, name string) string {
	if o.name == "windows" {
		// Windows computer names have a 15-character NetBIOS limit.
		sum := sha256.Sum256([]byte(name))
		return prefix + hex.EncodeToString(sum[:4])
	}
	return name
}

func (e *env) cloneVM(o osDef, name string) error {
	unlock, err := e.lockBase(o, true)
	if err != nil {
		return err
	}
	defer unlock()
	v := e.vms(o)
	if ok, err := v.exists(o.base()); err != nil || !ok {
		return errors.Join(err, fmt.Errorf("%s does not exist; run base build %s", o.base(), o.name))
	}
	if ok, err := v.exists(name); err != nil || ok {
		return errors.Join(err, fmt.Errorf("%s exists already; use vm start or vm down NAME", name))
	}
	if _, err := os.Stat(e.runDir(name)); err == nil {
		return fmt.Errorf("%s has leftover run files; run vm down %s", name, name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	step("cloning %s to %s", o.base(), name)
	if err := v.clone(o, name); err != nil {
		return errors.Join(err, v.remove(name))
	}
	return nil
}

func (e *env) bootVM(o osDef, name string) (string, int, error) {
	if err := e.startVMProcess(o, name); err != nil {
		return "", 0, err
	}
	v := e.vms(o)
	host, port, err := v.address(name, 3*time.Minute)
	if err != nil {
		return "", 0, errors.Join(err, v.stop(name))
	}
	return host, port, nil
}

func (e *env) startVMProcess(o osDef, name string) error {
	unlock, err := e.lockMacOSBoot(o)
	if err != nil {
		return err
	}
	defer unlock()
	if err := e.macosSlot(o); err != nil {
		return err
	}
	v := e.vms(o)
	logFile := filepath.Join(e.runDir(name), o.provider+".log")
	step("starting %s", name)
	if err := v.start(name, logFile); err != nil {
		return err
	}
	if o.macos == 0 {
		return nil
	}
	// Tart announces VNC after the VM starts. Polling tart list here can lock the ASIF disk during boot.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := tartVNC(logFile); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.Join(fmt.Errorf("%s did not start within 20s", name), v.stop(name))
}

// restart reboots the clone and waits until SSH answers from the new boot.
// QEMU keeps its port forward, so the address stays the same.
func restart(o osDef, login sshTarget) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	before, err := login.runContext(ctx, nil, o.guest.bootID)
	if err != nil {
		return err
	}
	// The session may end with the restart; only a new boot ID tells.
	_, _ = login.runContext(ctx, nil, o.guest.restart)
	for {
		probe, stop := context.WithTimeout(ctx, 15*time.Second)
		after, err := login.runContext(probe, nil, o.guest.bootID)
		stop()
		if err == nil && after != before {
			return nil
		}
		if ctx.Err() != nil {
			return errors.New("the clone did not restart within 5 minutes")
		}
		time.Sleep(3 * time.Second)
	}
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
	config := strings.NewReader(e.network.wgConfig(address, routerKey, gateway, o.guest))
	if _, err := login.run(config, strings.ReplaceAll(o.guest.wgUp, "${router_ip}", e.network.address(2).String())); err != nil {
		return "", err
	}
	return address.String(), e.reachesMock(o, login)
}

// waitSession waits until the test user's desktop session runs, so a clone
// is ready for -gui commands and screenshots when vm up or vm start returns.
func waitSession(o osDef, login sshTarget) error {
	if o.guest.session == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for {
		_, err := login.runContext(ctx, nil, o.guest.session)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("the test user's desktop session did not start within 2 minutes: %w", err)
		case <-time.After(3 * time.Second):
		}
	}
}

// reachesMock waits until the clone reaches the mock by name through the router.
func (e *env) reachesMock(o osDef, login sshTarget) error {
	resolves := fmt.Sprintf(o.guest.resolves, e.network.address(10))
	var last error
	for range 15 {
		if _, last = login.run(nil, resolves); last == nil {
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
	o, _ := e.osOfVM(name)
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

func (e *env) startVM(name string) (vmInfo, error) {
	info, err := e.loadVM(name)
	if err != nil {
		return info, err
	}
	if err := e.requireRouter(); err != nil {
		return info, err
	}
	o, _ := e.osOfVM(name)
	v := e.vms(o)
	list, err := v.list()
	if err != nil {
		return info, err
	}
	for _, vm := range list {
		if vm.Name != name {
			continue
		}
		if vm.isRunning() {
			info.IP, info.Port, err = v.address(name, 3*time.Minute)
		} else {
			info.IP, info.Port, err = e.bootVM(o, name)
			if err != nil {
				err = vmStartError(err, filepath.Join(e.runDir(name), o.provider+".log"))
			}
		}
		if err != nil {
			return info, err
		}
		if err := e.saveVM(info); err != nil {
			return info, err
		}
		login := info.login()
		if err := login.wait(5 * time.Minute); err != nil {
			return info, err
		}
		if err := e.reachesMock(o, login); err != nil {
			return info, err
		}
		return info, waitSession(o, login)
	}
	return info, fmt.Errorf("clone %s has a record but no VM; run vm down %s and retry", name, name)
}

// vmDown removes a clone, its router peer and its run directory; parts
// that are already gone are skipped.
func (e *env) vmDown(name string) error {
	o, own := e.osOfVM(name)
	legacyOS, legacy := legacyCloneOS(name)
	if !own && !legacy {
		return fmt.Errorf("%s is not a clone of instance %s (use vm list)", name, e.instance)
	}
	dir := e.runDir(name)
	if legacy {
		o = legacyOS
		dir = e.path("runs", name)
	}
	step("removing %s", name)
	// Keep the peer and the run directory with the clone's key while the VM
	// still exists, so it stays reachable and vm down can be retried.
	if err := e.vms(o).remove(name); err != nil {
		return fmt.Errorf("%w; kept %s and the router peer", err, dir)
	}
	if legacy {
		return errors.Join(removePeerFile(e.path("router", "peers", name)), os.RemoveAll(dir))
	}
	return errors.Join(e.removePeer(name), os.RemoveAll(dir))
}

// cloneNames lists clone names known to a provider, the run directories or
// the router.
func (e *env) cloneNames(vms []vmEntry) ([]string, error) {
	seen := make(map[string]bool)
	for _, vm := range vms {
		seen[vm.Name] = true
	}
	runs, err := os.ReadDir(e.instancePath("runs"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
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
	return e.ownClones(seen), nil
}

func (e *env) ownClones(seen map[string]bool) []string {
	var names []string
	for n := range seen {
		if _, ok := e.osOfVM(n); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

type cloneRow struct {
	vmInfo
	State  string `json:"state"`
	Legacy bool   `json:"legacy,omitempty"`
}

func (e *env) legacyClones(vms []vmEntry) (map[string]osDef, error) {
	legacy := make(map[string]osDef)
	for _, vm := range vms {
		if o, ok := legacyCloneOS(vm.Name); ok {
			legacy[vm.Name] = o
		}
	}
	for _, dir := range []string{e.path("runs"), e.path("router", "peers")} {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, entry := range entries {
			if o, ok := legacyCloneOS(entry.Name()); ok {
				legacy[entry.Name()] = o
			}
		}
	}
	return legacy, nil
}

func (e *env) cloneRows() ([]cloneRow, error) {
	vms, err := e.allVMs()
	if err != nil {
		return nil, err
	}
	names, err := e.cloneNames(vms)
	if err != nil {
		return nil, err
	}
	legacy, err := e.legacyClones(vms)
	if err != nil {
		return nil, err
	}
	state := make(map[string]string)
	for _, vm := range vms {
		state[vm.Name] = vm.State
	}
	for name := range legacy {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := []cloneRow{}
	for _, n := range names {
		info := vmInfo{Name: n}
		o, old := legacy[n]
		if old {
			info.OS = o.name
		} else if saved, err := e.loadVM(n); err == nil {
			info = saved
		}
		s := state[n]
		if s == "" {
			s = "missing"
		}
		rows = append(rows, cloneRow{info, s, old})
	}
	return rows, nil
}

func (e *env) vmList(asJSON bool) error {
	rows, err := e.cloneRows()
	if err != nil {
		return err
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
		name := r.Name
		if r.Legacy {
			name += " (legacy)"
		}
		fmt.Printf("%-24s %-8s %-21s %s\n", name, r.State, address, r.BedAddress)
	}
	return nil
}

func (e *env) removeClones() error {
	vms, err := e.allVMs()
	if err != nil {
		return err
	}
	names, err := e.cloneNames(vms)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range names {
		errs = append(errs, e.vmDown(n))
	}
	return errors.Join(errs...)
}

func (e *env) status(out io.Writer) error {
	slots, err := e.readSlots()
	if err != nil {
		return err
	}
	orphans, err := e.orphanedSlots(slots)
	if err != nil {
		return err
	}
	for _, id := range orphans {
		if _, err := fmt.Fprintf(out, "orphaned instance %s (slot %d, missing worktree %s); run go run ./tools/live prune\n",
			id, slots[id].Slot, slots[id].Root); err != nil {
			return err
		}
	}
	if err := e.loadNetwork(); err != nil {
		return err
	}
	if err := requireMac(); err != nil {
		return err
	}
	fmt.Println("services:")
	if err := e.services("status"); err != nil {
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
