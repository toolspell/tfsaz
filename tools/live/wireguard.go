package main

import (
	"fmt"
	"net/netip"
	"os"
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
// working. DNS goes to the router's resolver. The private key stays in the
// VM, in a file wg genkey wrote there.
func wgConfig(address netip.Addr, routerKey, endpoint string) string {
	return fmt.Sprintf(`[Interface]
Address = %s/32
PostUp = wg set %%i private-key /etc/wireguard/private.key
PostUp = resolvectl dns %%i %s; resolvectl domain %%i '~.'

[Peer]
PublicKey = %s
Endpoint = %s:51820
AllowedIPs = 0.0.0.0/0
PersistentKeepalive = 25
`, address, routerAddress, routerKey, endpoint)
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

func (e *env) addPeer(name, key string, address netip.Addr) error {
	return e.compose("exec", "-T", "router", "peer", "add", name, key, address.String())
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
