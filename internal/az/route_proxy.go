package az

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Serve the PAC document and refuse SOCKS5 and HTTP proxy requests on the same loopback
// port. There is no outbound dial, TLS termination, or authentication method.
func serveRouteProxy(ctx context.Context, hosts []string, port int, status io.Writer) error {
	var pac bytes.Buffer
	if err := writeRoutePAC(&pac, hosts, port); err != nil {
		return err
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp4", address)
	if err != nil {
		return fmt.Errorf("could not open local PAC/proxy port: %w", err)
	}
	defer closeQuietly(ln)
	stop := context.AfterFunc(ctx, func() { closeQuietly(ln) })
	defer stop()
	if _, err := fmt.Fprintf(status, "tfsaz: local PAC http://%s/proxy.pac; SOCKS5 and HTTP proxy requests are refused\n", address); err != nil {
		return err
	}
	slots := make(chan struct{}, 32)
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
			go func() {
				defer func() { <-slots }()
				defer closeQuietly(c)
				// Without a deadline the reads still end when the client or the listener closes.
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				handleRouteProxy(c, address, pac.Bytes())
			}()
		default:
			closeQuietly(c)
		}
	}
}

// handleRouteProxy answers one request. A failed write means the client is
// gone, and the caller closes the connection either way.
func handleRouteProxy(c net.Conn, address string, pac []byte) {
	// Limit parsing even when the local port receives a malformed request.
	r := bufio.NewReader(io.LimitReader(c, 8192))
	lead, err := r.Peek(1)
	if err != nil {
		return
	}
	if lead[0] == 5 {
		// RFC 1928: no acceptable authentication methods. The client stops
		// before sending a CONNECT target, application data, or credentials.
		_, _ = c.Write([]byte{5, 255})
		return
	}
	req, err := http.ReadRequest(r)
	if err != nil {
		return
	}
	if req.Method == http.MethodConnect || req.URL.IsAbs() {
		_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
		return
	}
	if req.Method != "GET" || req.RequestURI != "/proxy.pac" || req.Host != address {
		_, _ = io.WriteString(c, "HTTP/1.1 404 Not Found\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
		return
	}
	_, _ = fmt.Fprintf(c, "HTTP/1.1 200 OK\r\nContent-Type: application/x-ns-proxy-autoconfig\r\nCache-Control: no-store\r\nConnection: close\r\nContent-Length: %d\r\n\r\n", len(pac))
	_, _ = c.Write(pac)
}

func parseProxyOptions(args []string) (port int, shared bool, err error) {
	port = 1080
	gotPort := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--include-dev-azure" && !shared:
			shared = true
		case args[i] == "--port" && !gotPort && i+1 < len(args):
			gotPort = true
			i++
			port, err = strconv.Atoi(args[i])
			if err != nil || port < 1 || port > 65535 {
				return 0, false, usageError{errors.New("proxy port must be between 1 and 65535")}
			}
		default:
			return 0, false, usageError{errors.New("expected [--port PORT] [--include-dev-azure]")}
		}
	}
	return port, shared, nil
}
