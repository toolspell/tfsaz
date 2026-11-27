// Command devops-server-mock serves the subset of an Azure DevOps Server
// 2022.2 that TFSaz and stock T3 Code use: REST, Git over HTTP and SSH, and
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
	httpAddr, sshAddr, sshPort string
	data                       string
	pat                        string
	keysDir                    string
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg := config{
		httpAddr: env("MOCK_HTTP_ADDR", ":8080"),
		sshAddr:  env("MOCK_SSH_ADDR", ":2222"),
		sshPort:  env("MOCK_SSH_PORT", "2222"),
		data:     env("MOCK_DATA", "/data"),
		pat:      os.Getenv("MOCK_PAT"),
		keysDir:  env("MOCK_KEYS_DIR", "/keys"),
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
	go func() {
		log.Fatal(srv.serveSSH(cfg.sshAddr, cfg.keysDir))
	}()
	log.Printf("HTTP on %s, SSH on %s, collections: %s", cfg.httpAddr, cfg.sshAddr, strings.Join(srv.collectionNames(), ", "))
	log.Fatal(http.ListenAndServe(cfg.httpAddr, srv))
}
