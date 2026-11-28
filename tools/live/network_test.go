package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestLiveNetworks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T)
	}{
		{"overlap", checkNetworkOverlap},
		{"Docker networks", checkDockerNetworks},
		{"host routes", checkHostRoutes},
	} {
		t.Run(tc.name, tc.check)
	}
}

func checkNetworkOverlap(t *testing.T) {
	subnet := slotNetwork(1).subnet
	orbBridge := existingNetwork{prefix: subnet, source: "host interface bridge101", interfaceName: "bridge101", interfaceAddress: subnet.Addr()}
	for _, tc := range []struct {
		name, prefix, project, device string
		wantConflict                  bool
		orbStack                      bool
	}{
		{name: "parent route", prefix: "172.30.0.0/16", wantConflict: true},
		{name: "host address", prefix: "172.30.54.100/32", wantConflict: true},
		{name: "foreign Docker network", prefix: "172.30.54.0/24", project: "tfsaz-live-other", wantConflict: true},
		{name: "Docker Desktop without a host bridge", prefix: "172.30.54.0/24", project: "tfsaz-live-first"},
		{name: "wrong own subnet", prefix: "172.30.54.0/25", project: "tfsaz-live-first", wantConflict: true},
		{name: "own bridge address", prefix: "172.30.54.1/32", device: "br-own"},
		{name: "route on another interface", prefix: "172.30.54.1/32", device: "utun0", wantConflict: true},
		{name: "OrbStack subnet route", prefix: "172.30.54.0/24", device: "bridge101", orbStack: true},
		{name: "OrbStack neighbor route", prefix: "172.30.54.2/32", device: "bridge101", orbStack: true},
		{name: "foreign exact route alongside OrbStack", prefix: "172.30.54.0/24", device: "utun0", orbStack: true, wantConflict: true},
		{name: "foreign narrower route alongside OrbStack", prefix: "172.30.54.128/25", device: "utun0", orbStack: true, wantConflict: true},
		{name: "foreign route via gateway on OrbStack bridge", prefix: "172.30.54.128/25", orbStack: true, wantConflict: true},
		{name: "adjacent subnet", prefix: "172.30.55.0/24"},
		{name: "IPv6", prefix: "fd00::/64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			networks := []existingNetwork{
				{prefix: subnet, source: "own Docker network", project: "tfsaz-live-first", interfaceName: "br-own"},
				{prefix: netip.MustParsePrefix(tc.prefix), source: tc.name, project: tc.project, interfaceName: tc.device},
			}
			if tc.orbStack {
				networks = append(networks, orbBridge)
			}
			got := overlappingNetwork(subnet, "tfsaz-live-first", networks)
			if (got != "") != tc.wantConflict {
				t.Errorf("overlap = %q; want conflict %t", got, tc.wantConflict)
			}
		})
	}
}

func checkDockerNetworks(t *testing.T) {
	for _, tc := range []struct {
		name, json string
		want       []existingNetwork
		wantError  string
	}{
		{
			name: "owned bridge, legacy bed and unlabelled lookalike",
			json: `[
				{"ID":"123456789abcde","Name":"tfsaz-live-first_bed","Driver":"bridge",
				 "Labels":{"com.docker.compose.project":"tfsaz-live-first","com.docker.compose.network":"bed"},
				 "IPAM":{"Config":[{"Subnet":"172.30.54.0/24"},{"Subnet":"fd00::/64"}]}},
				{"Name":"tfsaz-live_bed","Driver":"bridge","Labels":{"com.docker.compose.project":"tfsaz-live","com.docker.compose.network":"bed"},
				 "Options":{"com.docker.network.bridge.name":"br-legacy"},"IPAM":{"Config":[{"Subnet":"172.30.53.0/24"}]}},
				{"Name":"tfsaz-live-second_bed","IPAM":{"Config":[{"Subnet":"172.30.55.0/24"}]}}
			]`,
			want: []existingNetwork{
				{prefix: netip.MustParsePrefix("172.30.54.0/24"), source: "Docker network tfsaz-live-first_bed", project: "tfsaz-live-first", interfaceName: "br-123456789abc"},
				{prefix: netip.MustParsePrefix("172.30.53.0/24"), source: "Docker network tfsaz-live_bed", project: "tfsaz-live", interfaceName: "br-legacy"},
				{prefix: netip.MustParsePrefix("172.30.55.0/24"), source: "Docker network tfsaz-live-second_bed"},
			},
		},
		{name: "invalid JSON", json: "{", wantError: "unexpected end"},
		{name: "invalid subnet", json: `[{"Name":"invalid","IPAM":{"Config":[{"Subnet":"oops"}]}}]`, wantError: "network invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDockerNetworks(tc.json)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("parseDockerNetworks = %v, %v; want %q", got, err, tc.wantError)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("parseDockerNetworks = %v, %v; want %v", got, err, tc.want)
			}
			slot, err := selectSlot(slotRegistry{"first": {Slot: 1}}, "second", got, freeHostPorts)
			if err != nil || slot != 3 {
				t.Errorf("slot with a legacy bed = %d, %v; want 3", slot, err)
			}
		})
	}
}

func TestLiveSlots(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T)
	}{
		{"reservations", checkSlotReservations},
		{"selection", checkSlotSelection},
	} {
		t.Run(tc.name, tc.check)
	}
}

func checkSlotReservations(t *testing.T) {
	home := t.TempDir()
	first := &env{home: home, root: t.TempDir(), instance: "first"}
	second := &env{home: home, root: t.TempDir(), instance: "second"}
	conflict := []existingNetwork{{prefix: netip.MustParsePrefix("172.30.53.0/24"), source: "Docker network elsewhere"}}
	for _, tc := range []struct {
		name string
		env  *env
		want int
	}{
		{"skip overlapping slot", first, 1},
		{"second instance has its own slot", second, 2},
		{"reuse reservation", &env{home: home, root: first.root, instance: first.instance}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.env.reserveNetwork(conflict, freeHostPorts); err != nil || *tc.env.network != slotNetwork(tc.want) {
				t.Fatalf("reservation = %v, %v; want slot %d", tc.env.network, err, tc.want)
			}
		})
	}
	t.Run("reject a duplicate ID from another worktree", func(t *testing.T) {
		other := &env{home: home, root: second.root, instance: first.instance}
		if err := other.loadNetwork(); err == nil || !strings.Contains(err.Error(), first.root) {
			t.Fatalf("loadNetwork = %v; want ownership error for %s", err, first.root)
		}
	})
	t.Run("reclaim a released slot", func(t *testing.T) {
		for _, e := range []*env{first, second} {
			if err := e.releaseNetwork(); err != nil {
				t.Fatal(err)
			}
		}
		if err := second.reserveNetwork(conflict, freeHostPorts); err != nil || *second.network != slotNetwork(1) {
			t.Fatalf("reservation = %v, %v; want slot 1", second.network, err)
		}
	})
}

func checkSlotSelection(t *testing.T) {
	own := existingNetwork{prefix: slotNetwork(0).subnet, source: "Docker network tfsaz-live-first_bed", project: "tfsaz-live-first"}
	route := existingNetwork{prefix: netip.MustParsePrefix("172.30.0.0/16"), source: "host route"}
	reserved := slotRegistry{"first": {Slot: 0}}
	busy := slotNetwork(0).ports
	probe := func(ports bedPorts) error {
		if ports == busy {
			return busyPort("UDP", ports.WireGuard, errors.New("address already in use"))
		}
		return nil
	}
	full := make(slotRegistry)
	for n := range slotCount {
		full[fmt.Sprint(n)] = reservation{Slot: n}
	}
	for _, tc := range []struct {
		name      string
		slots     slotRegistry
		networks  []existingNetwork
		probe     func(bedPorts) error
		want      int
		wantError string
	}{
		{name: "existing project without reservation", networks: []existingNetwork{own}, wantError: "outside this reservation"},
		{name: "reserved slot now overlaps a route", slots: reserved, networks: []existingNetwork{own, route}, wantError: "host route"},
		{name: "all slots overlap a route", networks: []existingNetwork{route}, wantError: "no free live slot"},
		{name: "skip busy slot", probe: probe, want: 1},
		{name: "reserved busy port", slots: reserved, probe: probe, wantError: "lsof -nP -i :51820"},
		{name: "all slots reserved", slots: full, probe: probe, wantError: "go run ./tools/live prune"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := tc.probe
			if probe == nil {
				probe = freeHostPorts
			}
			got, err := selectSlot(tc.slots, "first", tc.networks, probe)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Errorf("selectSlot = %d, %v; want %q", got, err, tc.wantError)
				}
			} else if got != tc.want || err != nil {
				t.Errorf("selectSlot = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestHostPortProbe(t *testing.T) {
	for _, tc := range []struct {
		name, network, address string
		set                    func(*bedPorts, int)
	}{
		{"HTTP", "tcp4", "127.0.0.1:0", func(p *bedPorts, port int) { p.HTTP = port }},
		{"HTTPS", "tcp4", "127.0.0.1:0", func(p *bedPorts, port int) { p.HTTPS = port }},
		{"SSH", "tcp4", "127.0.0.1:0", func(p *bedPorts, port int) { p.SSH = port }},
		{"journal", "tcp4", "127.0.0.1:0", func(p *bedPorts, port int) { p.Journal = port }},
		{"WireGuard", "udp", ":0", func(p *bedPorts, port int) { p.WireGuard = port }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var port int
			var closeSocket func() error
			if tc.network == "tcp4" {
				l, err := net.Listen(tc.network, tc.address)
				if err != nil {
					t.Fatal(err)
				}
				port, closeSocket = l.Addr().(*net.TCPAddr).Port, l.Close
			} else {
				l, err := net.ListenPacket(tc.network, tc.address)
				if err != nil {
					t.Fatal(err)
				}
				port, closeSocket = l.LocalAddr().(*net.UDPAddr).Port, l.Close
			}
			defer func() { _ = closeSocket() }()
			var ports bedPorts
			tc.set(&ports, port)
			if err := probeHostPorts(ports, false, false); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("lsof -nP -i :%d", port)) {
				t.Errorf("probeHostPorts = %v; want busy port %d", err, port)
			}
			if err := probeHostPorts(ports, tc.network == "tcp4", tc.network == "udp"); err != nil {
				t.Errorf("probeHostPorts rejected a port held by the running project: %v", err)
			}
			if err := closeSocket(); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := probeHostPorts(ports, false, false); err != nil {
					t.Fatalf("probeHostPorts after socket close = %v", err)
				}
			}
		})
	}
}

func checkRouteOverlap(t *testing.T, routes []existingNetwork) {
	t.Helper()
	var prefixes []string
	for _, route := range routes {
		prefixes = append(prefixes, route.prefix.String())
	}
	want := []string{"10.0.0.0/8", "172.30.54.0/24", "192.0.2.123/32"}
	if !slices.Equal(prefixes, want) {
		t.Errorf("host routes = %v; want %v", prefixes, want)
	}
	if got := overlappingNetwork(slotNetwork(0).subnet, "", routes); got != "" {
		t.Errorf("slot 0 overlaps %q", got)
	}
	if got := overlappingNetwork(slotNetwork(1).subnet, "", routes); got != "host route (172.30.54.0/24)" {
		t.Errorf("slot 1 overlap = %q", got)
	}
}
