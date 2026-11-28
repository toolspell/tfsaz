//go:build !darwin

package main

import (
	"runtime"
	"slices"
	"testing"
)

func checkHostRoutes(t *testing.T) {
	old := output
	t.Cleanup(func() { output = old })
	output = func(_ []string, bin string, args ...string) (string, error) {
		switch runtime.GOOS {
		case "linux":
			if bin != "ip" || !slices.Equal(args, []string{"-j", "-4", "route", "show", "table", "all"}) {
				t.Fatalf("unexpected command: %s %q", bin, args)
			}
			return `[{"dst":"default"},{"dst":"0.0.0.0/0"},{"dst":"0.0.0.0/1"},{"dst":"128.0.0.0/1"},
				{"dst":"10.0.0.0/8"},{"dst":"172.30.54.1/24","dev":"eth0"},{"dst":"192.0.2.123"}]`, nil
		case "windows":
			if bin != "powershell.exe" {
				t.Fatalf("unexpected command: %s %q", bin, args)
			}
			return "0.0.0.0/0\r\n0.0.0.0/1\r\n128.0.0.0/1\r\n10.0.0.0/8\r\n172.30.54.1/24\r\n192.0.2.123/32\r\n", nil
		default:
			t.Fatal("unexpected route command")
			return "", nil
		}
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("host route inspection is unavailable on this platform")
	}
	got, err := hostRoutes(nil)
	if err != nil {
		t.Fatal(err)
	}
	checkRouteOverlap(t, got)
}
