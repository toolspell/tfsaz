package main

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestCIGuard(t *testing.T) {
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
	for _, name := range []string{"CI", "GITHUB_ACTIONS"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, env := range []struct{ name, value string }{
		{"CI", "true"},
		{"GITHUB_ACTIONS", ""},
		{},
	} {
		name := env.name
		if name == "" {
			name = "local"
		}
		t.Run(name, func(t *testing.T) {
			if env.name != "" {
				t.Setenv(env.name, env.value)
			}
			for _, tc := range []struct {
				command string
				blocked bool
			}{
				{"help", false},
				{"up", true},
				{"up ubuntu macos windows", true},
				{"down", true},
				{"prune", true},
				{"status", false},
				{"doctor", false},
				{"doctor -install=false -yes=false", false},
				{"doctor --install", true},
				{"doctor -yes=true", true},
				{"services status", false},
				{"services up", true},
				{"services down", true},
				{"mock ca", false},
				{"mock requests -json", false},
				{"mock reset", true},
				{"base build ubuntu", true},
				{"base update macos", true},
				{"base rm windows", true},
				{"vm list -json", false},
				{"vm vnc tfsaz-live-first-ubuntu-demo", false},
				{"vm up ubuntu", true},
				{"vm ssh tfsaz-live-first-ubuntu-demo", true},
				{"vm run tfsaz-live-first-ubuntu-demo -- uname", true},
				{"vm push tfsaz-live-first-ubuntu-demo LOCAL REMOTE", true},
				{"vm pull tfsaz-live-first-ubuntu-demo REMOTE LOCAL", true},
				{"vm screenshot tfsaz-live-first-ubuntu-demo FILE", true},
				{"vm forward tfsaz-live-first-ubuntu-demo 9222", true},
				{"vm t3 install tfsaz-live-first-ubuntu-demo", true},
				{"vm t3 start tfsaz-live-first-ubuntu-demo", true},
				{"vm browser start tfsaz-live-first-ubuntu-demo chrome", true},
				{"vm stop tfsaz-live-first-ubuntu-demo", true},
				{"vm start tfsaz-live-first-ubuntu-demo", true},
				{"vm down tfsaz-live-first-ubuntu-demo", true},
				{"setup ubuntu", true},
				{"verify macos", true},
				{"clean", true},
				{"vm future-command", false},
			} {
				t.Run(tc.command, func(t *testing.T) {
					var stdout, stderr bytes.Buffer
					code := liveMain(strings.Fields(tc.command), &stdout, &stderr)
					switch {
					case tc.blocked && env.name != "":
						want := "live: live test changes are disabled when " + env.name + " is set\n"
						if code != 1 || stderr.String() != want {
							t.Fatalf("exit %d, stderr %q, want %q", code, stderr.String(), want)
						}
					case tc.command == "help":
						if code != 0 || stderr.Len() != 0 {
							t.Fatalf("exit %d, stderr %q", code, stderr.String())
						}
					case tc.command == "vm future-command":
						if code != 2 || !strings.Contains(stderr.String(), "unknown action") {
							t.Fatalf("exit %d, stderr %q", code, stderr.String())
						}
					default:
						if code != 1 || stderr.String() != "live: run from inside the TFSaz repository\n" {
							t.Fatalf("exit %d, stderr %q, want repository lookup to fail", code, stderr.String())
						}
					}
				})
			}
		})
	}
	t.Run("read-only diagnostics do not create state", func(t *testing.T) {
		if err := os.Unsetenv("GIT_DIR"); err != nil {
			t.Fatal(err)
		}
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git unavailable")
		}
		home := filepath.Join(t.TempDir(), "state")
		t.Setenv("CI", "true")
		t.Setenv("TFSAZ_LIVE_HOME", home)
		t.Setenv("TFSAZ_LIVE_INSTANCE", "first")
		var stdout, stderr bytes.Buffer
		if code := liveMain([]string{"services", "status"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
		if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("state directory exists or could not be checked: %v", err)
		}
	})
}

func TestFreeAddress(t *testing.T) {
	bed := slotNetwork(0)
	addr := netip.MustParseAddr
	firstVMAddr, lastVMAddr := bed.address(129), bed.address(254)
	all := make(map[netip.Addr]bool)
	for a := firstVMAddr; a.Compare(lastVMAddr) <= 0; a = a.Next() {
		all[a] = true
	}
	for _, tc := range []struct {
		name    string
		used    map[netip.Addr]bool
		want    string
		wantErr bool
	}{
		{"none used", nil, "172.30.53.129", false},
		{"gap", map[netip.Addr]bool{addr("172.30.53.129"): true, addr("172.30.53.131"): true}, "172.30.53.130", false},
		{"full", all, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bed.freeAddress(tc.used)
			if (err != nil) != tc.wantErr || (!tc.wantErr && got.String() != tc.want) {
				t.Fatalf("freeAddress = %v, %v; want %s", got, err, tc.want)
			}
		})
	}
}

func TestVersionLess(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"2.42.0", "2.0.0", false},
		{"1.9.5", "1.11.0", true},
		{"1.11", "1.11.0", false},
		{"2.20.0", "2.20.1", true},
	} {
		if got := versionLess(tc.a, tc.b); got != tc.want {
			t.Errorf("versionLess(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}

func TestCloneNames(t *testing.T) {
	e := &env{instance: "first"}
	ubuntu := oses[0]
	for _, tc := range []struct {
		name       string
		own        bool
		ubuntuBase bool
	}{
		{"tfsaz-live-first-ubuntu-a1b2c3", true, true},
		{"tfsaz-live-first-ubuntu-verify-a1b2c3", true, true},
		{"tfsaz-live-second-ubuntu-a1b2c3", false, true},
		{"tfsaz-live-firstextra-ubuntu-a1b2c3", false, true},
		{"tfsaz-live-first-macos-up", true, false},
		{"tfsaz-live-first-windows-up", true, false},
		{"tfsaz-live-first-other-up", false, false},
		{"tfsaz-live-first-ubuntu-base", false, false},
		{"tfsaz-live-first-ubuntu-", false, false},
		{"tfsaz-live-first-ubuntu-/../..", false, false},
		{"tfsaz-live-first-ubuntu-a/b", false, false},
		{"tfsaz-ubuntu-legacy", false, true},
		{"tfsaz-ubuntu-base", false, false},
		{"tfsaz-ubuntu-build", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := e.ownClones(map[string]bool{tc.name: true})
			if (len(got) == 1) != tc.own || (len(got) == 1 && got[0] != tc.name) {
				t.Errorf("ownClones = %v; want own %t", got, tc.own)
			}
			if got := ubuntu.isClone(tc.name); got != tc.ubuntuBase {
				t.Errorf("ubuntu base dependency = %t; want %t", got, tc.ubuntuBase)
			}
		})
	}
}

func TestInstanceID(t *testing.T) {
	root := t.TempDir()
	first, err := instanceID(root, "")
	if err != nil || len(first) != 12 || !instancePattern.MatchString(first) {
		t.Fatalf("default instance ID = %q, %v", first, err)
	}
	for _, tc := range []struct {
		name, root, override, sameAs string
		wantErr                      bool
	}{
		{name: "stable", root: root + string(filepath.Separator) + ".", sameAs: first},
		{name: "different worktree", root: t.TempDir()},
		{name: "override", root: root, override: "myworktree", sameAs: "myworktree"},
		{name: "uppercase", root: root, override: "MyWorktree", wantErr: true},
		{name: "path traversal", root: root, override: "../elsewhere", wantErr: true},
		{name: "too long", root: root, override: strings.Repeat("a", 17), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := instanceID(tc.root, tc.override)
			if (err != nil) != tc.wantErr {
				t.Fatalf("instanceID = %q, %v; want error %t", got, err, tc.wantErr)
			}
			if !tc.wantErr && (tc.sameAs != "" && got != tc.sameAs || tc.sameAs == "" && got == first) {
				t.Errorf("instanceID = %q; expected %q or a distinct worktree ID", got, tc.sameAs)
			}
		})
	}
}

func TestBedNetworkConfig(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		instance, subnet, mockIP, routerIP, httpPort, httpsPort, sshPort, journalPort, wireguardPort string
		slot                                                                                         int
	}{
		{"first", "172.30.53.0/24", "172.30.53.10", "172.30.53.2", "8080", "8443", "2222", "19090", "51820", 0},
		{"second", "172.30.54.0/24", "172.30.54.10", "172.30.54.2", "8081", "8444", "2223", "19091", "51821", 1},
	} {
		t.Run(tc.instance, func(t *testing.T) {
			network := slotNetwork(tc.slot)
			e := &env{home: home, bed: "../../live-tests", instance: tc.instance, network: &network}
			if err := e.prepareBed(); err != nil {
				t.Fatal(err)
			}
			config, err := os.ReadFile(e.instancePath("Corefile"))
			if err != nil || strings.Count(string(config), "IN A "+tc.mockIP) != 2 || strings.Contains(string(config), "${mock_ip}") {
				t.Fatalf("Corefile = %q, %v; want two records for %s", config, err, tc.mockIP)
			}
			ipPrefix := strings.TrimSuffix(tc.subnet, "0/24")
			t.Run("WireGuard", func(t *testing.T) {
				checkWGConfig(t, network, ipPrefix+"129", tc.routerIP, tc.wireguardPort)
			})
			for key, value := range map[string]string{
				"CONTAINER_SUBNET": ipPrefix + "0/25", "VM_SUBNET": ipPrefix + "128/25", "GATEWAY": ipPrefix + "1", "DNS_IP": ipPrefix + "53",
				"PROJECT": "tfsaz-live-" + tc.instance, "STATE": filepath.ToSlash(filepath.Join(home, "instances", tc.instance)),
				"SUBNET": tc.subnet, "ROUTER_IP": tc.routerIP, "MOCK_IP": tc.mockIP, "HTTP_PORT": tc.httpPort,
				"HTTPS_PORT": tc.httpsPort, "SSH_PORT": tc.sshPort, "JOURNAL_PORT": tc.journalPort, "WIREGUARD_PORT": tc.wireguardPort,
			} {
				want := "TFSAZ_LIVE_" + key + "=" + value
				if !slices.Contains(network.composeEnv(e.project(), e.instancePath()), want) {
					t.Errorf("Compose environment missing %q", want)
				}
			}
			if got, want := network.journalURL(), "http://127.0.0.1:"+tc.journalPort+"/requests"; got != want {
				t.Errorf("journal URL = %q; want %q", got, want)
			}
			if got, want := e.cloneName(oses[0], "up"), "tfsaz-live-"+tc.instance+"-ubuntu-up"; got != want {
				t.Errorf("clone name = %q; want %q", got, want)
			}
		})
	}
}

func checkWGConfig(t *testing.T, network bedNetwork, address, routerIP, wireguardPort string) {
	t.Helper()
	for _, tc := range []struct {
		name                 string
		guest                guest
		key, dns, allowedIPs string
	}{
		{"ubuntu", ubuntuGuest, "private-key /etc/wireguard/private.key", "resolvectl dns %i " + routerIP, "0.0.0.0/0"},
		{"macos", macosGuest, "private-key /etc/wireguard/private.key", "DNS = " + routerIP, "0.0.0.0/0"},
		{"windows", windowsGuest, "PrivateKey = __PRIVATE_KEY__", "DNS = " + routerIP, "0.0.0.0/1, 128.0.0.0/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := network.wgConfig(netip.MustParseAddr(address), "ROUTER", "192.168.64.1", tc.guest)
			for _, want := range []string{
				tc.key, "Address = " + address + "/32", tc.dns,
				"PublicKey = ROUTER", "Endpoint = 192.168.64.1:" + wireguardPort, "AllowedIPs = " + tc.allowedIPs + "\n",
			} {
				if !strings.Contains(config, want) {
					t.Errorf("config misses %q:\n%s", want, config)
				}
			}
		})
	}
}

func TestCloneResources(t *testing.T) {
	for _, tc := range []struct {
		os   string
		want []string
	}{
		{"ubuntu", []string{"--cpu", "2", "--memory", "4096", "--display", "1920x1080px"}},
		{"macos", []string{"--cpu", "2", "--memory", "6144", "--display", "1920x1080px"}},
		{"windows", []string{"-smp", "2", "-m", "6G"}},
	} {
		t.Run(tc.os, func(t *testing.T) {
			e := &env{instance: "first"}
			name := "tfsaz-live-first-" + tc.os + "-resources"
			o, ok := e.osOfVM(name)
			if !ok {
				t.Fatalf("no guest OS for %s", name)
			}
			if o.provider == "tart" {
				want := append([]string{"set", name}, tc.want...)
				if got := tartCloneSettings(o, name); !slices.Equal(got, want) {
					t.Fatalf("Tart settings: got %q, want %q", got, want)
				}
				return
			}
			args := (qemu{home: t.TempDir()}).commandArgs(name, "efi-code.fd", 2222)
			i := slices.Index(args, "-smp")
			if i < 0 || len(args)-i < len(tc.want) || !slices.Equal(args[i:i+len(tc.want)], tc.want) {
				t.Fatalf("QEMU args %q are missing %q", args, tc.want)
			}
		})
	}
	t.Run("macOS host capacity", checkMacOSCapacityCases)
}

func checkMacOSCapacityCases(t *testing.T) {
	mac := oses[1]
	first := vmEntry{Name: "tfsaz-live-first-macos-up", Running: true}
	second := vmEntry{Name: "tfsaz-live-second-macos-a1b2c3", State: "running"}
	for _, tc := range []struct {
		name string
		vms  []vmEntry
		full bool
	}{
		{"second slot available", []vmEntry{first, {Name: second.Name, State: "stopped"}}, false},
		{"two worktrees", []vmEntry{first, second}, true},
		{"base build counts", []vmEntry{first, {Name: mac.build(), Running: true}}, true},
		{"legacy VM counts", []vmEntry{second, {Name: "tfsaz-macos-legacy", Running: true}}, true},
		{"other guest OS", []vmEntry{first, {Name: "tfsaz-live-second-ubuntu-up", Running: true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkMacOSCapacity(mac, tc.vms); (err != nil) != tc.full {
				t.Errorf("macOS capacity = %v; want full %t", err, tc.full)
			}
		})
	}
}

func TestRouterPeers(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T)
	}{
		{"ignore staging files", checkPeerStagingFiles},
		{"remove peer", checkRemovePeer},
	} {
		t.Run(tc.name, tc.check)
	}
}

func checkPeerStagingFiles(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	dir := e.instancePath("router", "peers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, record := range map[string]string{
		"tfsaz-live-first-ubuntu-a":  "KEY 172.30.53.129\n",
		".tfsaz-live-first-ubuntu-b": "KEY 172.30.53.130\n", // peer.sh renames it into place
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	peers, err := e.peers()
	if err != nil || len(peers) != 1 || peers["tfsaz-live-first-ubuntu-a"].String() != "172.30.53.129" {
		t.Fatalf("peers = %v, %v; want only tfsaz-live-first-ubuntu-a", peers, err)
	}
}

func checkRemovePeer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		psOut   string
		psErr   error
		wantCmd bool
	}{
		{name: "running router", psOut: "router-id", wantCmd: true},
		{name: "stopped router"},
		{name: "Docker stopped", psErr: errors.New("Docker engine stopped")},
		{name: "Docker stopped with partial output", psOut: "router-id", psErr: errors.New("Docker engine stopped")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			network := slotNetwork(0)
			e := &env{home: t.TempDir(), instance: "first", network: &network}
			name := e.cloneName(oses[0], "abc123")
			peer := e.instancePath("router", "peers", name)
			putLiveFile(t, peer)
			warnings := captureLiveStderr(t)
			oldOutput, oldCmd := output, cmd
			t.Cleanup(func() { output, cmd = oldOutput, oldCmd })
			queries, updates := 0, 0
			output = func(_ []string, bin string, args ...string) (string, error) {
				if bin != "docker" || !slices.Equal(args, e.composeArgs([]string{"ps", "--status", "running", "-q", "router"})) {
					t.Fatalf("unexpected command: %s %q", bin, args)
				}
				queries++
				return tc.psOut, tc.psErr
			}
			cmd = func(_ string, _ []string, bin string, args ...string) error {
				if bin != "docker" || !slices.Equal(args, e.composeArgs([]string{"exec", "-T", "router", "peer", "remove", name})) {
					t.Fatalf("unexpected command: %s %q", bin, args)
				}
				updates++
				return os.Remove(peer)
			}
			for range 2 {
				if err := e.removePeer(name); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(peer); !os.IsNotExist(err) {
				t.Errorf("peer file remained: %v", err)
			}
			if queries != 1 || (updates == 1) != tc.wantCmd {
				t.Errorf("router queried %d times, updated %d times", queries, updates)
			}
			if got := strings.Contains(readLiveFile(t, warnings), "the router was not updated"); got != (tc.psErr != nil) {
				t.Errorf("unexpected warning: %s", readLiveFile(t, warnings))
			}
		})
	}
}

func TestLoadVMStaysInRuns(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	if err := os.WriteFile(e.path("vm.json"), []byte(`{"name":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := e.loadVM("tfsaz-live-first-ubuntu-/../.."); err == nil {
		t.Fatalf("loadVM read %+v outside runs", info)
	}
}

func TestLoadVMMetadataError(t *testing.T) {
	const name = "tfsaz-live-first-ubuntu-demo"
	for _, tc := range []struct{ kind, want string }{
		{"missing", "no clone"},
		{"unreadable", "read metadata"},
		{"missing metadata", "read metadata"},
		{"malformed", "decode metadata"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			e := &env{home: t.TempDir(), instance: "first"}
			file := filepath.Join(e.runDir(name), "vm.json")
			switch tc.kind {
			case "unreadable":
				if err := os.MkdirAll(file, 0o700); err != nil {
					t.Fatal(err)
				}
			case "malformed", "missing metadata":
				if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
					t.Fatal(err)
				}
				if tc.kind == "malformed" {
					if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := e.loadVM(name)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "vm list -json") {
				t.Fatalf("got %v, want %q and vm list -json", err, tc.want)
			}
			var syntax *json.SyntaxError
			var path *os.PathError
			if tc.kind == "malformed" && !errors.As(err, &syntax) || tc.kind != "malformed" && !errors.As(err, &path) {
				t.Fatalf("missing cause in %v", err)
			}
		})
	}
}

func TestMockJournalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := requestMockJournal(http.MethodDelete, srv.URL, http.StatusNoContent)
	if err == nil || !strings.Contains(err.Error(), "DELETE") || !strings.Contains(err.Error(), "503") ||
		!strings.Contains(err.Error(), "services status") {
		t.Fatalf("got %v, want DELETE, HTTP 503 and services status", err)
	}
}

func TestMockRequestsVMFilter(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	info := vmInfo{Name: "tfsaz-live-first-ubuntu-a", OS: "ubuntu", IP: "192.0.2.1", BedAddress: "172.30.53.129"}
	if err := os.MkdirAll(e.runDir(info.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := e.saveVM(info); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/requests" || r.URL.Query().Get("ip") != info.BedAddress {
			t.Errorf("journal request = %s %s; want GET /requests?ip=%s", r.Method, r.URL.RequestURI(), info.BedAddress)
		}
		_, _ = w.Write([]byte("[]\n"))
	}))
	defer admin.Close()
	var out bytes.Buffer
	if err := e.mockRequests(admin.URL+"/requests", info.Name, true, &out); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "[]\n" {
		t.Errorf("journal output = %q; want an empty JSON array", got)
	}
}

func TestSSHTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T)
	}{
		{"forward arguments", checkSSHForwardArgs},
		{"sensitive error", checkSensitiveSSHError},
	} {
		t.Run(tc.name, tc.check)
	}
}

func checkSSHForwardArgs(t *testing.T) {
	const ssh = "/usr/bin/ssh"
	for _, tc := range []struct {
		name string
		host string
		port int
		peer []string
	}{
		{"default SSH port", "192.0.2.1", 0, []string{"tester@192.0.2.1"}},
		{"forwarded SSH port", "127.0.0.1", 22222, []string{"-p", "22222", "tester@127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := sshTarget{user: "tester", host: tc.host, port: tc.port, key: "id_ed25519", knownHosts: "known_hosts"}
			want := append([]string{
				ssh, "-N", "-n", "-o", "ExitOnForwardFailure=yes",
				"-o", "ForkAfterAuthentication=no", "-S", "none",
				"-L", "127.0.0.1:19222:127.0.0.1:9222",
				"-i", "id_ed25519", "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
				"-o", "StrictHostKeyChecking=accept-new", "-o", "UserKnownHostsFile=known_hosts",
				"-o", "ConnectTimeout=5", "-o", "LogLevel=ERROR",
				"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3",
			}, tc.peer...)
			if got := target.forwardArgs(ssh, "19222", "9222"); !slices.Equal(got, want) {
				t.Errorf("forwardArgs = %q; want %q", got, want)
			}
		})
	}
}

func checkSensitiveSSHError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake SSH executable uses a sh script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >&2\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const value, label = "fixture-value", "verify mock REST API"
	target := sshTarget{host: "192.0.2.1"}
	err := target.runSensitive(ctx, "curl -u :"+value, label)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 || !strings.Contains(err.Error(), label) || strings.Contains(err.Error(), value) {
		t.Fatalf("got %v, want safe label and exit status 7", err)
	}
}

func TestRouterKeyError(t *testing.T) {
	e := &env{home: t.TempDir()}
	_, err := e.routerKey()
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "services status") {
		t.Fatalf("got %v, want missing file cause and services status", err)
	}
}

func TestEncodePowerShell(t *testing.T) {
	command := encodePowerShell(`Write-Output "ä 'b'"`)
	b64, ok := strings.CutPrefix(command, "pwsh -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand ")
	b64, exit := strings.CutSuffix(b64, "; exit $LASTEXITCODE")
	if !ok || !exit {
		t.Fatalf("command = %q", command)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw)%2 != 0 {
		t.Fatalf("decode: %v, %d bytes", err, len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[2*i:])
	}
	if got := string(utf16.Decode(units)); !strings.HasSuffix(got, "\nWrite-Output \"ä 'b'\"") {
		t.Errorf("script = %q", got)
	}
}

func TestVNCFrame(t *testing.T) {
	defer func(wait time.Duration) { vncBlackWait = wait }(vncBlackWait)
	vncBlackWait = 200 * time.Millisecond
	black := []byte{0, 0, 0, 0, 0, 0, 0, 0}
	current := []byte{1, 2, 3, 0, 4, 5, 6, 0}
	for _, tc := range []struct {
		name   string
		frames [][]byte
		want   []color.RGBA
	}{
		{"current frame, no second request", [][]byte{current}, []color.RGBA{{3, 2, 1, 255}, {6, 5, 4, 255}}},
		{"black frame, then the current one", [][]byte{black, current}, []color.RGBA{{3, 2, 1, 255}, {6, 5, 4, 255}}},
		{"black frame only", [][]byte{black}, []color.RGBA{{0, 0, 0, 255}, {0, 0, 0, 255}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A real socket: net.Pipe's unbuffered writes would deadlock the
			// handshake, where both sides write before they read.
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = l.Close() }()
			done := make(chan error, 1)
			go func() {
				server, err := l.Accept()
				if err != nil {
					done <- err
					return
				}
				defer func() { _ = server.Close() }()
				_ = server.SetDeadline(time.Now().Add(10 * time.Second))
				done <- fakeVNCServer(server, tc.frames)
			}()
			client, err := net.Dial("tcp", l.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			_ = client.SetDeadline(time.Now().Add(10 * time.Second))
			img, err := vncFrame(client, client, "secret")
			_ = client.Close()
			if err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if got := img.Bounds().Size(); got != image.Pt(2, 1) {
				t.Fatalf("size = %v, want the size the server announced after its placeholder", got)
			}
			for x, want := range tc.want {
				if got := img.RGBAAt(x, 0); got != want {
					t.Errorf("pixel %d = %v, want %v", x, got, want)
				}
			}
		})
	}
}

// fakeVNCServer speaks the server side of RFB 3.8 with VNC authentication.
// It answers the first request with a new size only, as Apple's server
// does, then sends a stale frame and the current one.
// fakeVNCServer answers the handshake, announces a 2x1 screen and sends
// frames, each after a request, then waits until the client hangs up, as
// Apple's server does on a screen that does not change.
func fakeVNCServer(conn net.Conn, frames [][]byte) error {
	read := func(n int) ([]byte, error) {
		b := make([]byte, n)
		_, err := io.ReadFull(conn, b)
		return b, err
	}
	challenge := []byte("0123456789abcdef")
	desktopSize := encodingDesktopSize
	if _, err := conn.Write(append([]byte("RFB 003.008\n"), 1, 2)); err != nil {
		return err
	}
	if _, err := read(12 + 1); err != nil {
		return err
	}
	if _, err := conn.Write(challenge); err != nil {
		return err
	}
	response, err := read(16)
	if err != nil {
		return err
	}
	if want, _ := vncAuth("secret", challenge); !bytes.Equal(response, want) {
		return fmt.Errorf("wrong VNC response")
	}
	// SecurityResult OK, then ServerInit: a 1280x720 placeholder, a pixel
	// format the client replaces, and an empty name.
	init := []byte{0, 0, 0, 0, 0x05, 0x00, 0x02, 0xd0}
	init = append(init, make([]byte, 16)...)
	init = append(init, 0, 0, 0, 0)
	if _, err := conn.Write(init); err != nil {
		return err
	}
	if _, err := read(1 + 20 + 4); err != nil {
		return err
	}
	encodings, err := read(4 * 4)
	if err != nil {
		return err
	}
	if !bytes.Contains(encodings, binary.BigEndian.AppendUint32(nil, uint32(desktopSize))) {
		return fmt.Errorf("the client offers no desktop-size encoding, which stops Apple's VNC server")
	}
	if _, err := read(10); err != nil {
		return err
	}
	resize := []byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 2, 0, 1}
	resize = binary.BigEndian.AppendUint32(resize, uint32(desktopSize))
	if _, err := conn.Write(resize); err != nil {
		return err
	}
	if _, err := read(10); err != nil {
		return err
	}
	for i, pixels := range frames {
		if i > 0 {
			if _, err := read(10); err != nil {
				return err
			}
		}
		update := append([]byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 2, 0, 1, 0, 0, 0, 0}, pixels...)
		if _, err := conn.Write(update); err != nil {
			return err
		}
	}
	_, err = io.Copy(io.Discard, conn)
	return err
}

func TestTartStartupLog(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T)
	}{
		{"VNC address and password", checkTartVNC},
		{"archive on boot failure", checkFailedVMUpLogs},
	} {
		t.Run(tc.name, tc.check)
	}
}

func checkTartVNC(t *testing.T) {
	log := filepath.Join(t.TempDir(), "tart.log")
	content := "VNC server is running at vnc://:old@127.0.0.1:5000\nguest has stopped the virtual machine\n" +
		"VNC server is running at vnc://:new-pw@127.0.0.1:5001\n"
	if err := os.WriteFile(log, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	address, password, err := tartVNC(log)
	if err != nil || address != "127.0.0.1:5001" || password != "new-pw" {
		t.Fatalf("tartVNC = %q, %q, %v; want the last start's address", address, password, err)
	}

	const value = "fixture%secret"
	if err := os.WriteFile(log, []byte("vnc://:"+value+"@127.0.0.1:5001"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = tartVNC(log)
	var parseErr *neturl.Error
	if !errors.As(err, &parseErr) || !strings.Contains(err.Error(), log) || strings.Contains(err.Error(), value) {
		t.Fatalf("got %v, want the log path and the wrapped parser error without the password", err)
	}
}

func checkFailedVMUpLogs(t *testing.T) {
	for _, archiveOK := range []bool{true, false} {
		name := "archive available"
		if !archiveOK {
			name = "archive blocked"
		}
		t.Run(name, func(t *testing.T) { checkFailedVMUpCleanup(t, archiveOK) })
	}
}

func checkFailedVMUpCleanup(t *testing.T, archiveOK bool) {
	t.Helper()
	e := &env{home: t.TempDir(), instance: "first"}
	o := oses[1]
	name := e.cloneName(o, "up")
	logFile := filepath.Join(e.runDir(name), "tart.log")
	putLiveFile(t, filepath.Join(e.runDir(name), "id_ed25519"))
	putLiveFile(t, filepath.Join(e.tart().home, "test"))
	oldOutput := output
	t.Cleanup(func() { output = oldOutput })
	deleted := false
	output = func(_ []string, bin string, args ...string) (string, error) {
		if bin != "tart" {
			t.Fatalf("unexpected command: %s %q", bin, args)
		}
		switch strings.Join(args, " ") {
		case "list --source local --format json":
			return `[{"Name":"` + name + `","State":"stopped"}]`, nil
		case "stop " + name + " --timeout 30":
			return "", nil
		case "delete " + name:
			deleted = true
			return "", nil
		}
		t.Fatalf("unexpected Tart command: %q", args)
		return "", nil
	}
	const body = "old line\n2\n3\n4\n5\nVNC server is running at vnc://:secret@127.0.0.1:5900\nVM failed to boot\n"
	if err := os.WriteFile(logFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("VM did not start")
	kept := e.instancePath("failed-starts", name+".log")
	if !archiveOK {
		putLiveFile(t, filepath.Dir(kept))
	}
	err := e.cleanupFailedVM(o, name, cause)
	if archiveOK {
		if got := readLiveFile(t, kept); got != body || !strings.Contains(err.Error(), kept) {
			t.Errorf("kept log = %q; startup error = %v", got, err)
		}
	} else if !strings.Contains(err.Error(), "save startup log") {
		t.Errorf("archive error missing from %v", err)
	}
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "VM failed to boot") ||
		strings.Contains(err.Error(), "old line") || strings.Contains(err.Error(), "secret") {
		t.Errorf("startup error = %v", err)
	}
	if _, err := os.Stat(e.runDir(name)); !os.IsNotExist(err) {
		t.Errorf("failed clone's run directory remains: %v", err)
	}
	if !deleted {
		t.Error("failed clone was not deleted")
	}
}

func TestShellQuote(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	for _, s := range []string{`plain`, `it's`, `"double" and 'single'`, `$HOME; rm -rf /`} {
		out, err := exec.Command("sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("sh got %q, %v; want %q", out, err, s)
		}
	}
}

func TestParseBrowserStart(t *testing.T) {
	const ubuntu = "tfsaz-live-first-ubuntu-test"
	const windows = "tfsaz-live-first-windows-test"
	const macos = "tfsaz-live-first-macos-test"
	for _, tc := range []struct {
		name    string
		args    []string
		port    int
		url     string
		wantErr string
	}{
		{name: "default port", args: []string{"start", ubuntu, "chrome"}, port: 9222},
		{name: "port and URL", args: []string{"start", windows, "edge", "-port", "65535", "about:blank"}, port: 65535, url: "about:blank"},
		{name: "URL only", args: []string{"start", macos, "chrome", "https://example.invalid"}, port: 9222, url: "https://example.invalid"},
		{name: "missing browser", args: []string{"start", ubuntu}, wantErr: "missing BROWSER"},
		{name: "wrong action", args: []string{"stop", ubuntu, "chrome"}, wantErr: "unknown action"},
		{name: "two URLs", args: []string{"start", ubuntu, "chrome", "about:blank", "https://example.invalid"}, wantErr: "unexpected operand"},
		{name: "unknown flag", args: []string{"start", ubuntu, "chrome", "-unknown"}, wantErr: "flag provided but not defined"},
		{name: "zero port", args: []string{"start", ubuntu, "chrome", "-port", "0"}, wantErr: "invalid port"},
		{name: "large port", args: []string{"start", ubuntu, "chrome", "-port", "65536"}, wantErr: "invalid port"},
		{name: "Safari", args: []string{"start", macos, "safari"}, wantErr: "unsupported browser"},
		{name: "Firefox", args: []string{"start", ubuntu, "firefox"}, wantErr: "unsupported browser"},
		{name: "unknown browser", args: []string{"start", windows, "unknown"}, wantErr: "unsupported browser"},
		{name: "Edge on Ubuntu", args: []string{"start", ubuntu, "edge"}, wantErr: "only Windows clones come with Edge"},
		{name: "invalid clone", args: []string{"start", "tfsaz-ubuntu-base", "chrome"}, wantErr: "invalid clone name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			help, args, _, err := resolveHelp(append([]string{"vm", "browser"}, tc.args...))
			call := liveInvocation{commandHelp: help}
			if err == nil {
				err = call.readArgs(args, call.flagSet())
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				if tc.wantErr == "unsupported browser" && !strings.Contains(err.Error(), "mock requests") {
					t.Fatalf("error omits the journal command: %v", err)
				}
				return
			}
			if err != nil || len(call.args) != 2 || call.args[0] != tc.args[1] || call.args[1] != tc.args[2] || call.port != tc.port || call.browserURL != tc.url {
				t.Errorf("got %q, %d, %q, %v, want %q, %d, %q, nil", call.args, call.port, call.browserURL, err, tc.args[1:3], tc.port, tc.url)
			}
		})
	}
}

func TestBrowserStartCommand(t *testing.T) {
	const url = "https://example.invalid/?q=it's%20open&other=1"
	for _, tc := range []struct {
		os, browser, executable, profile, urlArg, detach string
	}{
		{"ubuntu", "chrome", "nohup google-chrome", `--user-data-dir="${TMPDIR:-/tmp}/tfsaz-live-chrome-9333"`, ` -- 'https://example.invalid/?q=it'\''s%20open&other=1'`, " </dev/null >/dev/null 2>&1 &"},
		{"macos", "chrome", "open -na 'Google Chrome' --args", `--user-data-dir="${temp%/}/tfsaz-live-chrome-9333"`, ` -- 'https://example.invalid/?q=it'\''s%20open&other=1'`, ""},
		{"windows", "chrome", `"$env:ProgramFiles\Google\Chrome\Application\chrome.exe"`, `"--user-data-dir=$env:TEMP\tfsaz-live-chrome-9333"`, "", "$null = [Diagnostics.Process]::Start($start)"},
		{"windows", "edge", `"${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe"`, `"--user-data-dir=$env:TEMP\tfsaz-live-edge-9333"`, "", "$null = [Diagnostics.Process]::Start($start)"},
	} {
		t.Run(tc.os+"/"+tc.browser, func(t *testing.T) {
			command := browserStartCommand(tc.os, tc.browser, 9333, url)
			for _, want := range []string{tc.executable, tc.profile, "--remote-debugging-port=9333", "--no-first-run", "--no-default-browser-check"} {
				if !strings.Contains(command, want) {
					t.Errorf("command is missing %q: %s", want, command)
				}
			}
			urlArg := tc.urlArg
			if tc.os == "windows" {
				urlArg = "[Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString([]byte(url)) + "')"
			}
			if !strings.Contains(command, urlArg) || !strings.HasSuffix(command, tc.detach) {
				t.Errorf("command should pass %q and end in %q: %s", urlArg, tc.detach, command)
			}
			if command := browserStartCommand(tc.os, tc.browser, 9333, ""); strings.Contains(command, urlArg) || strings.Contains(command, " -- ") || strings.Contains(command, "Add('--')") {
				t.Errorf("command without URL has a trailing argument: %s", command)
			}
		})
	}
}

func TestCDPVersionMatches(t *testing.T) {
	for _, tc := range []struct {
		name, body, app string
	}{
		{"T3", `{"Browser":"Chrome/140.0","User-Agent":"Mozilla/5.0 T3Code/0.1.13 Chrome/140.0","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/t3"}`, t3UserAgent},
		{"Chrome", `{"Browser":"Chrome/158.0","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/chrome"}`, "chrome"},
		{"Edge", `{"Browser":"Edg/158.0","User-Agent":"Mozilla/5.0 Chrome/158.0 Edg/158.0","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/edge"}`, "edge"},
		{"missing WebSocket", `{"Browser":"Chrome/158.0"}`, ""},
		{"invalid JSON", `Chrome/158.0 T3Code webSocketDebuggerUrl`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, app := range []string{t3UserAgent, "chrome", "edge"} {
				if got, want := cdpVersionMatches(tc.body, app), app == tc.app; got != want {
					t.Errorf("match %s = %t, want %t", app, got, want)
				}
			}
		})
	}
}

func TestT3Installer(t *testing.T) {
	for _, tc := range []struct{ os, tag, manifest, file, name string }{
		{"macos", "v0.1.13-nightly.20261125.5258", "nightly-mac.yml", "T3-Code-0.1.13-nightly.20261125.5258-arm64.zip", "T3 Code (Nightly)"},
		{"ubuntu", "v0.1.13-nightly.20261125.5258", "nightly-linux-arm64.yml", "T3-Code-0.1.13-nightly.20261125.5258-arm64.deb", "T3 Code (Nightly)"},
		{"windows", "v0.1.12", "latest.yml", "T3-Code-0.1.12-arm64.exe", "T3 Code (Beta)"},
	} {
		manifest, file, err := t3Installer(tc.os, tc.tag)
		if err != nil || manifest != tc.manifest || file != tc.file {
			t.Errorf("t3Installer(%s, %s) = %q, %q, %v", tc.os, tc.tag, manifest, file, err)
		}
		if got := t3Name(tc.tag); got != tc.name {
			t.Errorf("t3Name(%s) = %q, want %q", tc.tag, got, tc.name)
		}
	}
}

func TestManifestSHA512(t *testing.T) {
	manifest := "version: 1\nfiles:\n  - url: a-arm64.zip\n    sha512: AAA==\n    size: 1\n  - url: a-x64.zip\n    sha512: BBB==\npath: a-arm64.zip\nsha512: AAA==\n"
	for file, want := range map[string]string{"a-arm64.zip": "AAA==", "a-x64.zip": "BBB=="} {
		if got, err := manifestSHA512(strings.NewReader(manifest), file); err != nil || got != want {
			t.Errorf("manifestSHA512(%s) = %q, %v; want %q", file, got, err, want)
		}
	}
	if _, err := manifestSHA512(strings.NewReader(manifest), "missing.zip"); err == nil {
		t.Error("manifestSHA512 found a file the manifest does not list")
	}
}

func TestDownload(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl unavailable")
	}
	src := filepath.Join(t.TempDir(), "installer")
	if err := os.WriteFile(src, []byte("installer"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcPath := filepath.ToSlash(src)
	if !strings.HasPrefix(srcPath, "/") {
		srcPath = "/" + srcPath
	}
	url := (&neturl.URL{Scheme: "file", Path: srcPath}).String()
	sum := sha512.Sum512([]byte("installer"))
	good := base64.StdEncoding.EncodeToString(sum[:])
	for _, tc := range []struct {
		name, url, want string
		runs            int
		ok              bool
	}{
		{name: "no checksum", url: url, runs: 1, ok: true},
		{name: "matching checksum, parallel runs", url: url, want: good, runs: 4, ok: true},
		{name: "wrong checksum", url: url, want: "AAAA", runs: 1},
		{name: "failed download", url: url + ".missing", runs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			errs := make(chan error, tc.runs)
			for range tc.runs {
				go func() { errs <- download(path, tc.url, tc.want) }()
			}
			deadline := time.After(30 * time.Second)
			for range tc.runs {
				select {
				case err := <-errs:
					if (err == nil) != tc.ok {
						t.Errorf("download: %v, want success %v", err, tc.ok)
					}
				case <-deadline:
					t.Fatal("download did not finish within 30 s")
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if want := map[bool][]string{true: {"file"}, false: nil}[tc.ok]; !slices.Equal(names, want) {
				t.Errorf("directory holds %q, want %q", names, want)
			}
		})
	}
}

func freeHostPorts(bedPorts) error { return nil }
