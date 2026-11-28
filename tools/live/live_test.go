package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestMockRequestsVMFilter(t *testing.T) {
	e := &env{home: t.TempDir()}
	info := vmInfo{Name: "tfsaz-ubuntu-a", IP: "192.0.2.1", BedAddress: "172.30.53.129"}
	if err := os.MkdirAll(e.runDir(info.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.saveVM(info); err != nil {
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

func TestTartVNC(t *testing.T) {
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
}

func TestSplitCommand(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		name    string
		flags   []string
		command []string
		ok      bool
	}{
		{[]string{"vm1", "--", "echo", "hi"}, "vm1", []string{}, []string{"echo", "hi"}, true},
		{[]string{"vm1", "-json", "-gui", "--", "id"}, "vm1", []string{"-json", "-gui"}, []string{"id"}, true},
		{[]string{"vm1", "echo"}, "", nil, nil, false},
		{[]string{"vm1", "--"}, "", nil, nil, false},
		{[]string{"--", "echo"}, "", nil, nil, false},
	} {
		name, flags, command, ok := splitCommand(tc.args)
		if name != tc.name || ok != tc.ok || !slices.Equal(flags, tc.flags) || !slices.Equal(command, tc.command) {
			t.Errorf("splitCommand(%q) = %q, %q, %q, %v", tc.args, name, flags, command, ok)
		}
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
