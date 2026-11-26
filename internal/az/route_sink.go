package az

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

func routeHostnames(cfg config, records []setupRecord, shared bool) ([]string, error) {
	realHosts := make(map[string]bool)
	for _, p := range cfg.Profiles {
		u, err := url.Parse(p.CollectionURL)
		if err != nil {
			return nil, errors.New("invalid collection URL")
		}
		realHosts[strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))] = true
	}
	var names []string
	seen := make(map[string]bool)
	var candidates []string
	for _, binding := range httpsRouteBindings(cfg, records) {
		candidates = append(candidates, binding.base.Hostname())
	}
	// A transaction in progress still owns its alias even when it cannot route.
	for _, r := range records {
		if r.Host == setupHost(r.ProfileID, r.CollectionURL, r.Project, r.Repository) {
			candidates = append(candidates, r.Host, "dev.azure.com")
		}
	}
	for _, host := range candidates {
		name := strings.ToLower(host)
		if seen[name] || name == "dev.azure.com" && !shared {
			continue
		}
		if realHosts[name] || name == "marketplace.visualstudio.com" || name == "vs-ssh.visualstudio.com" {
			return nil, errors.New("the host list overlaps a collection server or shared Microsoft service")
		}
		names = append(names, name)
		seen[name] = true
	}
	slices.Sort(names)
	if len(names) == 0 {
		return nil, errors.New("no dedicated alias hostnames found; the shared dev.azure.com hostname requires --include-dev-azure")
	}
	return names, nil
}

func (a app) routeHosts(ctx context.Context, cmd string, cfg config, records []setupRecord, shared bool) error {
	names, err := routeHostnames(cfg, records, shared)
	if err != nil {
		return err
	}
	for _, name := range names {
		if cmd == "hosts" {
			if _, err := fmt.Fprintf(a.stdout, "127.0.0.1 %s\n::1 %s\n", name, name); err != nil {
				return err
			}
			continue
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, name)
		if err != nil || len(ips) == 0 {
			return fmt.Errorf("could not resolve registered hostname %s", name)
		}
		for _, ip := range ips {
			if !ip.IP.Equal(net.IPv4(127, 0, 0, 1)) && !ip.IP.Equal(net.IPv6loopback) {
				return fmt.Errorf("registered hostname %s resolves outside the local sink", name)
			}
		}
		if _, err := fmt.Fprintln(a.stdout, name, "loopback"); err != nil {
			return err
		}
	}
	return nil
}

// A deny-only listener. Never read TLS, HTTP, credentials or any other bytes.
// Bind both loopback families before reporting readiness.
func runRouteSink(ctx context.Context, port string, status io.Writer) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return usageError{errors.New("sink port must be between 1 and 65535")}
	}
	var listeners []net.Listener
	for _, addr := range []string{"127.0.0.1", "::1"} {
		ln, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(n)))
		if err != nil {
			for _, l := range listeners {
				closeQuietly(l)
			}
			return fmt.Errorf("could not bind loopback port %d: %w", n, err)
		}
		listeners = append(listeners, ln)
	}
	var refused atomic.Uint64
	var workers sync.WaitGroup
	done := make(chan error, len(listeners))
	defer func() {
		for _, ln := range listeners {
			closeQuietly(ln)
		}
		workers.Wait()
		// A summary at shutdown; the result is already decided.
		_, _ = fmt.Fprintf(status, "tfsaz: refused %d TCP connection(s)\n", refused.Load())
	}()
	for _, ln := range listeners {
		workers.Go(func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					done <- err
					return
				}
				refused.Add(1)
				closeQuietly(conn)
			}
		})
	}
	if _, err := fmt.Fprintf(status, "tfsaz: refusing TCP connections on IPv4 and IPv6 loopback port %d\n", n); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-done:
		return err
	}
}
