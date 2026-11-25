package az

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyRouteNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable on PATH")
	}
	t.Setenv("NODE_OPTIONS", "--dns-result-order=ipv4first")
	t.Setenv("NODE_TLS_REJECT_UNAUTHORIZED", "")
	t.Setenv("NODE_EXTRA_CA_CERTS", "")
	t.Setenv("NODE_USE_SYSTEM_CA", "")

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the Node TLS check sent an HTTP request")
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	serverNames := make(chan string, 4)
	server.TLS = &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			serverNames <- hello.ServerName
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, cert, 0600); err != nil {
		t.Fatal(err)
	}
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config{Profiles: []Profile{{
		ID:            "fixture",
		CollectionURL: server.URL + "/Private%20Collection",
		CAFile:        caFile,
	}}}
	var out bytes.Buffer
	if err := verifyRouteNode(t.Context(), cfg, &out); err == nil || !strings.Contains(err.Error(), "Node rejected the collection certificate") || out.Len() != 0 {
		t.Fatalf("without Node trust got output %q, error %v", &out, err)
	}

	t.Setenv("NODE_EXTRA_CA_CERTS", caFile)
	if err := verifyRouteNode(t.Context(), cfg, &out); err != nil || out.String() != "profile fixture: Node verified collection TLS\n" {
		t.Fatalf("with Node trust got output %q, error %v", &out, err)
	}

	out.Reset()
	mismatch := config{Profiles: append([]Profile{{
		ID:            "mismatch",
		CollectionURL: "https://" + net.JoinHostPort("localhost", base.Port()) + "/Private%20Collection",
	}}, cfg.Profiles...)}
	if err := verifyRouteNode(t.Context(), mismatch, &out); err == nil || !strings.Contains(err.Error(), "profile mismatch: Node rejected the collection certificate") || strings.Contains(err.Error(), "localhost") || out.String() != "profile fixture: Node verified collection TLS\n" {
		t.Fatalf("with hostname mismatch got output %q, error %v", &out, err)
	}

	for _, expected := range []string{"", "", "localhost", ""} {
		select {
		case got := <-serverNames:
			if got != expected {
				t.Fatalf("got SNI %q, want %q", got, expected)
			}
		default:
			t.Fatal("expected an incoming TLS handshake")
		}
	}
	out.Reset()
	t.Setenv("NODE_TLS_REJECT_UNAUTHORIZED", "0")
	if err := verifyRouteNode(t.Context(), cfg, &out); err == nil || !strings.Contains(err.Error(), "unset NODE_TLS_REJECT_UNAUTHORIZED") || out.Len() != 0 {
		t.Fatalf("with disabled verification got output %q, error %v", &out, err)
	}
}
