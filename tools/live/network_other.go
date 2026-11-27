//go:build !darwin

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strings"
)

type routeRow struct{ Dst, Dev string }

func hostRoutes(_ []net.Interface) ([]existingNetwork, error) {
	var table []routeRow
	switch runtime.GOOS {
	case "linux":
		out, err := output(nil, "ip", "-j", "-4", "route", "show", "table", "all")
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(out), &table); err != nil {
			return nil, err
		}
	case "windows":
		out, err := output(nil, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			"Get-NetRoute -AddressFamily IPv4 -ErrorAction Stop | Select-Object -ExpandProperty DestinationPrefix")
		if err != nil {
			return nil, err
		}
		for _, dst := range strings.Fields(out) {
			table = append(table, routeRow{Dst: dst})
		}
	default:
		return nil, fmt.Errorf("live Docker networking is not supported on %s", runtime.GOOS)
	}
	return parseHostRoutes(table)
}

func parseHostRoutes(table []routeRow) ([]existingNetwork, error) {
	var routes []existingNetwork
	for _, row := range table {
		if row.Dst == "default" {
			continue
		}
		if !strings.Contains(row.Dst, "/") {
			row.Dst += "/32"
		}
		p, err := netip.ParsePrefix(row.Dst)
		if err != nil {
			return nil, err
		}
		if p.Bits() >= 8 {
			routes = append(routes, existingNetwork{prefix: p.Masked(), source: "host route", interfaceName: row.Dev})
		}
	}
	return routes, nil
}
