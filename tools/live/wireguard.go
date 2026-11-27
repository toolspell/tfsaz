package main

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Addresses of the test bed; they match live-tests/compose.yaml.
var (
	routerAddress = netip.MustParseAddr("172.30.53.2")
	firstVMAddr   = netip.MustParseAddr("172.30.53.129")
	lastVMAddr    = netip.MustParseAddr("172.30.53.254")
)

// wgConfig is the VM's tunnel: all traffic goes to the router, except the
// tunnel itself and the VM's own NAT network, so SSH from the host keeps
// working. lines set the private key, which stays in the VM in a file wg
// genkey wrote there, and DNS through the router's resolver.
func wgConfig(address netip.Addr, routerKey, endpoint, lines string) string {
	return fmt.Sprintf(`[Interface]
Address = %s/32
%s
[Peer]
PublicKey = %s
Endpoint = %s:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25
`, address, lines, routerKey, endpoint)
}

// freeAddress picks the first VM address no saved peer uses.
func freeAddress(used map[netip.Addr]bool) (netip.Addr, error) {
	for a := firstVMAddr; a.Compare(lastVMAddr) <= 0; a = a.Next() {
		if !used[a] {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("all VM addresses from %s to %s are in use", firstVMAddr, lastVMAddr)
}

// peers reads the router's saved peers: one file per VM with "KEY ADDRESS".
func (e *env) peers() (map[string]netip.Addr, error) {
	dir := e.path("router", "peers")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	peers := make(map[string]netip.Addr)
	for _, entry := range entries {
		// peer.sh stages a record as .NAME and renames it into place.
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if os.IsNotExist(err) {
			continue // removed since the listing
		}
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(string(data))
		if len(fields) != 2 {
			continue
		}
		if addr, err := netip.ParseAddr(fields[1]); err == nil {
			peers[entry.Name()] = addr
		}
	}
	return peers, nil
}

func (e *env) routerKey() (string, error) {
	data, err := os.ReadFile(e.path("router", "public.key"))
	if err != nil {
		return "", fmt.Errorf("router key missing; run services up: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// registerPeer adds the VM to the router at the first free address. The
// router refuses an address another peer took meanwhile; then the next one
// is tried.
func (e *env) registerPeer(name, key string) (netip.Addr, error) {
	for range 5 {
		peers, err := e.peers()
		if err != nil {
			return netip.Addr{}, err
		}
		used := make(map[netip.Addr]bool)
		for peer, a := range peers {
			if peer != name {
				used[a] = true
			}
		}
		address, err := freeAddress(used)
		if err != nil {
			return address, err
		}
		step("joining %s to the router as %s", name, address)
		err = e.compose("exec", "-T", "router", "peer", "add", name, key, address.String())
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 {
			return address, err
		}
	}
	return netip.Addr{}, fmt.Errorf("no free router address for %s after 5 tries", name)
}

// removePeer drops a peer. With the router down, deleting its file is
// enough, since the router loads the files on start.
func (e *env) removePeer(name string) error {
	if running, _ := e.routerRunning(); running {
		return e.compose("exec", "-T", "router", "peer", "remove", name)
	}
	err := os.Remove(e.path("router", "peers", name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
