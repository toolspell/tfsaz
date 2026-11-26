//go:build darwin

package az

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// macOS: two on-demand LaunchAgents in the user's GUI domain. launchd binds
// one listening socket each on 127.0.0.1:443 and [::1]:443 and passes it in
// inetdCompatibility Wait mode to TFSaz, which runs as the user. Nothing
// forwards or rewrites ports, no root job exists, and the sockets close when
// the user logs out. The only administrator step edits /etc/hosts. The leaf
// gets SSL trust in the user's login keychain with one exact hostname rule per
// alias (see route_system_darwin_trust.go).

type macListener struct {
	suffix string // label suffix
	node   string // literal loopback address launchd binds
	family string // launchd SockFamily
}

var macListeners = []macListener{
	{"ipv4", "127.0.0.1", "IPv4"},
	{"ipv6", "::1", "IPv6"},
}

func macAgentLabel(id string, l macListener) string {
	return "tfsaz.route-redirect." + id + "." + l.suffix
}

func macAgentPlistPath(id string, l macListener) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", macAgentLabel(id, l)+".plist"), nil
}

func macGUIDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func routeSystemLogHint(p routeSystemPlan) string {
	return "see " + filepath.Join(p.dir, "redirect.log") + " and launchctl print " + macGUIDomain() + "/" + macAgentLabel(p.id, macListeners[0])
}

func routeSystemInstallOS(ctx context.Context, p routeSystemPlan) error {
	hosts, err := readRouteHosts()
	if err != nil {
		return err
	}
	if err := routeHostsConflict(hosts, p.id, p.aliases); err != nil {
		return err
	}
	// Release this installation's own sockets first, so any remaining listener
	// on port 443 belongs to another program.
	for _, l := range macListeners {
		if err := macBootout(ctx, macAgentLabel(p.id, l)); err != nil {
			return err
		}
	}
	if err := checkRouteSystem443(ctx, p); err != nil {
		return err
	}
	for _, l := range macListeners {
		if err := installMacAgent(ctx, p, l); err != nil {
			return err
		}
	}
	// The probe also triggers on-demand activation of both agents.
	if err := waitRouteSystemService(ctx, p, 15*time.Second); err != nil {
		return err
	}
	if err := installMacTrust(ctx, p); err != nil {
		return err
	}
	if editRouteHosts(hosts, p.id, p.aliases, true) == hosts {
		return nil // already in place; no administrator prompt on retry
	}
	return runRouteSystemPrivileged(ctx, routeHostsShell(routeHostsPath(), p.id, p.aliases, true))
}

func routeSystemRestoreOS(ctx context.Context, p routeSystemPlan) error {
	var errs []error
	for _, l := range macListeners {
		if err := removeMacAgent(ctx, p.id, l); err != nil {
			errs = append(errs, err)
		}
	}
	hosts, err := readRouteHosts()
	if err != nil {
		errs = append(errs, err)
	}
	if err != nil || routeHostsHasMarker(hosts, p.id) {
		if err := runRouteSystemPrivileged(ctx, routeHostsShell(routeHostsPath(), p.id, nil, false)); err != nil {
			errs = append(errs, err)
		}
	}
	if err := removeMacTrust(ctx, p); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func macAgentPlist(p routeSystemPlan, l macListener) string {
	log := filepath.Join(p.dir, "redirect.log")
	port := strconv.Itoa(p.port)
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + macAgentLabel(p.id, l) + `</string>
	<key>ProgramArguments</key>
	<array>
` + plistStrings([]string{p.exe, "route", "redirect", "--cert", p.certFile, "--key", p.keyFile, "--port", port, routeInheritedSocketFlag}) + `	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>TFSAZ_CONFIG</key>
		<string>` + xmlText(p.configFile) + `</string>
	</dict>
	<key>Sockets</key>
	<dict>
		<key>Listener</key>
		<dict>
			<key>SockType</key>
			<string>stream</string>
			<key>SockFamily</key>
			<string>` + l.family + `</string>
			<key>SockNodeName</key>
			<string>` + l.node + `</string>
			<key>SockServiceName</key>
			<string>` + port + `</string>
		</dict>
	</dict>
	<key>inetdCompatibility</key>
	<dict>
		<key>Wait</key>
		<true/>
	</dict>
	<key>StandardErrorPath</key>
	<string>` + xmlText(log) + `</string>
</dict>
</plist>
`
}

func plistStrings(values []string) string {
	var b strings.Builder
	for _, v := range values {
		b.WriteString("\t\t<string>" + xmlText(v) + "</string>\n")
	}
	return b.String()
}

// macBootout unloads a job and waits until launchd has released it, because
// bootout finishes asynchronously and its sockets stay bound until then.
func macBootout(ctx context.Context, label string) error {
	target := macGUIDomain() + "/" + label
	if _, err := runRouteSystemCommand(ctx, nil, "/bin/launchctl", "print", target); err != nil {
		return nil // not loaded
	}
	// The polling below decides; bootout's error only explains a timeout.
	_, bootoutErr := runRouteSystemCommand(ctx, nil, "/bin/launchctl", "bootout", target)
	for range 20 {
		if _, err := runRouteSystemCommand(ctx, nil, "/bin/launchctl", "print", target); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.Join(fmt.Errorf("launchd did not unload %s", target), bootoutErr)
}

func installMacAgent(ctx context.Context, p routeSystemPlan, l macListener) error {
	path, err := macAgentPlistPath(p.id, l)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(path, []byte(macAgentPlist(p, l)), 0o644); err != nil {
		return fmt.Errorf("write the LaunchAgent: %w", err)
	}
	if _, err := runRouteSystemCommand(ctx, nil, "/bin/launchctl", "bootstrap", macGUIDomain(), path); err != nil {
		return fmt.Errorf("start the LaunchAgent for %s port %d (needs a logged-in desktop session and a free port): %w", l.node, p.port, err)
	}
	return nil
}

func removeMacAgent(ctx context.Context, id string, l macListener) error {
	path, err := macAgentPlistPath(id, l)
	if err != nil {
		return err
	}
	if err := macBootout(ctx, macAgentLabel(id, l)); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the LaunchAgent: %w", err)
	}
	return nil
}
