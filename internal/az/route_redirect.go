package az

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/netutil"
)

// Only hostnames produced by setupHost qualify. A shorter tfsaz-* label could
// belong to a real cloud organization, and shared hosts serve many tenants.
var generatedAliasHost = regexp.MustCompile(`^tfsaz-[0-9a-f]{24}\.visualstudio\.com$`)

// redirectAliasHosts lists the dedicated hostnames that currently route to a
// configured collection through a profile or an active setup.
func redirectAliasHosts(cfg config, records []setupRecord) map[string]bool {
	realHosts := make(map[string]bool)
	for _, p := range cfg.Profiles {
		if u, err := url.Parse(p.CollectionURL); err == nil {
			realHosts[strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))] = true
		}
	}
	names := make(map[string]bool)
	for _, binding := range httpsRouteBindings(cfg, records) {
		host := binding.base.Host
		if generatedAliasHost.MatchString(host) && !realHosts[host] {
			names[host] = true
		}
	}
	return names
}

func parseRedirectOptions(args []string) (certFile, keyFile string, port int, err error) {
	port = 443
	gotPort := false
	usage := usageError{errors.New("expected route redirect --cert FILE --key FILE [--port PORT]")}
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) || args[i+1] == "" {
			return "", "", 0, usage
		}
		value := args[i+1]
		switch {
		case args[i] == "--cert" && certFile == "":
			certFile = value
		case args[i] == "--key" && keyFile == "":
			keyFile = value
		case args[i] == "--port" && !gotPort:
			gotPort = true
			port, err = strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return "", "", 0, usageError{errors.New("redirect port must be between 1 and 65535")}
			}
		default:
			return "", "", 0, usage
		}
		i++
	}
	if certFile == "" || keyFile == "" {
		return "", "", 0, usage
	}
	return certFile, keyFile, port, nil
}

// loadRedirectCertificate accepts only an unexpired server leaf whose every
// name is an exact registered alias. Client trust is configured separately.
func loadRedirectCertificate(certFile, keyFile string, allowed map[string]bool, now time.Time) (tls.Certificate, map[string]bool, error) {
	if runtime.GOOS != "windows" {
		info, err := os.Stat(keyFile)
		if err != nil {
			return tls.Certificate{}, nil, fmt.Errorf("read redirect private key: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return tls.Certificate{}, nil, errors.New("the redirect private key must be readable only by its owner; run chmod 600 on it")
		}
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load redirect certificate and key: %w", err)
	}
	if len(cert.Certificate) != 1 {
		return tls.Certificate{}, nil, errors.New("--cert must contain exactly one leaf certificate")
	}
	leaf := cert.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			return tls.Certificate{}, nil, fmt.Errorf("parse redirect certificate: %w", err)
		}
		cert.Leaf = leaf
	}
	names, err := validateRedirectLeaf(leaf, allowed, now)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return cert, names, nil
}

func validateRedirectLeaf(leaf *x509.Certificate, allowed map[string]bool, now time.Time) (map[string]bool, error) {
	switch {
	case !leaf.BasicConstraintsValid || leaf.IsCA || leaf.KeyUsage&(x509.KeyUsageCertSign|x509.KeyUsageCRLSign) != 0:
		return nil, errors.New("the redirect certificate must be a leaf with basicConstraints CA:FALSE, not a CA")
	case len(leaf.IPAddresses) > 0 || len(leaf.EmailAddresses) > 0 || len(leaf.URIs) > 0:
		return nil, errors.New("the redirect certificate may contain only DNS names")
	case len(leaf.DNSNames) == 0:
		return nil, errors.New("the redirect certificate needs a subjectAltName DNS entry for a registered alias")
	case !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) || slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageAny):
		return nil, errors.New("the redirect certificate must have extendedKeyUsage serverAuth and no anyExtendedKeyUsage")
	case now.Before(leaf.NotBefore) || now.After(leaf.NotAfter):
		return nil, errors.New("the redirect certificate is expired or not yet valid")
	}
	names := make(map[string]bool, len(leaf.DNSNames))
	for _, name := range leaf.DNSNames {
		lower := strings.ToLower(name)
		if strings.Contains(name, "*") {
			return nil, errors.New("the redirect certificate must not contain wildcard names")
		}
		if !allowed[lower] {
			return nil, fmt.Errorf("certificate name %s is not a registered dedicated tfsaz alias", name)
		}
		names[lower] = true
	}
	return names, nil
}

func serveRouteRedirect(ctx context.Context, certFile, keyFile string, port int, status io.Writer) error {
	return serveRouteRedirectOn(ctx, certFile, keyFile, port, status, listenRedirectLoopback)
}

func listenRedirectLoopback(port int) ([]net.Listener, error) {
	var listeners []net.Listener
	for _, addr := range []string{"127.0.0.1", "::1"} {
		ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(port)))
		if err != nil {
			for _, l := range listeners {
				closeQuietly(l)
			}
			if errors.Is(err, os.ErrPermission) {
				return nil, fmt.Errorf("could not bind loopback port %d: %w; this OS reserves low ports, so use --port with a loopback port redirect as described in https://github.com/toolspell/tfsaz/blob/main/docs/user/browser-routing-manual.md#5-run-the-service-on-port-443", port, err)
			}
			return nil, fmt.Errorf("could not bind loopback port %d: %w", port, err)
		}
		listeners = append(listeners, ln)
	}
	return listeners, nil
}

// serveRouteRedirectOn validates the configuration and certificate before it
// obtains listeners, so a misconfigured service never accepts a connection.
func serveRouteRedirectOn(ctx context.Context, certFile, keyFile string, port int, status io.Writer, listen func(int) ([]net.Listener, error)) error {
	configFile, err := configPath()
	if err != nil {
		return err
	}
	cfg, err := loadConfigAt(configFile)
	if err != nil {
		return err
	}
	records, err := readSetupRegistry(setupRegistryForConfig(configFile))
	if err != nil {
		return err
	}
	allowed := redirectAliasHosts(cfg, records)
	if len(allowed) == 0 {
		return errors.New("no dedicated tfsaz-*.visualstudio.com aliases are registered; set up a checkout first")
	}
	cert, names, err := loadRedirectCertificate(certFile, keyFile, allowed, time.Now())
	if err != nil {
		return err
	}
	listeners, err := listen(port)
	if err != nil {
		return err
	}
	return serveRedirectListeners(ctx, listeners, cert, names, configFile, port, status)
}

func serveRedirectListeners(ctx context.Context, listeners []net.Listener, cert tls.Certificate, names map[string]bool,
	configFile string, port int, status io.Writer) error {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			// Missing SNI, IP literals and other names fail the handshake.
			if !names[strings.ToLower(hello.ServerName)] {
				return nil, errors.New("unregistered server name")
			}
			return &cert, nil
		},
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	srv := &http.Server{
		Handler:           redirectHandler{configFile: configFile, names: names, port: port},
		TLSConfig:         tlsConfig,
		Protocols:         protocols,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// Handshake and protocol errors can include peer details or names.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	done := make(chan error, len(listeners))
	var workers sync.WaitGroup
	for _, ln := range listeners {
		workers.Go(func() { done <- srv.Serve(tls.NewListener(netutil.LimitListener(ln, 64), tlsConfig)) })
	}
	defer func() {
		closeQuietly(srv)
		workers.Wait()
	}()
	if _, err := fmt.Fprintf(status, "tfsaz: redirecting %d registered alias(es) on loopback port %d; other names, methods and paths are refused\n", len(names), port); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type redirectHandler struct {
	configFile string
	names      map[string]bool
	port       int
}

// ServeHTTP never reads the request body, forwards a request, or logs any part
// of it. Each request re-reads the configuration so restored setups stop at once.
func (h redirectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	refuse := func(code int) {
		header.Set("Connection", "close")
		header.Set("Content-Length", "0")
		w.WriteHeader(code)
	}
	if r.TLS == nil {
		refuse(http.StatusMisdirectedRequest)
		return
	}
	sni := strings.ToLower(r.TLS.ServerName)
	host, port := r.Host, ""
	if strings.Contains(host, ":") {
		var err error
		if host, port, err = net.SplitHostPort(host); err != nil {
			refuse(http.StatusBadRequest)
			return
		}
	}
	if !h.names[sni] || !strings.EqualFold(host, sni) || (port != "" && port != strconv.Itoa(h.port)) {
		refuse(http.StatusMisdirectedRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		header.Set("Allow", "GET, HEAD")
		refuse(http.StatusMethodNotAllowed)
		return
	}
	// A client that sends credentials or a body expects a different endpoint.
	if r.ContentLength != 0 || len(r.TransferEncoding) > 0 || !strings.HasPrefix(r.RequestURI, "/") {
		refuse(http.StatusBadRequest)
		return
	}
	if _, ok := r.Header["Authorization"]; ok {
		refuse(http.StatusForbidden)
		return
	}
	cfg, err := loadConfigAt(h.configFile)
	if err != nil {
		refuse(http.StatusServiceUnavailable)
		return
	}
	records, err := readSetupRegistry(setupRegistryForConfig(h.configFile))
	if err != nil {
		refuse(http.StatusServiceUnavailable)
		return
	}
	if !redirectAliasHosts(cfg, records)[sni] {
		refuse(http.StatusNotFound)
		return
	}
	route, err := mapHTTPSRoute(cfg, records, "https://"+sni+r.RequestURI)
	if err != nil || route.Action != httpsActionRedirect || !strings.HasPrefix(route.Destination, "https://") {
		refuse(http.StatusNotFound)
		return
	}
	// Browsers keep the original fragment when Location has none (RFC 9110).
	header.Set("Location", route.Destination)
	header.Set("Content-Length", "0")
	w.WriteHeader(http.StatusFound)
}
