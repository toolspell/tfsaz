// Command devops-server-mock serves the subset of an Azure DevOps Server
// 2022.2 that TFSaz and stock T3 Code use: REST, Git over HTTPS, HTTP and SSH, and
// plain web pages. State lives in memory and in bare repositories under
// MOCK_DATA; a restart starts from the seed again.
package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type config struct {
	httpAddr, httpsAddr, httpsAltAddr, adminAddr string
	sshAddr, sshPort, data, pat, keysDir         string
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg := config{
		httpAddr:     env("MOCK_HTTP_ADDR", ":8080"),
		httpsAddr:    env("MOCK_HTTPS_ADDR", ":443"),
		httpsAltAddr: env("MOCK_HTTPS_ALT_ADDR", ":8443"),
		adminAddr:    env("MOCK_ADMIN_ADDR", ":19090"),
		sshAddr:      env("MOCK_SSH_ADDR", ":2222"),
		sshPort:      env("MOCK_SSH_PORT", "2222"),
		data:         env("MOCK_DATA", "/data"),
		pat:          os.Getenv("MOCK_PAT"),
		keysDir:      env("MOCK_KEYS_DIR", "/keys"),
	}
	if cfg.pat == "" {
		log.Fatal("set MOCK_PAT to the PAT the mock accepts")
	}
	if err := os.RemoveAll(filepath.Join(cfg.data, "git")); err != nil {
		log.Fatal(err)
	}
	srv, err := newServer(cfg)
	if err != nil {
		log.Fatal(err)
	}
	tlsConfig, err := newMockTLS(cfg.keysDir)
	if err != nil {
		log.Fatal(err)
	}
	for _, addr := range []string{cfg.httpsAddr, cfg.httpsAltAddr} {
		https := &http.Server{Addr: addr, Handler: srv, TLSConfig: tlsConfig.Clone()}
		go func() { log.Fatal(https.ListenAndServeTLS("", "")) }()
	}
	go func() {
		log.Fatal(http.ListenAndServe(cfg.adminAddr, srv.journal.adminHandler()))
	}()
	go func() {
		log.Fatal(srv.serveSSH(cfg.sshAddr, cfg.keysDir))
	}()
	log.Printf("HTTP on %s, HTTPS on %s and %s, SSH on %s, admin on %s, collections: %s",
		cfg.httpAddr, cfg.httpsAddr, cfg.httpsAltAddr, cfg.sshAddr, cfg.adminAddr, strings.Join(srv.collectionNames(), ", "))
	log.Fatal(http.ListenAndServe(cfg.httpAddr, srv))
}
