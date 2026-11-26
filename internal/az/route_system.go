package az

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Machine integration for the loopback redirect service. The caller stages a
// private persistent directory with an exact-alias leaf and its key, calls
// install as the logged-in user and calls restore before deleting the assets.
// Every step is idempotent so an interrupted install or restore can be rerun.
//
// Ownership markers (id = routeSystemID(configFile)):
//   - hosts lines ending in " # tfsaz-route <id>"
//   - user service tfsaz-route-redirect-<id> (systemd --user, Task Scheduler)
//   - macOS LaunchAgents tfsaz.route-redirect.<id>.ipv4 and .ipv6
//   - Linux system unit tfsaz-route-redirect-<id>-nat.service and nft table inet tfsaz_route_<id>
//   - exact leaf trust entries, with an installation-specific NSS nickname on Linux
//
// Privileged steps run fixed scripts built in memory from system binaries. No
// elevated process executes the tfsaz binary or reads a user-writable file.
// The redirect service always runs as the invoking user.

const routeSystemStateName = "route-system.json"

type routeSystemPlan struct {
	id         string
	dir        string
	configFile string
	certFile   string
	keyFile    string
	leafSHA256 string
	certDER    []byte // nil on restore when the staged leaf is gone or differs
	aliases    []string
	exe        string
	port       int // port the user service listens on
}

type routeSystemState struct {
	ID   string `json:"id"`
	Port int    `json:"port"`
}

func manageRouteSystem(ctx context.Context, install bool, dir, configFile, certFile, keyFile, leafSHA256 string, aliases []string) error {
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		return errors.New("run browser routing setup as your own user, not as root; TFSaz asks for administrator rights only for the hosts file and port 443")
	}
	p, err := prepareRouteSystem(install, dir, configFile, certFile, keyFile, leafSHA256, aliases)
	if err != nil {
		return err
	}
	if !install {
		return routeSystemRestoreOS(ctx, p)
	}
	if err := routeSystemInstallOS(ctx, p); err != nil {
		return fmt.Errorf("%w; completed steps stay in place, so rerun setup to retry or restore to undo", err)
	}
	if err := verifyRouteSystem(ctx, p, 20*time.Second); err != nil {
		return fmt.Errorf("browser routing was installed but does not answer as expected: %w; rerun setup to retry or restore to undo", err)
	}
	return nil
}

func prepareRouteSystem(install bool, dir, configFile, certFile, keyFile, leafSHA256 string, aliases []string) (routeSystemPlan, error) {
	for _, path := range []string{dir, configFile, certFile, keyFile} {
		if !filepath.IsAbs(path) {
			return routeSystemPlan{}, fmt.Errorf("routing path %q must be absolute", path)
		}
	}
	p := routeSystemPlan{
		id:         routeSystemID(configFile),
		dir:        filepath.Clean(dir),
		configFile: filepath.Clean(configFile),
		certFile:   filepath.Clean(certFile),
		keyFile:    filepath.Clean(keyFile),
		leafSHA256: strings.ToLower(strings.ReplaceAll(leafSHA256, ":", "")),
	}
	if b, err := hex.DecodeString(p.leafSHA256); err != nil || len(b) != sha256.Size {
		return routeSystemPlan{}, errors.New("the leaf fingerprint must be a SHA-256 hex digest")
	}
	for _, f := range []string{p.certFile, p.keyFile} {
		if filepath.Dir(f) != p.dir {
			return routeSystemPlan{}, fmt.Errorf("%s must be directly inside the routing directory %s", f, p.dir)
		}
	}
	seen := make(map[string]bool)
	for _, a := range aliases {
		if a != strings.ToLower(a) || !generatedAliasHost.MatchString(a) {
			return routeSystemPlan{}, fmt.Errorf("%q is not a generated tfsaz-<24 hex>.visualstudio.com alias", a)
		}
		if !seen[a] {
			seen[a] = true
			p.aliases = append(p.aliases, a)
		}
	}
	slices.Sort(p.aliases)
	if !install {
		// Restore works from markers. The staged leaf only helps remove macOS trust.
		files := []string{p.certFile}
		if home, err := os.UserHomeDir(); err == nil {
			files = append(files, filepath.Join(home, ".tfsaz", "route-recovery", p.id+".pem"))
		}
		for _, file := range files {
			if der, err := readRouteSystemLeaf(file); err == nil && fingerprintHex(der) == p.leafSHA256 {
				p.certDER = der
				break
			}
		}
		return p, nil
	}
	if len(p.aliases) == 0 {
		return routeSystemPlan{}, errors.New("browser routing needs at least one registered alias")
	}
	if err := checkRouteSystemDir(p.dir); err != nil {
		return routeSystemPlan{}, err
	}
	cfg, err := loadConfigAt(p.configFile)
	if err != nil {
		return routeSystemPlan{}, err
	}
	records, err := readSetupRegistry(setupRegistryForConfig(p.configFile))
	if err != nil {
		return routeSystemPlan{}, err
	}
	registered := redirectAliasHosts(cfg, records)
	for _, a := range p.aliases {
		if !registered[a] {
			return routeSystemPlan{}, fmt.Errorf("%s is not a registered alias in %s", a, p.configFile)
		}
	}
	cert, names, err := loadRedirectCertificate(p.certFile, p.keyFile, seen, time.Now())
	if err != nil {
		return routeSystemPlan{}, err
	}
	if len(names) != len(p.aliases) {
		return routeSystemPlan{}, errors.New("the routing certificate must name exactly the requested aliases")
	}
	leaf := cert.Leaf
	if err := leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature); err != nil || !bytes.Equal(leaf.RawIssuer, leaf.RawSubject) {
		return routeSystemPlan{}, errors.New("the routing certificate must be self-signed so that trusting it covers only itself")
	}
	if fingerprintHex(leaf.Raw) != p.leafSHA256 {
		return routeSystemPlan{}, errors.New("the routing certificate does not match the expected SHA-256 fingerprint")
	}
	p.certDER = leaf.Raw
	if p.exe, err = os.Executable(); err == nil {
		p.exe, err = filepath.EvalSymlinks(p.exe)
	}
	if err != nil {
		return routeSystemPlan{}, fmt.Errorf("locate the TFSaz executable: %w", err)
	}
	if p.port, err = routeSystemServicePort(p.dir, p.id); err != nil {
		return routeSystemPlan{}, err
	}
	return p, nil
}

// routeSystemID names every system object of one configuration, so two
// configurations on one machine never touch each other's entries.
func routeSystemID(configFile string) string {
	sum := sha256.Sum256([]byte(routeConfigKey(configFile)))
	return hex.EncodeToString(sum[:8])
}

// routeConfigKey is the spelling of a configuration path that identifies its
// installation.
func routeConfigKey(configFile string) string {
	key := filepath.Clean(configFile)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key
}

func fingerprintHex(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func readRouteSystemLeaf(certFile string) ([]byte, error) {
	data, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	ders := pemCertificates(data)
	if len(ders) != 1 {
		return nil, errors.New("expected exactly one PEM certificate")
	}
	return ders[0], nil
}

func pemCertificates(data []byte) [][]byte {
	var ders [][]byte
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return ders
		}
		if block.Type == "CERTIFICATE" {
			ders = append(ders, block.Bytes)
		}
	}
}

func routeSystemPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// checkRouteSystemDir requires an existing directory that only the current
// user can change, because the service reads its key and certificate there.
func checkRouteSystemDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("routing directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("routing directory %s is not a directory", dir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("routing directory %s must be private; run chmod 700 on it", dir)
	}
	return routeSystemOwnedByUser(info)
}

// routeSystemServicePort keeps the chosen high port in the routing directory so
// retries and the privileged port redirect agree. Windows binds 443 directly
// and launchd binds it for the macOS user service.
func routeSystemServicePort(dir, id string) (int, error) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return 443, nil
	}
	path := filepath.Join(dir, routeSystemStateName)
	var st routeSystemState
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &st) == nil && st.ID == id && st.Port > 1024 && st.Port < 65536 {
		return st.Port, nil
	}
	for range 20 {
		ln4, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, fmt.Errorf("find a free loopback port: %w", err)
		}
		port := ln4.Addr().(*net.TCPAddr).Port
		ln6, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
		closeQuietly(ln4)
		if err != nil {
			continue
		}
		closeQuietly(ln6)
		data, _ := json.Marshal(routeSystemState{ID: id, Port: port})
		if err := writeFileAtomic(path, append(data, '\n'), 0o600); err != nil {
			return 0, err
		}
		return port, nil
	}
	return 0, errors.New("could not find a loopback port free on both IPv4 and IPv6")
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer removeStaged(tmp.Name(), &err)
	if _, err := tmp.Write(data); err != nil {
		closeQuietly(tmp)
		return err
	}
	if err := tmp.Chmod(mode); err != nil && runtime.GOOS != "windows" {
		closeQuietly(tmp)
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// routeSystemPeer reports whether anything accepts TCP at addr and, if it
// completes a TLS handshake for sni, the fingerprint of the presented leaf.
// It sends no application data.
func routeSystemPeer(ctx context.Context, addr, sni string) (connected bool, leaf string) {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false, ""
	}
	defer closeQuietly(conn)
	// The handshake also ends with ctx.
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	tc := tls.Client(conn, &tls.Config{ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if tc.HandshakeContext(ctx) != nil {
		return true, ""
	}
	if certs := tc.ConnectionState().PeerCertificates; len(certs) > 0 {
		return true, fingerprintHex(certs[0].Raw)
	}
	return true, ""
}

func waitRouteSystemService(ctx context.Context, p routeSystemPlan, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ok4 := routeSystemPeerIs(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port)), p)
		ok6 := routeSystemPeerIs(ctx, net.JoinHostPort("::1", strconv.Itoa(p.port)), p)
		if ok4 && ok6 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the redirect service did not answer on IPv4 and IPv6 loopback port %d; %s", p.port, routeSystemLogHint(p))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func routeSystemPeerIs(ctx context.Context, addr string, p routeSystemPlan) bool {
	_, leaf := routeSystemPeer(ctx, addr, p.aliases[0])
	return leaf == p.leafSHA256
}

// checkRouteSystem443 refuses to redirect loopback port 443 away from another
// program. Refused connections and this setup's own leaf are fine.
func checkRouteSystem443(ctx context.Context, p routeSystemPlan) error {
	for _, host := range []string{"127.0.0.1", "::1"} {
		connected, leaf := routeSystemPeer(ctx, net.JoinHostPort(host, "443"), p.aliases[0])
		if connected && leaf != p.leafSHA256 {
			return fmt.Errorf("another program already accepts connections on %s port 443; stop it or remove its port redirect first", host)
		}
	}
	return nil
}

// Use the same pinned dual-stack probe for setup and explicit verification.
func verifyRouteSystem(ctx context.Context, p routeSystemPlan, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		err := verifyManagedRoute(ctx, p.configFile, p.certFile, "", io.Discard)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// runRouteSystemCommand runs a system tool as the current user. Errors carry
// the tool's output but never script bodies or file contents.
var runRouteSystemCommand = func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		verb := filepath.Base(name)
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			verb += " " + args[0]
		}
		return out, fmt.Errorf("%s failed: %w%s", verb, err, routeSystemOutput(out))
	}
	return out, nil
}

func routeSystemOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return ""
	}
	if len(s) > 600 {
		s = s[:600] + "..."
	}
	return ": " + s
}
