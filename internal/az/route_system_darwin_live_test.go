//go:build darwin

package az

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMacLaunchdSocketLive loads the generated agents into the real GUI
// domain on a free high port, from a temporary directory, and unloads them.
// It changes no trust, hosts or LaunchAgents directory and needs no root.
// Set TFSAZ_LIVE_LAUNCHD=1 for a high port or 443 for the production port.
func TestMacLaunchdSocketLive(t *testing.T) {
	mode := os.Getenv("TFSAZ_LIVE_LAUNCHD")
	if mode != "1" && mode != "443" {
		t.Skip("set TFSAZ_LIVE_LAUNCHD=1 to load temporary LaunchAgents")
	}
	dir, configFile, certFile, keyFile, fp, alias := routeSystemTestAssets(t)
	exe := filepath.Join(dir, "tfsaz")
	build := exec.Command("go", "build", "-o", exe, "github.com/toolspell/tfsaz")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	p := routeSystemPlan{
		id: routeSystemID(configFile), dir: dir, configFile: configFile,
		certFile: certFile, keyFile: keyFile, leafSHA256: fp, aliases: []string{alias}, exe: exe,
	}
	var err error
	if p.port, err = routeSystemServicePortForTest(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if mode == "443" {
		p.port = 443
		if err := checkRouteSystem443(ctx, p); err != nil {
			t.Skip(err)
		}
	}
	plistDir := t.TempDir()
	for _, l := range macListeners {
		path := filepath.Join(plistDir, macAgentLabel(p.id, l)+".plist")
		if err := os.WriteFile(path, []byte(macAgentPlist(p, l)), 0o644); err != nil {
			t.Fatal(err)
		}
		label := macAgentLabel(p.id, l)
		t.Cleanup(func() { macBootout(context.Background(), label) })
		if _, err := runRouteSystemCommand(ctx, nil, "/bin/launchctl", "bootstrap", macGUIDomain(), path); err != nil {
			t.Fatal(err)
		}
	}
	if err := waitRouteSystemService(ctx, p, 15*time.Second); err != nil {
		log, _ := os.ReadFile(filepath.Join(dir, "redirect.log"))
		t.Fatalf("%v\nlog: %s", err, log)
	}
	leaf, err := readManagedRouteLeaf(certFile, map[string]bool{alias: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfigAt(configFile)
	if err != nil {
		t.Fatal(err)
	}
	records, err := readSetupRegistry(setupRegistryForConfig(configFile))
	if err != nil {
		t.Fatal(err)
	}
	check := routeCheck{cfg: cfg, records: records, leaf: leaf, port: p.port, now: time.Now, dial: (&net.Dialer{}).DialContext}
	target, err := check.targetForAlias(alias)
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"127.0.0.1", "::1"} {
		if err := check.probe(ctx, target, net.JoinHostPort(ip, strconv.Itoa(p.port))); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range macListeners {
		out, err := runRouteSystemCommand(ctx, nil, "/bin/launchctl", "print", macGUIDomain()+"/"+macAgentLabel(p.id, l))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", l.suffix, macLaunchdSummary(string(out)))
	}
	log, _ := os.ReadFile(filepath.Join(dir, "redirect.log"))
	t.Logf("redirect.log: %q", log)
	for _, l := range macListeners {
		if err := macBootout(ctx, macAgentLabel(p.id, l)); err != nil {
			t.Fatal(err)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1"} {
		if connected, _ := routeSystemPeer(ctx, net.JoinHostPort(ip, strconv.Itoa(p.port)), alias); connected {
			t.Fatalf("%s port still open after bootout", ip)
		}
	}
}

func macLaunchdSummary(out string) string {
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		for _, p := range []string{"state =", "pid =", "last exit code", "runs =", "stderr path", "inetd", "sockets"} {
			if strings.HasPrefix(s, p) {
				keep = append(keep, s)
			}
		}
	}
	return strings.Join(keep, "; ")
}

// routeSystemServicePortForTest finds a high port free on both loopback families.
func routeSystemServicePortForTest() (int, error) {
	for range 20 {
		ln4, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := ln4.Addr().(*net.TCPAddr).Port
		ln6, err := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(port)))
		ln4.Close()
		if err == nil {
			ln6.Close()
			return port, nil
		}
	}
	return 0, os.ErrNotExist
}
