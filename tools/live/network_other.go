//go:build !darwin

package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"runtime"
	"strings"
)

func hostRoutes() ([]existingNetwork, error) {
	var table []struct{ Dst, Dev string }
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
			table = append(table, struct{ Dst, Dev string }{Dst: dst})
		}
	default:
		return nil, fmt.Errorf("live Docker networking is not supported on %s", runtime.GOOS)
	}
	var routes []existingNetwork
	for _, row := range table {
		if row.Dst == "default" || row.Dst == "0.0.0.0/0" {
			continue
		}
		if !strings.Contains(row.Dst, "/") {
			row.Dst += "/32"
		}
		p, err := netip.ParsePrefix(row.Dst)
		if err != nil {
			return nil, err
		}
		routes = append(routes, existingNetwork{prefix: p.Masked(), source: "host route", interfaceName: row.Dev})
	}
	return routes, nil
}
