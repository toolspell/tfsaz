package main

import (
	"encoding/base64"
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestFreeAddress(t *testing.T) {
	addr := netip.MustParseAddr
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
			got, err := freeAddress(tc.used)
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
	for _, tc := range []struct {
		name  string
		clone bool
	}{
		{"tfsaz-ubuntu-a1b2c3", true},
		{"tfsaz-ubuntu-base", false},
		{"tfsaz-ubuntu-build", false},
		{"tfsaz-ubuntu-", false},
		{"tfsaz-ubuntu-/../..", false},
		{"tfsaz-ubuntu-a/b", false},
		{"tfsaz-macos-a1b2c3", true},
		{"tfsaz-macos-base", false},
		{"tfsaz-windows-x", true},
		{"tfsaz-windows-build", false},
		{"ubuntu-a1b2c3", false},
	} {
		if _, got := osOfVM(tc.name); got != tc.clone {
			t.Errorf("osOfVM(%s) = %v", tc.name, got)
		}
	}
}

func TestWGConfig(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		guest                guest
		key, dns, allowedIPs string
	}{
		{"ubuntu", ubuntuGuest, "private-key /etc/wireguard/private.key", "resolvectl dns %i 172.30.53.2", "0.0.0.0/0"},
		{"macos", macosGuest, "private-key /etc/wireguard/private.key", "DNS = 172.30.53.2", "0.0.0.0/0"},
		{"windows", windowsGuest, "PrivateKey = __PRIVATE_KEY__", "DNS = 172.30.53.2", "0.0.0.0/1, 128.0.0.0/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := wgConfig(netip.MustParseAddr("172.30.53.129"), "ROUTER", "192.168.64.1", tc.guest)
			for _, want := range []string{
				tc.key, "Address = 172.30.53.129/32", tc.dns,
				"PublicKey = ROUTER", "Endpoint = 192.168.64.1:51820", "AllowedIPs = " + tc.allowedIPs + "\n",
			} {
				if !strings.Contains(config, want) {
					t.Errorf("config misses %q:\n%s", want, config)
				}
			}
		})
	}
}

func TestPeersSkipsStagingFiles(t *testing.T) {
	e := &env{home: t.TempDir()}
	dir := e.path("router", "peers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, record := range map[string]string{
		"tfsaz-ubuntu-a":  "KEY 172.30.53.129\n",
		".tfsaz-ubuntu-b": "KEY 172.30.53.130\n", // peer.sh renames it into place
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	peers, err := e.peers()
	if err != nil || len(peers) != 1 || peers["tfsaz-ubuntu-a"].String() != "172.30.53.129" {
		t.Fatalf("peers = %v, %v; want only tfsaz-ubuntu-a", peers, err)
	}
}

func TestLoadVMStaysInRuns(t *testing.T) {
	e := &env{home: t.TempDir()}
	if err := os.WriteFile(e.path("vm.json"), []byte(`{"name":"outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := e.loadVM("tfsaz-ubuntu-/../.."); err == nil {
		t.Fatalf("loadVM read %+v outside runs", info)
	}
}

func TestEncodePowerShell(t *testing.T) {
	command := encodePowerShell(`Write-Output "ä 'b'"`)
	b64, ok := strings.CutPrefix(command, "powershell -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand ")
	if !ok {
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
