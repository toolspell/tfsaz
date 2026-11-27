package main

import (
	"net"
	"net/netip"
	"testing"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func TestHostRoutes(t *testing.T) {
	var messages []route.Message
	for _, s := range []string{"0.0.0.0/0", "0.0.0.0/1", "128.0.0.0/1", "10.0.0.0/8", "172.30.54.1/24", "192.0.2.123/32"} {
		p := netip.MustParsePrefix(s)
		addrs := make([]route.Addr, unix.RTAX_NETMASK+1)
		addrs[unix.RTAX_DST] = &route.Inet4Addr{IP: p.Addr().As4()}
		flags := 0
		if p.Bits() == 32 {
			flags = unix.RTF_HOST
		} else {
			addrs[unix.RTAX_NETMASK] = &route.Inet4Addr{IP: [4]byte(net.CIDRMask(p.Bits(), 32))}
		}
		if p.Bits() == 32 {
			flags |= unix.RTF_GATEWAY
		}
		messages = append(messages, &route.RouteMessage{Addrs: addrs, Flags: flags, Index: 42})
	}
	got, err := parseRouteMessages(messages, []net.Interface{{Index: 42, Name: "bridge101"}})
	if err != nil {
		t.Fatal(err)
	}
	checkRouteOverlap(t, got)
	if got[1].interfaceName != "bridge101" || got[2].interfaceName != "" {
		t.Errorf("host routes = %+v; want bridge101 only for directly connected routes", got)
	}
}
