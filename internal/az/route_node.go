package az

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// Use the Node executable's normal trust configuration. A bare TLS socket
// avoids sending HTTP requests, using an HTTP proxy, or following redirects.
const routeNodeProbe = `
const net = require('net');
const tls = require('tls');
const { domainToASCII } = require('url');
const [host, port, timeout] = process.argv.slice(1);
const servername = net.isIP(host) ? undefined : domainToASCII(host);
const certificateErrors =
  /^(CERT_|CRL_|INVALID_CA$|INVALID_PURPOSE$|PATH_LENGTH_EXCEEDED$|DEPTH_ZERO_SELF_SIGNED_CERT$|SELF_SIGNED_CERT_IN_CHAIN$|UNABLE_TO_(GET_ISSUER_CERT|GET_CRL|DECRYPT_CERT_SIGNATURE|DECODE_ISSUER_PUBLIC_KEY|VERIFY_LEAF_SIGNATURE)|ERR_TLS_CERT_)/;

process.exitCode = 11;
const socket = tls.connect({
  host,
  port: Number(port),
  servername,
  rejectUnauthorized: true,
  checkServerIdentity: (_, certificate) => tls.checkServerIdentity(servername || host, certificate),
});
// Exit instead of destroying the socket: a pending DNS lookup would keep Node running.
setTimeout(() => process.exit(13), Number(timeout)).unref();
socket.once('secureConnect', () => {
  process.exitCode = socket.authorized ? 0 : 10;
  if (socket.authorized) process.stdout.write('verified\n');
  socket.destroy();
});
socket.once('error', (error) => {
  process.exitCode = certificateErrors.test(error.code) ? 10 : /^ERR_SSL_/.test(error.code) ? 12 : 11;
});
`

func verifyRouteNode(ctx context.Context, cfg config, out io.Writer) error {
	if os.Getenv("NODE_TLS_REJECT_UNAUTHORIZED") == "0" {
		return errors.New("unset NODE_TLS_REJECT_UNAUTHORIZED before verifying Node TLS")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		return errors.New("could not find node on PATH; use the Node runtime of the client that makes direct HTTPS requests")
	}

	var failures []error
	for _, p := range cfg.Profiles {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		target, ok := baseURL(p.CollectionURL)
		if !ok {
			failures = append(failures, fmt.Errorf("profile %s: invalid collection URL", p.ID))
			continue
		}
		port := target.Port()
		if port == "" {
			port = "443"
		}
		if err := checkNodeTLS(ctx, node, target.Hostname(), port); err != nil {
			failures = append(failures, fmt.Errorf("profile %s: %w", p.ID, err))
			continue
		}
		if _, err := fmt.Fprintf(out, "profile %s: Node verified collection TLS\n", p.ID); err != nil {
			return errors.Join(append(failures, err)...)
		}
	}
	return errors.Join(failures...)
}

// The TLS budget starts once Node runs; a cold Node start on Windows can take
// longer than the handshake itself.
var nodeTLSTimeout = 10 * time.Second

const nodeStartupTimeout = 20 * time.Second

func checkNodeTLS(ctx context.Context, node, host, port string) error {
	probeCtx, cancel := context.WithTimeout(ctx, nodeStartupTimeout+nodeTLSTimeout)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, node, "-e", routeNodeProbe, "--", host, port, strconv.FormatInt(nodeTLSTimeout.Milliseconds(), 10)).Output()
	if err == nil {
		if string(output) != "verified\n" {
			return errors.New("Node exited without confirming the TLS handshake; check the runtime and its startup options")
		}
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if probeCtx.Err() != nil {
		return fmt.Errorf("Node did not finish the TLS check within %s; check the runtime and its startup options", nodeStartupTimeout+nodeTLSTimeout)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 10:
			return errors.New("Node rejected the collection certificate; check its hostname and chain, and configure Node trust with NODE_USE_SYSTEM_CA or NODE_EXTRA_CA_CERTS")
		case 11:
			return errors.New("Node could not connect to the collection; check DNS and network reachability")
		case 12:
			return errors.New("Node could not complete the TLS handshake; check the collection's HTTPS port and TLS settings")
		case 13:
			return errors.New("Node TLS connection timed out; check network reachability to the collection")
		}
	}
	return errors.New("could not run the Node TLS check; check node on PATH and its startup options")
}
