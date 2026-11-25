package az

import (
	"bytes"
	"io"
	"net"
	"slices"
	"testing"
	"time"
)

func TestCatchHostsScope(t *testing.T) {
	cfg := config{Profiles: []Profile{{ID: "one", Organization: "https://example.visualstudio.com", CollectionURL: "https://server.invalid/tfs/First"}}}
	for _, shared := range []bool{false, true} {
		names, err := routeHostnames(cfg, nil, shared)
		if err != nil || slices.Contains(names, "dev.azure.com") != shared || !slices.Contains(names, "example.visualstudio.com") {
			t.Fatalf("hosts %v, %v", names, err)
		}
	}
	cfg.Profiles[0].CollectionURL = "https://example.visualstudio.com/First"
	if _, err := routeHostnames(cfg, nil, false); err == nil {
		t.Fatal("would catch the real collection")
	}
}

func TestDenyProxy(t *testing.T) {
	const addr = "127.0.0.1:1080"
	pac := []byte("function FindProxyForURL() { return 'DIRECT'; }")
	for _, tc := range []struct{ input, want string }{
		{"\x05\x02\x00\x02", "\x05\xff"},
		{"GET /proxy.pac HTTP/1.1\r\nHost: 127.0.0.1:1080\r\n\r\n", string(pac)},
		{"GET /proxy.pac HTTP/1.1\r\nHost: other.test\r\n\r\n", "404 Not Found"},
		{"POST /proxy.pac HTTP/1.1\r\nHost: 127.0.0.1:1080\r\n\r\n", "404 Not Found"},
		{"CONNECT outside.test:443 HTTP/1.1\r\nHost: outside.test:443\r\n\r\n", "403 Forbidden"},
		{"GET http://outside.test/ HTTP/1.1\r\nHost: outside.test\r\n\r\n", "403 Forbidden"},
	} {
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() { defer close(done); defer server.Close(); handleRouteProxy(server, addr, pac) }()
		client.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.WriteString(client, tc.input); err != nil {
			t.Fatal(err)
		}
		out, err := io.ReadAll(client)
		client.Close()
		<-done
		if err != nil || !bytes.Contains(out, []byte(tc.want)) {
			t.Fatalf("reply %q, err %v", out, err)
		}
	}
}
