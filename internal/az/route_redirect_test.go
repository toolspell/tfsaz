package az

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type redirectTestCert struct {
	names     []string
	ca        bool
	ips       []net.IP
	usage     []x509.ExtKeyUsage
	notBefore time.Time
	notAfter  time.Time
}

func writeRedirectTestCert(t *testing.T, dir string, spec redirectTestCert) (certFile, keyFile string, der []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if spec.usage == nil {
		spec.usage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	if spec.notBefore.IsZero() {
		spec.notBefore = time.Now().Add(-time.Hour)
		spec.notAfter = time.Now().Add(time.Hour)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "tfsaz test"},
		DNSNames:              spec.names,
		IPAddresses:           spec.ips,
		NotBefore:             spec.notBefore,
		NotAfter:              spec.notAfter,
		BasicConstraintsValid: true,
		IsCA:                  spec.ca,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           spec.usage,
	}
	if spec.ca {
		template.KeyUsage |= x509.KeyUsageCertSign
	}
	der, err = x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	managerTestWrite(t, certFile, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	managerTestWrite(t, keyFile, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})))
	return certFile, keyFile, der
}

// redirectTestSetup writes a config with one active managed setup and returns
// the config path, registry path, generated alias and native collection URL.
func redirectTestSetup(t *testing.T) (configFile, registry, alias, collection string) {
	t.Helper()
	root := t.TempDir()
	configFile = filepath.Join(root, "config.json")
	cfg := httpsRouteTestConfig()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, configFile, string(data))
	p := cfg.Profiles[0]
	r, err := makePreserveSetupRecord(cfg, setupCheckout{repo: root, gitDir: filepath.Join(root, ".git")}, p.CollectionURL+"/Project/_git/Repo", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.Phase = setupPhaseActive
	data, err = json.Marshal([]setupRecord{r})
	if err != nil {
		t.Fatal(err)
	}
	registry = setupRegistryForConfig(configFile)
	managerTestWrite(t, registry, string(data))
	return configFile, registry, r.Host, p.CollectionURL
}

func TestRedirectAliasHostsOnlyDedicated(t *testing.T) {
	configFile, registry, alias, _ := redirectTestSetup(t)
	cfg, err := loadConfigAt(configFile)
	if err != nil {
		t.Fatal(err)
	}
	records, err := readSetupRegistry(registry)
	if err != nil {
		t.Fatal(err)
	}
	const profileAlias = "tfsaz-00112233445566778899aabb.visualstudio.com"
	cfg.Profiles[1].Aliases = append(cfg.Profiles[1].Aliases, "https://"+profileAlias, "https://tfsaz-short.visualstudio.com")
	names := redirectAliasHosts(cfg, records)
	if len(names) != 2 || !names[alias] || !names[profileAlias] {
		t.Fatalf("hosts %v; want only %s and %s", names, alias, profileAlias)
	}
	// Restored or interrupted setups stop counting immediately.
	records[0].Phase = "restoring"
	if names := redirectAliasHosts(cfg, records); names[alias] {
		t.Fatalf("inactive setup still registered: %v", names)
	}
	// A collection that really lives on a tfsaz-* host is never shadowed.
	cfg.Profiles[1].CollectionURL = "https://" + profileAlias + "/Root"
	if names := redirectAliasHosts(cfg, nil); names[profileAlias] {
		t.Fatal("redirect would shadow a real collection host")
	}
}

func TestRedirectOptions(t *testing.T) {
	t.Setenv("TFSAZ_CONFIG", filepath.Join(t.TempDir(), "absent.json"))
	a := app{stdout: io.Discard, stderr: io.Discard}
	for _, args := range [][]string{
		{}, {"--cert", "c"}, {"--key", "k"}, {"--cert", "c", "--key"},
		{"--cert", "c", "--key", "k", "--port", "0"}, {"--cert", "c", "--key", "k", "--port", "x"},
		{"--cert", "c", "--cert", "d", "--key", "k"}, {"--cert", "c", "--key", "k", "--include-dev-azure"},
		{"--cert", "", "--key", "k"},
	} {
		if err := a.run(t.Context(), append([]string{"route", "redirect"}, args...)); ExitCode(err) != 2 {
			t.Errorf("route redirect %q: got %v, want usage error", args, err)
		}
	}
	certFile, keyFile, port, err := parseRedirectOptions([]string{"--port", "8443", "--key", "k", "--cert", "c"})
	if err != nil || certFile != "c" || keyFile != "k" || port != 8443 {
		t.Fatalf("parsed %q %q %d %v", certFile, keyFile, port, err)
	}
	if _, _, port, _ := parseRedirectOptions([]string{"--cert", "c", "--key", "k"}); port != 443 {
		t.Fatalf("default port %d", port)
	}
}

func TestRedirectCertificateRequiresExactLeaf(t *testing.T) {
	const alias = "tfsaz-0123456789abcdef01234567.visualstudio.com"
	const other = "tfsaz-76543210fedcba9876543210.visualstudio.com"
	allowed := map[string]bool{alias: true, other: true}
	now := time.Now()
	good := func(dir string) error {
		_, _, err := loadRedirectCertificate(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"), allowed, now)
		return err
	}
	dir := t.TempDir()
	writeRedirectTestCert(t, dir, redirectTestCert{names: []string{alias, strings.ToUpper(other)}})
	if err := good(dir); err != nil {
		t.Fatalf("explicit alias leaf rejected: %v", err)
	}
	bundle, _, leafDER := writeRedirectTestCert(t, dir, redirectTestCert{names: []string{alias}})
	_, _, caDER := writeRedirectTestCert(t, t.TempDir(), redirectTestCert{ca: true})
	managerTestWrite(t, bundle, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))+
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})))
	if err := good(dir); err == nil {
		t.Error("accepted leaf bundled with a CA")
	}
	for name, spec := range map[string]redirectTestCert{
		"CA":             {names: []string{alias}, ca: true},
		"wildcard":       {names: []string{"*.visualstudio.com"}},
		"wildcard alias": {names: []string{alias, "*.visualstudio.com"}},
		"unregistered":   {names: []string{alias, "example.visualstudio.com"}},
		"shared host":    {names: []string{alias, "dev.azure.com"}},
		"IP name":        {names: []string{alias}, ips: []net.IP{net.IPv4(127, 0, 0, 1)}},
		"no DNS name":    {ips: []net.IP{net.IPv6loopback}},
		"client only":    {names: []string{alias}, usage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
		"any usage":      {names: []string{alias}, usage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageAny}},
		"expired":        {names: []string{alias}, notBefore: now.Add(-48 * time.Hour), notAfter: now.Add(-24 * time.Hour)},
		"not yet valid":  {names: []string{alias}, notBefore: now.Add(24 * time.Hour), notAfter: now.Add(48 * time.Hour)},
	} {
		dir := t.TempDir()
		writeRedirectTestCert(t, dir, spec)
		if err := good(dir); err == nil {
			t.Errorf("%s certificate accepted", name)
		}
	}
	if runtime.GOOS != "windows" {
		dir := t.TempDir()
		_, keyFile, _ := writeRedirectTestCert(t, dir, redirectTestCert{names: []string{alias}})
		if err := os.Chmod(keyFile, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := good(dir); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("group-readable key accepted: %v", err)
		}
	}
}

func TestRouteRedirectService(t *testing.T) {
	configFile, registry, alias, collection := redirectTestSetup(t)
	t.Setenv("TFSAZ_CONFIG", configFile)
	cfg, err := loadConfigAt(configFile)
	if err != nil {
		t.Fatal(err)
	}
	records, err := readSetupRegistry(registry)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile, der := writeRedirectTestCert(t, t.TempDir(), redirectTestCert{names: []string{alias}})
	cert, names, err := loadRedirectCertificate(certFile, keyFile, redirectAliasHosts(cfg, records), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	status := make(routeTestStatus, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveRedirectListeners(ctx, []net.Listener{ln}, cert, names, configFile, port, status)
	}()
	select {
	case <-status:
	case err := <-done:
		t.Fatalf("redirect service did not start: %v", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	client := func(serverName string) *http.Client {
		return &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp4", address)
				},
				TLSClientConfig:   &tls.Config{RootCAs: roots, ServerName: serverName},
				ForceAttemptHTTP2: true,
				DisableKeepAlives: true,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	type request struct {
		name, method, url, host, auth, body string
		status                              int
		location                            string
	}
	const pr = "/Project/_git/Repo/pullrequest/17?view=files"
	base := "https://" + alias
	for _, tc := range []request{
		{name: "PR page", method: "GET", url: base + pr, status: http.StatusFound, location: collection + pr},
		{name: "fragment stays client-side", method: "GET", url: base + pr + "#x=y", status: http.StatusFound, location: collection + pr},
		{name: "HEAD", method: "HEAD", url: base + "/Project/_git/Repo", status: http.StatusFound, location: collection + "/Project/_git/Repo"},
		{name: "explicit port", method: "GET", url: "https://" + net.JoinHostPort(alias, portText) + pr, status: http.StatusFound, location: collection + pr},
		{name: "wrong port", method: "GET", url: base + pr, host: alias + ":1", status: http.StatusMisdirectedRequest},
		{name: "Host mismatch", method: "GET", url: base + pr, host: "other.example", status: http.StatusMisdirectedRequest},
		{name: "POST", method: "POST", url: base + pr, status: http.StatusMethodNotAllowed},
		{name: "DELETE", method: "DELETE", url: base + pr, status: http.StatusMethodNotAllowed},
		{name: "body", method: "GET", url: base + pr, body: "x", status: http.StatusBadRequest},
		{name: "Authorization", method: "GET", url: base + pr, auth: "Basic Og==", status: http.StatusForbidden},
		{name: "Git fetch", method: "GET", url: base + "/Project/_git/Repo/info/refs?service=git-upload-pack", status: http.StatusNotFound},
		{name: "REST API", method: "GET", url: base + "/_apis/projects", status: http.StatusNotFound},
		{name: "other repository", method: "GET", url: base + "/Project/_git/Other", status: http.StatusNotFound},
		{name: "dot segments", method: "GET", url: base + "/Project/_git/Repo/%2e%2e/%2e%2e/_apis", status: http.StatusNotFound},
	} {
		var body io.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		req, err := http.NewRequestWithContext(ctx, tc.method, tc.url, body)
		if err != nil {
			t.Fatal(err)
		}
		if tc.host != "" {
			req.Host = tc.host
		}
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		resp, err := client(alias).Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		closeQuietly(resp.Body)
		if resp.StatusCode != tc.status || resp.Header.Get("Location") != tc.location || resp.ProtoMajor != 1 {
			t.Errorf("%s: %s %d Location %q; want %d %q", tc.name, resp.Proto, resp.StatusCode, resp.Header.Get("Location"), tc.status, tc.location)
		}
		if tc.status == http.StatusFound && resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: redirect is cacheable", tc.name)
		}
	}
	// Names outside the certificate fail during the handshake, before any request.
	for _, serverName := range []string{"", "127.0.0.1", "dev.azure.com", "tfsaz-76543210fedcba9876543210.visualstudio.com"} {
		conn, err := tls.Dial("tcp4", address, &tls.Config{ServerName: serverName, InsecureSkipVerify: true})
		if err == nil {
			err = conn.Handshake()
			closeQuietly(conn)
		}
		if err == nil {
			t.Errorf("handshake for SNI %q succeeded", serverName)
		}
	}
	// Restoring the checkout retires its alias without restarting the service.
	if err := os.Remove(registry); err != nil {
		t.Fatal(err)
	}
	resp, err := client(alias).Get(base + pr)
	if err != nil {
		t.Fatal(err)
	}
	closeQuietly(resp.Body)
	if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Location") != "" {
		t.Fatalf("restored alias still redirects: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("redirect service did not stop on cancellation")
	}
}

func TestRouteRedirectRefusesWithoutDedicatedAlias(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(httpsRouteTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, file, string(data))
	t.Setenv("TFSAZ_CONFIG", file)
	err = serveRouteRedirect(t.Context(), "cert.pem", "key.pem", 443, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no dedicated") {
		t.Fatalf("got %v", err)
	}
}
