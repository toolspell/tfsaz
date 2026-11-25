package az

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLinkHandlerRejectsRecursiveOrMissingBrowser(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := linkHandlerPaths(executable); err == nil {
		t.Fatal("accepted tfsaz as its own browser")
	}
	dir := t.TempDir()
	for _, browser := range []string{"relative", filepath.Join(dir, "missing.exe")} {
		if _, _, err := linkHandlerPaths(browser); err == nil {
			t.Fatalf("accepted browser %q", browser)
		}
	}
	opener := "xdg-open"
	if runtime.GOOS == "windows" {
		opener = "explorer.exe"
	}
	opener = filepath.Join(dir, opener)
	if err := os.WriteFile(opener, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := linkHandlerPaths(opener); err == nil {
		t.Fatal("accepted browser dispatcher")
	}
}

func TestOpenDesktopLinkHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dir := t.TempDir()
	browser := filepath.Join(dir, "browser.exe")
	if err := os.WriteFile(browser, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	err := openDesktopLink(ctx, filepath.Join(dir, "config.json"), browser, "https://example.org/")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want canceled", err)
	}
}
