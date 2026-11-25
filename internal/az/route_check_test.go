package az

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const routeCheckProfileAlias = "tfsaz-00112233445566778899aabb.visualstudio.com"

// routeCheckFixture runs the real redirect service on ephemeral loopback ports
// and returns a check whose dialer maps port 443 on each family onto them.
func routeCheckFixture(t *testing.T) (routeCheck, string, string, string) {
	t.Helper()
	configFile, registry, alias, collection := redirectTestSetup(t)
	cfg, err := loadConfigAt(configFile)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Profiles[1].Aliases = append(cfg.Profiles[1].Aliases, "https://"+routeCheckProfileAlias)
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, configFile, string(data))
	records, err := readSetupRegistry(registry)
	if err != nil {
		t.Fatal(err)
	}
	names := redirectAliasHosts(cfg, records)
	certFile, keyFile, _ := writeRedirectTestCert(t, t.TempDir(), redirectTestCert{names: []string{alias, routeCheckProfileAlias}})
	cert, certNames, err := loadRedirectCertificate(certFile, keyFile, names, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := readManagedRouteLeaf(certFile, names, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addresses := map[string]string{}
	var listeners []net.Listener
	for _, ip := range []string{"127.0.0.1", "::1"} {
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
		if err != nil {
			t.Skipf("loopback %s unavailable: %v", ip, err)
		}
		listeners = append(listeners, ln)
		addresses[net.JoinHostPort(ip, "443")] = ln.Addr().String()
	}
	ctx, cancel := context.WithCancel(t.Context())
	status := make(routeTestStatus, 1)
	done := make(chan error, 1)
	go func() { done <- serveRedirectListeners(ctx, listeners, cert, certNames, configFile, 443, status) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-status:
	case err := <-done:
		t.Fatalf("redirect service did not start: %v", err)
	}
	check := routeCheck{
		cfg:     cfg,
		records: records,
		names:   names,
		leaf:    leaf,
		port:    443,
		now:     time.Now,
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.IPv6loopback}}, nil
		},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			target, ok := addresses[address]
			if !ok {
				return nil, errors.New("unexpected dial " + address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, target)
		},
	}
	return check, alias, collection, certFile
}

func TestRouteCheckAllAliases(t *testing.T) {
	check, alias, _, _ := routeCheckFixture(t)
	var out bytes.Buffer
	if err := check.run(t.Context(), "", &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"alias " + alias + " (setup Project/Repo)",
		"open:     https://" + alias + "/Project/_git/Repo\n",
		"expected: https://server.invalid:8443/tfs/Main%20Collection/Project/_git/Repo\n",
		"alias " + routeCheckProfileAlias + " (profile modern)",
		"open:     https://" + routeCheckProfileAlias + "/" + routeCheckProject + "/_workitems/edit/1\n",
		"resolver: loopback only (IPv4 and IPv6)",
		"127.0.0.1:443: 302", "[::1]:443: 302",
		"not verified: whether the browser or OS trusts",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

func TestRouteCheckURLKeepsFragmentClientSide(t *testing.T) {
	check, alias, collection, _ := routeCheckFixture(t)
	const page = "/Project/_git/Repo/pullrequest/17?view=files"
	var out bytes.Buffer
	if err := check.run(t.Context(), "https://"+alias+page+"#discussion", &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "expected: "+collection+page+"#discussion\n") || strings.Count(out.String(), "alias ") != 1 {
		t.Fatalf("output:\n%s", out.String())
	}
	for name, raw := range map[string]string{
		"unregistered host": "https://example.visualstudio.com" + page,
		"explicit port":     "https://" + alias + ":443" + page,
		"credentials":       "https://user@" + alias + page,
		"plain HTTP":        "http://" + alias + page,
		"Git endpoint":      "https://" + alias + "/Project/_git/Repo/info/refs?service=git-upload-pack",
		"other repository":  "https://" + alias + "/Project/_git/Other",
	} {
		if err := check.run(t.Context(), raw, &bytes.Buffer{}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestRouteCheckFailures(t *testing.T) {
	check, alias, _, _ := routeCheckFixture(t)
	url := "https://" + alias + "/Project/_git/Repo"
	expect := func(name string, c routeCheck, want string) {
		t.Helper()
		err := c.run(t.Context(), url, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", name, err, want)
		}
	}

	remote := check
	remote.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("192.0.2.1")}}, nil
	}
	expect("non-loopback answer", remote, "not a tfsaz loopback")
	other := check
	other.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.2")}}, nil
	}
	expect("other loopback", other, "not a tfsaz loopback")
	ipv4Only := check
	ipv4Only.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	expect("missing IPv6 answer", ipv4Only, "both 127.0.0.1 and ::1")
	missing := check
	missing.lookup = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("no such host") }
	expect("unresolved", missing, "does not resolve")

	// A different managed leaf for the same alias must not accept the served one.
	otherFile, _, _ := writeRedirectTestCert(t, t.TempDir(), redirectTestCert{names: []string{alias}})
	otherLeaf, err := readManagedRouteLeaf(otherFile, check.names, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mismatch := check
	mismatch.leaf = otherLeaf
	expect("different leaf", mismatch, "not the managed leaf")

	closed := check
	closed.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		address := ln.Addr().String()
		ln.Close()
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	expect("nothing listening", closed, "nothing accepts connections")

	stray := check
	stray.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return check.dial(ctx, network, "127.0.0.1:443")
	}
	expect("IPv6 answered by IPv4", stray, "unexpected address")

	// Restoring the setup retires its alias in the running service.
	retired := check
	retired.records = nil
	retired.names = map[string]bool{alias: true}
	if err := retired.run(t.Context(), url, &bytes.Buffer{}); err == nil {
		t.Error("retired setup alias verified")
	}
}

func TestReadManagedRouteLeaf(t *testing.T) {
	const alias = "tfsaz-0123456789abcdef01234567.visualstudio.com"
	allowed := map[string]bool{alias: true}
	now := time.Now()
	dir := t.TempDir()
	certFile, keyFile, der := writeRedirectTestCert(t, dir, redirectTestCert{names: []string{alias}})
	if _, err := readManagedRouteLeaf(certFile, allowed, now); err != nil {
		t.Fatalf("managed leaf rejected: %v", err)
	}
	if _, err := readManagedRouteLeaf(keyFile, allowed, now); err == nil {
		t.Error("private key accepted as certificate")
	}
	_, _, caDER := writeRedirectTestCert(t, t.TempDir(), redirectTestCert{ca: true})
	bundle := filepath.Join(t.TempDir(), "bundle.pem")
	managerTestWrite(t, bundle, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))+
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})))
	if _, err := readManagedRouteLeaf(bundle, allowed, now); err == nil {
		t.Error("bundle accepted")
	}
	for name, spec := range map[string]redirectTestCert{
		"CA":           {names: []string{alias}, ca: true},
		"wildcard":     {names: []string{alias, "*.visualstudio.com"}},
		"unregistered": {names: []string{alias, "dev.azure.com"}},
		"IP name":      {names: []string{alias}, ips: []net.IP{net.IPv4(127, 0, 0, 1)}},
		"expired":      {names: []string{alias}, notBefore: now.Add(-48 * time.Hour), notAfter: now.Add(-24 * time.Hour)},
	} {
		file, _, _ := writeRedirectTestCert(t, t.TempDir(), spec)
		if _, err := readManagedRouteLeaf(file, allowed, now); err == nil {
			t.Errorf("%s certificate accepted", name)
		}
	}
}

func TestVerifyManagedRouteRequiresAlias(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(httpsRouteTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, file, string(data))
	err = verifyManagedRoute(t.Context(), file, "cert.pem", "", &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no dedicated") {
		t.Fatalf("got %v", err)
	}
}
