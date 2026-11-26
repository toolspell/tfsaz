//go:build darwin

package az

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The flow runs against faked launchctl, security and sudo; it changes
// nothing outside temporary directories.
func TestRouteSystemDarwinInstallAndRestore(t *testing.T) {
	dir, configFile, certFile, keyFile, fp, alias := routeSystemTestAssets(t)
	if connected, _ := routeSystemPeer(context.Background(), "127.0.0.1:443", alias); connected {
		t.Skip("something listens on loopback port 443 on this machine")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	p, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, fp, []string{alias})
	if err != nil {
		t.Fatal(err)
	}
	if p.port != 443 {
		t.Fatalf("service port %d, want 443", p.port)
	}
	// The faked launchd "binds" a free high port instead of 443.
	p.port = routeSystemTestService(t, certFile, keyFile)
	pemData, _ := os.ReadFile(certFile)
	trusted, inKeychain := false, false
	loaded := map[string]bool{}
	calls := fakeRouteSystemCommands(t, func(c routeSystemCall) ([]byte, error) {
		switch {
		case c.name == "/bin/launchctl" && c.args[0] == "print":
			if loaded[c.args[1]] {
				return nil, nil
			}
			return nil, errors.New("not loaded")
		case c.name == "/bin/launchctl" && c.args[0] == "bootstrap":
			label := strings.TrimSuffix(filepath.Base(c.args[2]), ".plist")
			loaded[c.args[1]+"/"+label] = true
		case c.name == "/bin/launchctl" && c.args[0] == "bootout":
			delete(loaded, c.args[1])
		case c.name == macSecurity && c.args[0] == "verify-cert":
			if trusted {
				return nil, nil
			}
			return nil, errors.New("not trusted")
		case c.name == macSecurity && c.args[0] == "add-trusted-cert" && slices.Contains(c.args, "-o"):
			out := c.args[slices.Index(c.args, "-o")+1]
			return nil, os.WriteFile(out, []byte(macTestTrustSettings(alias)), 0o600)
		case c.name == macSecurity && c.args[0] == "add-trusted-cert":
			trusted, inKeychain = true, true
		case c.name == macSecurity && c.args[0] == "remove-trusted-cert":
			trusted = false
		case c.name == macSecurity && c.args[0] == "find-certificate":
			if inKeychain {
				return pemData, nil
			}
			return nil, errors.New("not found")
		case c.name == macSecurity && c.args[0] == "delete-certificate":
			inKeychain = false
		}
		return nil, nil
	})
	if err := routeSystemInstallOS(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Fatalf("want two loaded agents, got %v", loaded)
	}
	for _, l := range macListeners {
		path, _ := macAgentPlistPath(p.id, l)
		agent, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"<string>" + p.exe + "</string>", "<string>" + routeInheritedSocketFlag + "</string>",
			"<key>SockNodeName</key>\n\t\t\t<string>" + l.node + "</string>",
			"<key>SockServiceName</key>\n\t\t\t<string>" + strconv.Itoa(p.port) + "</string>",
			"<key>inetdCompatibility</key>", "<key>TFSAZ_CONFIG</key>", "<string>" + configFile + "</string>",
		} {
			if !strings.Contains(string(agent), want) {
				t.Errorf("%s lacks %q", l.suffix, want)
			}
		}
		for _, forbidden := range []string{"UserName", "KeepAlive", "RunAtLoad", "0.0.0.0"} {
			if strings.Contains(string(agent), forbidden) {
				t.Errorf("%s contains %s", l.suffix, forbidden)
			}
		}
	}
	var sudo []routeSystemCall
	var adds [][]string
	for _, c := range *calls {
		if strings.HasSuffix(c.name, "/sudo") {
			sudo = append(sudo, c)
		}
		if c.name == macSecurity && c.args[0] == "add-trusted-cert" {
			adds = append(adds, c.args)
		}
	}
	if len(adds) != 2 || !slices.Contains(adds[0], "-o") || slices.Contains(adds[0], "-k") || !slices.Contains(adds[1], "-k") {
		t.Fatalf("want a rendered trust check, then one keychain change: %v", adds)
	}
	if want := macTrustArgs([]string{alias}); !slices.Equal(adds[1][:len(want)], want) {
		t.Fatalf("trust is not scoped to SSL for the alias: %v", adds[1])
	}
	if len(sudo) != 1 {
		t.Fatalf("want one administrator step, got %d", len(sudo))
	}
	script := sudo[0].args[len(sudo[0].args)-1]
	if !strings.Contains(script, "127.0.0.1 "+alias+" # tfsaz-route "+p.id) {
		t.Error("administrator script lacks the hosts entry")
	}
	// Root only edits the hosts file with system programs.
	for _, forbidden := range []string{p.exe, dir, home, configFile, "pfctl", "launchctl", "LaunchDaemons"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("administrator script references %s", forbidden)
		}
	}

	*calls = nil
	r, err := prepareRouteSystem(false, dir, configFile, certFile, keyFile, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := routeSystemRestoreOS(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	for _, l := range macListeners {
		path, _ := macAgentPlistPath(p.id, l)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s LaunchAgent left behind", l.suffix)
		}
	}
	if len(loaded) != 0 || trusted || inKeychain {
		t.Fatalf("agents %v, trust %v or keychain copy %v left behind", loaded, trusted, inKeychain)
	}
	for _, c := range *calls {
		if strings.HasSuffix(c.name, "/sudo") {
			t.Fatal("restore asked for administrator rights with nothing to remove")
		}
	}
}

func macTestTrustSettings(hosts ...string) string {
	var rules strings.Builder
	for _, h := range hosts {
		rules.WriteString(`<dict><key>kSecTrustSettingsPolicy</key><data>KoZIhvdjZAED</data>` +
			`<key>kSecTrustSettingsPolicyName</key><string>sslServer</string>` +
			`<key>kSecTrustSettingsPolicyString</key><string>` + h + `</string></dict>`)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>trustList</key><dict><key>0BF2</key><dict>
<key>issuerName</key><data>MBE=</data><key>modDate</key><date>2026-11-25T22:29:24Z</date>
<key>trustSettings</key><array>` + rules.String() + `</array></dict></dict>
<key>trustVersion</key><integer>1</integer></dict></plist>`
}

func TestValidateMacTrustSettings(t *testing.T) {
	a, b := routeTestAlias, "tfsaz-ffffffffffffffffffffffff.visualstudio.com"
	if err := validateMacTrustSettings([]byte(macTestTrustSettings(a, b, a, b)), []string{a, b}); err != nil {
		t.Fatalf("exact repeated rules: %v", err)
	}
	unscoped := strings.Replace(macTestTrustSettings(a), "<key>kSecTrustSettingsPolicyString</key><string>"+a+"</string>", "", 1)
	allowed := strings.Replace(macTestTrustSettings(a), "</dict></array>", "<key>kSecTrustSettingsAllowedError</key><integer>-2147408896</integer></dict></array>", 1)
	otherPolicy := strings.Replace(macTestTrustSettings(a), "KoZIhvdjZAED", "KoZIhvdjZAEC", 1)
	for name, doc := range map[string]string{
		"missing alias": macTestTrustSettings(a),
		"foreign host":  macTestTrustSettings(a, "example.com"),
		"unscoped":      unscoped,
		"allowed error": allowed,
		"other policy":  otherPolicy,
		"no rules":      macTestTrustSettings(),
		"not a plist":   "<html/>",
	} {
		aliases := []string{a, b}
		if name != "missing alias" && name != "foreign host" {
			aliases = []string{a}
		}
		if err := validateMacTrustSettings([]byte(doc), aliases); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Renders trust settings with the real system tool into temporary files only.
// This touches no trust domain or keychain and shows no prompt.
func TestMacTrustExpressionNative(t *testing.T) {
	if _, err := os.Stat(macSecurity); err != nil {
		t.Skip("no security tool")
	}
	other := "tfsaz-ffffffffffffffffffffffff.visualstudio.com"
	aliases := []string{routeTestAlias, other}
	certFile, _, _ := writeRedirectTestCert(t, t.TempDir(), redirectTestCert{names: aliases})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := checkMacTrustExpression(ctx, certFile, aliases); err != nil {
		t.Fatal(err)
	}
}

func TestCheckInheritedListener(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:443": true, "[::1]:443": true,
		"127.0.0.2:443": false, "0.0.0.0:443": false, "[::]:443": false, "192.0.2.1:443": false, "127.0.0.1:8443": false,
	} {
		tcp, err := net.ResolveTCPAddr("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkInheritedListener(tcp, 443); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
}

// TestInheritedRedirectServes starts this test binary as launchd would in
// inetd Wait mode: the listening socket on stdin, stdout and stderr.
func TestInheritedRedirectServes(t *testing.T) {
	if os.Getenv("TFSAZ_TEST_INHERITED") == "1" {
		port, _ := strconv.Atoi(os.Getenv("TFSAZ_TEST_PORT"))
		err := serveRouteRedirectInherited(context.Background(), os.Getenv("TFSAZ_TEST_CERT"), os.Getenv("TFSAZ_TEST_KEY"), port, os.Stderr)
		os.Exit(map[bool]int{true: 3, false: 0}[err != nil])
	}
	configFile, _, alias, collection := redirectTestSetup(t)
	certFile, keyFile, _ := writeRedirectTestCert(t, t.TempDir(), redirectTestCert{names: []string{alias}})
	start := func(declaredPort int) (*exec.Cmd, int) {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		file, err := ln.(*net.TCPListener).File()
		closeQuietly(ln)
		if err != nil {
			t.Fatal(err)
		}
		defer closeQuietly(file)
		if declaredPort == 0 {
			declaredPort = port
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestInheritedRedirectServes$")
		cmd.Env = append(os.Environ(), "TFSAZ_TEST_INHERITED=1", "TFSAZ_CONFIG="+configFile,
			"TFSAZ_TEST_CERT="+certFile, "TFSAZ_TEST_KEY="+keyFile, "TFSAZ_TEST_PORT="+strconv.Itoa(declaredPort))
		cmd.Stdin, cmd.Stdout, cmd.Stderr = file, file, file
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd, port
	}

	cmd, port := start(0)
	// The child may have exited already, and Wait reports the kill itself.
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
			},
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, ServerName: alias},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	// The socket listens before the child starts, so the kernel queues this
	// connection until the child has validated its configuration and accepts.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+alias+":"+strconv.Itoa(port)+"/Project/_git/Repo", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	closeQuietly(resp.Body)
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), collection) {
		t.Fatalf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// A socket on another port than the plist declares is refused.
	bad, _ := start(1)
	if err := bad.Wait(); err == nil {
		t.Fatal("child served a socket on an unexpected port")
	}
}

func TestMacTrustRemovalWithOnlyRecoveryCertificate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, file, _, _, _, alias := routeSystemTestAssets(t)
	cert, key, der := writeRedirectTestCert(t, dir, redirectTestCert{names: []string{alias}, notBefore: time.Now().Add(-48 * time.Hour), notAfter: time.Now().Add(-24 * time.Hour)})
	fp := fingerprintHex(der)
	receipt := filepath.Join(home, ".tfsaz", "route-recovery", routeSystemID(file)+".pem")
	if err := os.MkdirAll(filepath.Dir(receipt), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cert, receipt); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareRouteSystem(false, dir, file, cert, key, fp, []string{alias})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := errors.New("user cancelled trust removal")
	removals := 0
	fakeRouteSystemCommands(t, func(c routeSystemCall) ([]byte, error) {
		switch c.args[0] {
		case "find-certificate":
			return nil, nil // no item in the login keychain
		case "remove-trusted-cert":
			removals++
			der, err := readRouteSystemLeaf(c.args[1])
			if err != nil || fingerprintHex(der) != fp {
				t.Fatal("restore used the wrong leaf")
			}
			if removals == 1 {
				return nil, cancelled
			}
			return nil, nil
		default:
			t.Fatalf("unexpected security command %s", c.args[0])
			return nil, nil
		}
	})
	if err := removeMacTrust(t.Context(), plan); !errors.Is(err, cancelled) {
		t.Fatalf("cancelled restore returned %v", err)
	}
	if err := removeMacTrust(t.Context(), plan); err != nil || removals != 2 {
		t.Fatalf("retry returned %v, removals %d", err, removals)
	}
}
