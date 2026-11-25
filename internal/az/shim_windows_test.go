package az

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsShimCopyWithoutLinkPrivilege(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "tfsaz.exe")
	path := filepath.Join(dir, "az.exe")
	if err := os.WriteFile(source, []byte("launcher"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyWindowsShim(source, path); err != nil || !sameBinary(source, path) {
		t.Fatalf("install copy: %v", err)
	}
	if err := os.WriteFile(path, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyWindowsShim(source, path); err == nil {
		t.Fatal("overwrote existing shim")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "foreign" {
		t.Fatalf("foreign launcher: %q, %v", data, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 2 {
		t.Fatalf("unexpected export contents: %v, %v", entries, err)
	}
}
