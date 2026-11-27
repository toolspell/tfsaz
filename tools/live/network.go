package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
)

const slotCount = 16

type bedPorts struct {
	HTTP      int `json:"http"`
	HTTPS     int `json:"https"`
	SSH       int `json:"ssh"`
	Journal   int `json:"journal"`
	WireGuard int `json:"wireguard"`
}

type bedNetwork struct {
	subnet netip.Prefix
	ports  bedPorts
}

func slotNetwork(slot int) bedNetwork {
	return bedNetwork{
		subnet: netip.PrefixFrom(netip.AddrFrom4([4]byte{172, 30, byte(53 + slot), 0}), 24),
		ports:  bedPorts{HTTP: 8080 + slot, HTTPS: 8443 + slot, SSH: 2222 + slot, Journal: 19090 + slot, WireGuard: 51820 + slot},
	}
}

func (b bedNetwork) address(last byte) netip.Addr {
	addr := b.subnet.Addr().As4()
	addr[3] = last
	return netip.AddrFrom4(addr)
}

func (b bedNetwork) composeEnv(project, state string) []string {
	return []string{
		"TFSAZ_LIVE_PROJECT=" + project,
		"TFSAZ_LIVE_STATE=" + filepath.ToSlash(state),
		"TFSAZ_LIVE_SUBNET=" + b.subnet.String(),
		"TFSAZ_LIVE_CONTAINER_SUBNET=" + netip.PrefixFrom(b.address(0), 25).String(),
		"TFSAZ_LIVE_VM_SUBNET=" + netip.PrefixFrom(b.address(128), 25).String(),
		"TFSAZ_LIVE_GATEWAY=" + b.address(1).String(),
		"TFSAZ_LIVE_ROUTER_IP=" + b.address(2).String(),
		"TFSAZ_LIVE_MOCK_IP=" + b.address(10).String(),
		"TFSAZ_LIVE_DNS_IP=" + b.address(53).String(),
		"TFSAZ_LIVE_HTTP_PORT=" + strconv.Itoa(b.ports.HTTP),
		"TFSAZ_LIVE_HTTPS_PORT=" + strconv.Itoa(b.ports.HTTPS),
		"TFSAZ_LIVE_SSH_PORT=" + strconv.Itoa(b.ports.SSH),
		"TFSAZ_LIVE_JOURNAL_PORT=" + strconv.Itoa(b.ports.Journal),
		"TFSAZ_LIVE_WIREGUARD_PORT=" + strconv.Itoa(b.ports.WireGuard),
	}
}

func (b bedNetwork) corefile(template string) (string, error) {
	return render(template, map[string]string{"mock_ip": b.address(10).String()})
}

func (b bedNetwork) journalURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/requests", b.ports.Journal)
}

type existingNetwork struct {
	prefix        netip.Prefix
	source        string
	project       string
	interfaceName string
}

func selectSlot(slots slotRegistry, instance string, networks []existingNetwork) (int, error) {
	project := "tfsaz-live-" + instance
	held, reserved := slots[instance]
	for _, n := range networks {
		if n.project == project && (!reserved || n.prefix != slotNetwork(held.Slot).subnet) {
			return 0, fmt.Errorf("%s already exists outside this reservation (%s); use its worktree and TFSAZ_LIVE_HOME or choose another instance ID", n.source, n.prefix)
		}
	}
	if reserved {
		subnet := slotNetwork(held.Slot).subnet
		if conflict := overlappingNetwork(subnet, project, networks); conflict != "" {
			return 0, fmt.Errorf("instance %s subnet %s overlaps %s; resolve the conflict or run down to release its slot", instance, subnet, conflict)
		}
		return held.Slot, nil
	}
	used := make(map[int]bool)
	for _, entry := range slots {
		used[entry.Slot] = true
	}
	var conflicts []string
	for slot := range slotCount {
		if used[slot] {
			continue
		}
		subnet := slotNetwork(slot).subnet
		if conflict := overlappingNetwork(subnet, "", networks); conflict != "" {
			conflicts = append(conflicts, fmt.Sprintf("%s overlaps %s", subnet, conflict))
			continue
		}
		return slot, nil
	}
	if len(conflicts) == 0 {
		return 0, fmt.Errorf("all %d live slots are reserved; run down in an unused worktree", slotCount)
	}
	return 0, fmt.Errorf("no free live subnet in 172.30.53.0/24 through 172.30.68.0/24; %s", strings.Join(conflicts, "; "))
}

func overlappingNetwork(subnet netip.Prefix, project string, networks []existingNetwork) string {
	ownBridges := make(map[string]bool)
	for _, n := range networks {
		if project != "" && n.project == project && n.prefix == subnet && n.interfaceName != "" {
			ownBridges[n.interfaceName] = true
		}
	}
	for _, n := range networks {
		if !subnet.Overlaps(n.prefix) {
			continue
		}
		ownNetwork := project != "" && n.project == project && n.prefix == subnet
		ownHostLink := n.project == "" && ownBridges[n.interfaceName] && n.prefix.Bits() >= subnet.Bits()
		if !ownNetwork && !ownHostLink {
			return n.source + " (" + n.prefix.String() + ")"
		}
	}
	return ""
}

func networkSnapshot() ([]existingNetwork, error) {
	networks, err := dockerNetworks()
	if err != nil {
		return nil, fmt.Errorf("inspect Docker networks before starting the bed: %w", err)
	}
	routes, err := hostRoutes()
	if err != nil {
		return nil, fmt.Errorf("inspect host routes before starting the bed: %w", err)
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	networks = append(networks, routes...)
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, addr := range addrs {
			p, err := netip.ParsePrefix(addr.String())
			if err != nil {
				return nil, fmt.Errorf("inspect host interface %s: %w", iface.Name, err)
			}
			if p.Addr().Is4() {
				networks = append(networks, existingNetwork{prefix: p.Masked(), source: "host interface " + iface.Name, interfaceName: iface.Name})
			}
		}
	}
	return networks, nil
}

func dockerNetworks() ([]existingNetwork, error) {
	out, err := output(nil, "docker", "network", "ls", "--quiet")
	if err != nil || out == "" {
		return nil, err
	}
	ids := strings.Fields(out)
	out, err = output(nil, "docker", append([]string{"network", "inspect"}, ids...)...)
	if err != nil {
		return nil, err
	}
	var inspected []struct {
		ID, Name, Driver string
		Labels, Options  map[string]string
		IPAM             struct {
			Config []struct{ Subnet string }
		}
	}
	if err := json.Unmarshal([]byte(out), &inspected); err != nil {
		return nil, err
	}
	var networks []existingNetwork
	for _, n := range inspected {
		project := n.Labels["com.docker.compose.project"]
		if n.Name != project+"_bed" || n.Labels["com.docker.compose.network"] != "bed" {
			project = ""
		}
		bridge := ""
		if n.Driver == "bridge" && project != "" {
			bridge = n.Options["com.docker.network.bridge.name"]
			if bridge == "" && len(n.ID) >= 12 {
				bridge = "br-" + n.ID[:12]
			}
		}
		for _, config := range n.IPAM.Config {
			if config.Subnet == "" {
				continue
			}
			p, err := netip.ParsePrefix(config.Subnet)
			if err != nil {
				return nil, fmt.Errorf("network %s: %w", n.Name, err)
			}
			if p.Addr().Is4() {
				networks = append(networks, existingNetwork{prefix: p.Masked(), source: "Docker network " + n.Name, project: project, interfaceName: bridge})
			}
		}
	}
	return networks, nil
}
