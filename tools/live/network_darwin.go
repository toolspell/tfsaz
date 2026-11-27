package main

import (
	"fmt"
	"net"
	"net/netip"

	"golang.org/x/net/route"
	"golang.org/x/sys/unix"
)

func hostRoutes() ([]existingNetwork, error) {
	rib, err := route.FetchRIB(unix.AF_INET, route.RIBTypeRoute, 0)
	if err != nil {
		return nil, err
	}
	messages, err := route.ParseRIB(route.RIBTypeRoute, rib)
	if err != nil {
		return nil, err
	}
	var routes []existingNetwork
	for _, msg := range messages {
		m, ok := msg.(*route.RouteMessage)
		if !ok || len(m.Addrs) <= unix.RTAX_NETMASK {
			continue
		}
		dst, ok := m.Addrs[unix.RTAX_DST].(*route.Inet4Addr)
		if !ok {
			continue
		}
		bits := 32
		if mask, ok := m.Addrs[unix.RTAX_NETMASK].(*route.Inet4Addr); ok {
			var size int
			bits, size = net.IPMask(mask.IP[:]).Size()
			if size != 32 {
				return nil, fmt.Errorf("invalid route mask for %s", netip.AddrFrom4(dst.IP))
			}
		} else if m.Flags&unix.RTF_HOST == 0 {
			if dst.IP != [4]byte{} {
				return nil, fmt.Errorf("missing route mask for %s", netip.AddrFrom4(dst.IP))
			}
			continue
		}
		if bits != 0 {
			p := netip.PrefixFrom(netip.AddrFrom4(dst.IP), bits).Masked()
			routes = append(routes, existingNetwork{prefix: p, source: "host route"})
		}
	}
	return routes, nil
}
