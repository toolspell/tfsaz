package az

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	routeTestID    = "0123456789abcdef"
	routeTestAlias = "tfsaz-0123456789abcdef01234567.visualstudio.com"
)

func TestEditRouteHostsKeepsForeignLines(t *testing.T) {
	other := "127.0.0.1 tfsaz-aaaaaaaaaaaaaaaaaaaaaaaa.visualstudio.com # tfsaz-route ffffffffffffffff"
	base := "127.0.0.1 localhost\n# comment\n" + other + "\n10.0.0.1 server # note"
	installed := editRouteHosts(base, routeTestID, []string{routeTestAlias}, true)
	want := "127.0.0.1 " + routeTestAlias + " # tfsaz-route " + routeTestID + "\n::1 " + routeTestAlias + " # tfsaz-route " + routeTestID + "\n" + base
	if installed != want {
		t.Fatalf("install:\n%q\nwant\n%q", installed, want)
	}
	if again := editRouteHosts(installed, routeTestID, []string{routeTestAlias}, true); again != installed {
		t.Fatalf("install is not idempotent:\n%q", again)
	}
	restored := editRouteHosts(installed, routeTestID, nil, false)
	if restored != base || !strings.Contains(restored, other) {
		t.Fatalf("restore:\n%q", restored)
	}
	// Without our marker, restore leaves even a missing final newline alone.
	if got := editRouteHosts(base, routeTestID, nil, false); got != base {
		t.Fatalf("restore changed a foreign file: %q", got)
	}
}

func TestEditRouteHostsKeepsCRLF(t *testing.T) {
	base := "127.0.0.1 localhost\r\n"
	installed := editRouteHosts(base, routeTestID, []string{routeTestAlias}, true)
	if strings.Count(installed, "\r\n") != 3 || strings.Contains(strings.ReplaceAll(installed, "\r\n", ""), "\n") {
		t.Fatalf("line endings not kept: %q", installed)
	}
	if got := editRouteHosts(installed, routeTestID, nil, false); got != base {
		t.Fatalf("restore: %q", got)
	}
}

func TestRouteHostsConflict(t *testing.T) {
	aliases := []string{routeTestAlias}
	own := strings.Join(routeHostsLines(routeTestID, aliases), "\n")
	for _, tc := range []struct {
		hosts string
		ok    bool
	}{
		{"127.0.0.1 localhost\n" + own + "\n", true},
		{"# 10.0.0.1 " + routeTestAlias + "\n", true},
		{"10.0.0.1 x # " + routeTestAlias + "\n", true},
		{"10.0.0.1 other " + strings.ToUpper(routeTestAlias) + "\n", false},
		{"127.0.0.1 " + routeTestAlias + ". # mine\n", false},
		{"127.0.0.1 " + routeTestAlias + " # tfsaz-route ffffffffffffffff\n", false},
	} {
		err := routeHostsConflict(tc.hosts, routeTestID, aliases)
		if (err == nil) != tc.ok {
			t.Errorf("%q: err=%v", tc.hosts, err)
		}
	}
}

func routeSystemTestAssets(t *testing.T) (dir, configFile, certFile, keyFile, fp, alias string) {
	t.Helper()
	configFile, _, alias, _ = redirectTestSetup(t)
	dir = filepath.Join(t.TempDir(), "routing")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	certFile, keyFile, der := writeRedirectTestCert(t, dir, redirectTestCert{names: []string{alias}})
	return dir, configFile, certFile, keyFile, fingerprintHex(der), alias
}

func TestPrepareRouteSystemInstall(t *testing.T) {
	dir, configFile, certFile, keyFile, fp, alias := routeSystemTestAssets(t)
	p, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, strings.ToUpper(fp), []string{alias, alias})
	if err != nil {
		t.Fatal(err)
	}
	if p.leafSHA256 != fp || len(p.aliases) != 1 || p.certDER == nil || p.exe == "" || p.id != routeSystemID(configFile) {
		t.Fatalf("unexpected plan %+v", p)
	}
	if runtime.GOOS == "darwin" && p.port != 443 {
		t.Fatalf("macOS service port %d, want 443", p.port)
	}
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(filepath.Join(dir, routeSystemStateName))
		var st routeSystemState
		if err != nil || json.Unmarshal(data, &st) != nil || st.Port != p.port || st.ID != p.id || p.port <= 1024 {
			t.Fatalf("port state not persisted: %s %v", data, err)
		}
		// A retry reuses the same port so the privileged redirect stays valid.
		again, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, fp, []string{alias})
		if err != nil || again.port != p.port {
			t.Fatalf("retry port %d, want %d (%v)", again.port, p.port, err)
		}
	}

	other := strings.Replace(alias, alias[6:10], "ffff", 1)
	for name, call := range map[string]func() error{
		"relative": func() error {
			_, err := prepareRouteSystem(true, "routing", configFile, certFile, keyFile, fp, []string{alias})
			return err
		},
		"fingerprint": func() error {
			_, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, strings.Repeat("0", 64), []string{alias})
			return err
		},
		"short fingerprint": func() error {
			_, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, "abcd", []string{alias})
			return err
		},
		"uppercase alias": func() error {
			_, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, fp, []string{strings.ToUpper(alias)})
			return err
		},
		"shared host": func() error {
			_, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, fp, []string{"dev.azure.com"})
			return err
		},
		"wildcard": func() error {
			_, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, fp, []string{"*.visualstudio.com"})
			return err
		},
		"unregistered": func() error {
			_, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, fp, []string{other})
			return err
		},
		"cert outside dir": func() error {
			_, err := prepareRouteSystem(true, filepath.Dir(dir), configFile, certFile, keyFile, fp, []string{alias})
			return err
		},
		"no aliases": func() error {
			_, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, fp, nil)
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestPrepareRouteSystemRequiresExactNames(t *testing.T) {
	dir, configFile, _, _, _, alias := routeSystemTestAssets(t)
	// A leaf with an extra registered-looking name does not match the request.
	extra := "tfsaz-ffffffffffffffffffffffff.visualstudio.com"
	certFile, keyFile, der := writeRedirectTestCert(t, dir, redirectTestCert{names: []string{alias, extra}})
	if _, err := prepareRouteSystem(true, dir, configFile, certFile, keyFile, fingerprintHex(der), []string{alias}); err == nil {
		t.Fatal("accepted a leaf naming more than the requested aliases")
	}
}

func TestPrepareRouteSystemRestoreNeedsNoAssets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gone")
	p, err := prepareRouteSystem(false, dir, filepath.Join(dir, "config.json"), filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), strings.Repeat("ab", 32), nil)
	if err != nil || p.certDER != nil || p.id == "" {
		t.Fatalf("restore plan %+v err %v", p, err)
	}
}

func TestRouteSystemDirMustBePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkRouteSystemDir(dir); err == nil {
		t.Fatal("accepted a group-readable directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkRouteSystemDir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestRouteSystemIDIsStable(t *testing.T) {
	a := routeSystemID("/x/config.json")
	if a != routeSystemID("/x/./config.json") || a == routeSystemID("/y/config.json") || len(a) != 16 {
		t.Fatalf("id %q", a)
	}
}

// routeSystemTestService serves the leaf on both loopback families of one
// free port, as the redirect service would.
func routeSystemTestService(t *testing.T, certFile, keyFile string) int {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	for range 20 {
		ln4, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
		if err != nil {
			t.Fatal(err)
		}
		port := ln4.Addr().(*net.TCPAddr).Port
		ln6, err := tls.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)), cfg)
		if err != nil {
			ln4.Close()
			continue
		}
		for _, ln := range []net.Listener{ln4, ln6} {
			t.Cleanup(func() { ln.Close() })
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					go func() {
						c.(*tls.Conn).Handshake()
						c.Close()
					}()
				}
			}()
		}
		return port
	}
	t.Fatal("no free dual-stack port")
	return 0
}

func TestWaitRouteSystemServiceMatchesLeaf(t *testing.T) {
	dir, _, certFile, keyFile, fp, alias := routeSystemTestAssets(t)
	p := routeSystemPlan{id: routeTestID, dir: dir, leafSHA256: fp, aliases: []string{alias}}
	p.port = routeSystemTestService(t, certFile, keyFile)
	if err := waitRouteSystemService(context.Background(), p, time.Second); err != nil {
		t.Fatal(err)
	}
	p.leafSHA256 = strings.Repeat("0", 64)
	if err := waitRouteSystemService(context.Background(), p, 300*time.Millisecond); err == nil {
		t.Fatal("accepted a different leaf")
	}
}

type routeSystemCall struct {
	name  string
	args  []string
	stdin string
}

// fakeRouteSystemCommands records commands instead of running them.
func fakeRouteSystemCommands(t *testing.T, reply func(c routeSystemCall) ([]byte, error)) *[]routeSystemCall {
	t.Helper()
	var calls []routeSystemCall
	old := runRouteSystemCommand
	runRouteSystemCommand = func(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		c := routeSystemCall{name: name, args: args, stdin: string(stdin)}
		calls = append(calls, c)
		return reply(c)
	}
	t.Cleanup(func() { runRouteSystemCommand = old })
	return &calls
}
