package az

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// routeCheckProject names a synthetic project for profile aliases, which have
// no bound repository. The probe only reaches the local redirect service.
const routeCheckProject = "tfsaz-route-check"

// routeCheck holds the inputs of one verification run. Tests replace the
// resolver, dialer and clock; production pins both loopbacks on port 443.
type routeCheck struct {
	cfg     config
	records []setupRecord
	names   map[string]bool
	leaf    *x509.Certificate
	port    int
	now     func() time.Time
	lookup  func(ctx context.Context, host string) ([]net.IPAddr, error)
	dial    func(ctx context.Context, network, address string) (net.Conn, error)
}

// routeCheckTarget is one alias page and what the client should end up at.
type routeCheckTarget struct {
	alias    string
	label    string
	open     string // URL the user opens, fragment included
	request  string // URL sent over HTTP, fragment stripped
	location string // Location the redirect service must return
	expected string // destination the browser shows, fragment included
}

// verifyManagedRoute checks the local redirect path for registered aliases
// without changing the machine. It sends no credential and follows no redirect.
func verifyManagedRoute(ctx context.Context, configFile, certFile, rawURL string, stdout io.Writer) error {
	if configFile == "" {
		var err error
		if configFile, err = configPath(); err != nil {
			return err
		}
	}
	if certFile == "" {
		return errors.New("expected the managed redirect certificate file")
	}
	cfg, err := loadConfigAt(configFile)
	if err != nil {
		return err
	}
	records, err := readSetupRegistry(setupRegistryForConfig(configFile))
	if err != nil {
		return err
	}
	check := routeCheck{
		cfg:     cfg,
		records: records,
		names:   redirectAliasHosts(cfg, records),
		port:    443,
		now:     time.Now,
		lookup:  net.DefaultResolver.LookupIPAddr,
		dial:    (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	if len(check.names) == 0 {
		return errors.New("no dedicated tfsaz-*.visualstudio.com aliases are registered; set up a checkout first")
	}
	if check.leaf, err = readManagedRouteLeaf(certFile, check.names, check.now()); err != nil {
		return err
	}
	return check.run(ctx, rawURL, stdout)
}

func (c routeCheck) run(ctx context.Context, rawURL string, stdout io.Writer) error {
	var targets []routeCheckTarget
	if rawURL != "" {
		target, err := c.targetForURL(rawURL)
		if err != nil {
			return err
		}
		targets = append(targets, target)
	} else {
		aliases := make([]string, 0, len(c.names))
		for alias := range c.names {
			aliases = append(aliases, alias)
		}
		slices.Sort(aliases)
		for _, alias := range aliases {
			target, err := c.targetForAlias(alias)
			if err != nil {
				return fmt.Errorf("alias %s: %w", alias, err)
			}
			targets = append(targets, target)
		}
	}
	var failures []error
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if err := c.checkTarget(ctx, target, stdout); err != nil {
			failures = append(failures, fmt.Errorf("alias %s: %w", target.alias, err))
		}
	}
	if _, err := fmt.Fprintln(stdout, "not verified: whether the browser or OS trusts the managed certificate, and sign-in at the destination"); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

// targetForAlias infers an allowlisted page from the alias binding. A setup
// alias uses its repository page; a profile alias uses a synthetic work item.
func (c routeCheck) targetForAlias(alias string) (routeCheckTarget, error) {
	for _, binding := range httpsRouteBindings(c.cfg, c.records) {
		if binding.base.Host != alias {
			continue
		}
		path := "/" + routeCheckProject + "/_workitems/edit/1"
		label := "profile " + binding.profile.ID
		if binding.project != "" {
			path = "/" + url.PathEscape(binding.project) + "/_git/" + url.PathEscape(binding.repository)
			label = "setup " + binding.project + "/" + binding.repository
		}
		u, err := url.Parse("https://" + alias + path)
		if err != nil {
			return routeCheckTarget{}, err
		}
		return c.target(alias, label, u)
	}
	return routeCheckTarget{}, errors.New("no route binding found")
}

func (c routeCheck) targetForURL(raw string) (routeCheckTarget, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil {
		return routeCheckTarget{}, errors.New("use an HTTPS alias URL without embedded credentials")
	}
	alias := strings.ToLower(u.Host)
	if u.Port() != "" || !c.names[alias] {
		return routeCheckTarget{}, errors.New("the URL host is not exactly a registered dedicated tfsaz alias")
	}
	return c.target(alias, "requested URL", u)
}

func (c routeCheck) target(alias, label string, u *url.URL) (routeCheckTarget, error) {
	route, err := mapHTTPSRoute(c.cfg, c.records, u.String())
	if err != nil {
		return routeCheckTarget{}, err
	}
	if route.Action != httpsActionRedirect || !strings.HasPrefix(route.Destination, "https://") {
		return routeCheckTarget{}, errors.New("the URL is not a browser page the redirect service serves")
	}
	// The fragment never reaches the server; browsers reapply it to Location.
	stripped := *u
	stripped.Fragment, stripped.RawFragment = "", ""
	location, err := mapHTTPSRoute(c.cfg, c.records, stripped.String())
	if err != nil {
		return routeCheckTarget{}, err
	}
	return routeCheckTarget{
		alias:    alias,
		label:    label,
		open:     u.String(),
		request:  stripped.String(),
		location: location.Destination,
		expected: route.Destination,
	}, nil
}

func (c routeCheck) checkTarget(ctx context.Context, target routeCheckTarget, out io.Writer) error {
	if _, err := fmt.Fprintf(out, "alias %s (%s)\n", target.alias, target.label); err != nil {
		return err
	}
	if err := c.leaf.VerifyHostname(target.alias); err != nil {
		return errors.New("the managed certificate does not name this alias")
	}
	families, err := c.checkResolver(ctx, target.alias)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "  resolver: loopback only (%s)\n", families); err != nil {
		return err
	}
	var failures []error
	for _, ip := range []string{"127.0.0.1", "::1"} {
		address := net.JoinHostPort(ip, strconv.Itoa(c.port))
		if err := c.probe(ctx, target, address); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", address, err))
			continue
		}
		if _, err := fmt.Fprintf(out, "  %s: 302 to expected Location, no-store, no-referrer, managed leaf\n", address); err != nil {
			return err
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	_, err = fmt.Fprintf(out, "  open:     %s\n  expected: %s\n", target.open, target.expected)
	return err
}

// checkResolver accepts only the two loopback addresses the redirect service
// binds. Any other answer would send the browser somewhere else.
func (c routeCheck) checkResolver(ctx context.Context, alias string) (string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, err := c.lookup(lookupCtx, alias)
	if err != nil || len(ips) == 0 {
		return "", errors.New("the local resolver does not resolve this alias; add the tfsaz route hosts entries")
	}
	var v4, v6 bool
	for _, ip := range ips {
		switch {
		case ip.IP.Equal(net.IPv4(127, 0, 0, 1)):
			v4 = true
		case ip.IP.Equal(net.IPv6loopback):
			v6 = true
		default:
			return "", fmt.Errorf("the local resolver returns %s, which is not a tfsaz loopback address", ip.IP)
		}
	}
	if !v4 || !v6 {
		return "", errors.New("the resolver must return both 127.0.0.1 and ::1 for this alias")
	}
	return "IPv4 and IPv6", nil
}

// probe sends one HEAD request to a pinned loopback address. It uses no proxy,
// cookie jar or credential, and it reports the redirect instead of following it.
func (c routeCheck) probe(ctx context.Context, target routeCheckTarget, address string) error {
	want, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(c.leaf)
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := c.dial(ctx, "tcp", address)
			if err != nil {
				return nil, err
			}
			if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); !ok || !tcp.IP.Equal(net.ParseIP(want)) {
				closeQuietly(conn)
				return nil, errors.New("connected to an unexpected address")
			}
			return conn, nil
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: target.alias,
			// The managed leaf is the only root, so the chain and hostname are
			// checked against it. The exact bytes are compared as well.
			RootCAs: roots,
			Time:    c.now,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 || !bytes.Equal(cs.PeerCertificates[0].Raw, c.leaf.Raw) {
					return errServedLeafMismatch
				}
				return nil
			},
		},
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target.request, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return describeRouteProbeError(err)
	}
	closeQuietly(resp.Body)
	switch {
	case resp.StatusCode != http.StatusFound:
		return fmt.Errorf("got HTTP %d, want 302; check that the redirect service runs with the current configuration", resp.StatusCode)
	case resp.Header.Get("Location") != target.location:
		return fmt.Errorf("Location is %q, want %q", resp.Header.Get("Location"), target.location)
	case resp.Header.Get("Cache-Control") != "no-store":
		return errors.New("the redirect is missing Cache-Control: no-store")
	case resp.Header.Get("Referrer-Policy") != "no-referrer":
		return errors.New("the redirect is missing Referrer-Policy: no-referrer")
	}
	return nil
}

var errServedLeafMismatch = errors.New("the served certificate is not the managed leaf")

func describeRouteProbeError(err error) error {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var opErr *net.OpError
	switch {
	case errors.Is(err, errServedLeafMismatch), errors.As(err, &unknown):
		return errServedLeafMismatch
	case errors.As(err, &hostname):
		return errors.New("the served certificate does not name this alias")
	case errors.As(err, &invalid):
		return fmt.Errorf("the served certificate is not valid: %v", invalid.Error())
	case errors.As(err, &opErr) && opErr.Op == "dial":
		return errors.New("nothing accepts connections here; check the service installed by tfsaz route setup")
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("the redirect service did not answer in time")
	}
	return fmt.Errorf("HTTPS request failed: %w", err)
}

// readManagedRouteLeaf mirrors loadRedirectCertificate without the private key,
// so the comparison uses the same leaf the redirect service may serve.
func readManagedRouteLeaf(certFile string, allowed map[string]bool, now time.Time) (*x509.Certificate, error) {
	data, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("read managed redirect certificate: %w", err)
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("the managed redirect certificate file must contain exactly one PEM certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse managed redirect certificate: %w", err)
	}
	if _, err := validateRedirectLeaf(leaf, allowed, now); err != nil {
		return nil, err
	}
	return leaf, nil
}
