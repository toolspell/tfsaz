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

func TestDockerNetworks(t *testing.T) {
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

func TestLiveSlotHostPorts(t *testing.T) {
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
		want      int
		wantError string
	}{
		{"skip busy slot", nil, 1, ""},
		{"reserved busy port", slotRegistry{"first": {Slot: 0}}, 0, "lsof -nP -i :51820"},
		{"all slots reserved", full, 0, "go run ./tools/live prune"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectSlot(tc.slots, "first", nil, probe)
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
