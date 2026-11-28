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
				{"vm vnc tfsaz-ubuntu-demo", false},
				{"vm up ubuntu", true},
				{"vm ssh tfsaz-ubuntu-demo", true},
				{"vm run tfsaz-ubuntu-demo -- uname", true},
				{"vm push tfsaz-ubuntu-demo LOCAL REMOTE", true},
				{"vm pull tfsaz-ubuntu-demo REMOTE LOCAL", true},
				{"vm screenshot tfsaz-ubuntu-demo FILE", true},
				{"vm forward tfsaz-ubuntu-demo 9222", true},
				{"vm t3 install tfsaz-ubuntu-demo", true},
				{"vm t3 start tfsaz-ubuntu-demo", true},
				{"vm stop tfsaz-ubuntu-demo", true},
				{"vm start tfsaz-ubuntu-demo", true},
				{"vm down tfsaz-ubuntu-demo", true},
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
}

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
			name := prefix + tc.os + "-resources"
			o, ok := osOfVM(name)
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

func TestLoadVMMetadataError(t *testing.T) {
	const name = "tfsaz-ubuntu-demo"
	for _, tc := range []struct{ kind, want string }{
		{"missing", "no clone"},
		{"unreadable", "read metadata"},
		{"missing metadata", "read metadata"},
		{"malformed", "decode metadata"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			e := &env{home: t.TempDir()}
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

func TestSensitiveSSHError(t *testing.T) {
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
