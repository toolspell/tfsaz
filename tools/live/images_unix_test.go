//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStageIgnoresUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })
	dir := filepath.Join(t.TempDir(), "context")
	if err := stage(dir, []contextFile{{"dnsmasq.conf", []byte("x\n")}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "dnsmasq.conf"))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v, err %v; want 0644 under umask 077", info.Mode(), err)
	}
}
