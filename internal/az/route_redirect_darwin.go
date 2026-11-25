//go:build darwin

package az

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
)

// routeInheritedSocketFlag is appended by the managed LaunchAgents only.
const routeInheritedSocketFlag = "--launchd-socket"

// serveRouteRedirectInherited serves the listening socket that launchd passes
// on standard input in inetdCompatibility Wait mode. launchd creates and binds
// the socket; this process runs as the user and never binds port 443 itself.
func serveRouteRedirectInherited(ctx context.Context, certFile, keyFile string, port int, status io.Writer) error {
	return serveRouteRedirectOn(ctx, certFile, keyFile, port, inheritedStatus(status), func(port int) ([]net.Listener, error) {
		ln, err := net.FileListener(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("launchd did not pass a listening socket on standard input: %w", err)
		}
		if err := checkInheritedListener(ln.Addr(), port); err != nil {
			ln.Close()
			return nil, err
		}
		return []net.Listener{ln}, nil
	})
}

// checkInheritedListener refuses anything but a TCP socket on the expected
// loopback port, so a changed plist cannot expose the redirector elsewhere.
func checkInheritedListener(addr net.Addr, port int) error {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() || tcp.Port != port {
		return fmt.Errorf("the inherited socket %v is not on loopback port %d", addr, port)
	}
	if ip4 := tcp.IP.To4(); ip4 != nil && !ip4.Equal(net.IPv4(127, 0, 0, 1)) || ip4 == nil && !tcp.IP.Equal(net.IPv6loopback) {
		return errors.New("the inherited socket must be bound to 127.0.0.1 or ::1")
	}
	return nil
}

// In inetd mode launchd may also place the socket on stdout and stderr.
// Status goes only to a log file that launchd opened for stderr.
func inheritedStatus(status io.Writer) io.Writer {
	f, ok := status.(*os.File)
	if !ok {
		return status
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return io.Discard
	}
	return status
}
