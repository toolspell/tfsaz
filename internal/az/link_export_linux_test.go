package az

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportDesktopLinkHandler(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "route config.json")
	t.Setenv("TFSAZ_CONFIG", config)
	browser := filepath.Join(root, "browser launcher")
	if err := os.Symlink("/bin/true", browser); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "com.toolspell.tfsaz.link-handler.desktop")
	ctx := context.Background()
	if err := exportDesktopLinkHandler(ctx, dest, browser); err != nil {
		t.Fatal(err)
	}
	entry, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		` route handler-open "` + config + `" "` + browser + `" %u`,
		"MimeType=x-scheme-handler/http;x-scheme-handler/https;",
	} {
		if !strings.Contains(string(entry), want) {
			t.Errorf("desktop entry is missing %q:\n%s", want, entry)
		}
	}
	if err := exportDesktopLinkHandler(ctx, dest, browser); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwriting existing desktop file: %v", err)
	}
	after, err := os.ReadFile(dest)
	if err != nil || string(after) != string(entry) {
		t.Fatalf("existing desktop file changed: %q, %v", after, err)
	}
	link := filepath.Join(root, "dangling.desktop")
	if err := os.Symlink(filepath.Join(root, "missing.desktop"), link); err != nil {
		t.Fatal(err)
	}
	if err := exportDesktopLinkHandler(ctx, link, browser); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwriting desktop symlink: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := exportDesktopLinkHandler(canceled, filepath.Join(root, "canceled.desktop"), browser); !errors.Is(err, context.Canceled) {
		t.Fatalf("export with canceled context: %v", err)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 3 {
		t.Fatalf("unexpected files after export: %v, %v", files, err)
	}
}

func TestDesktopLinkHandlerGIOArguments(t *testing.T) {
	gio, err := exec.LookPath("gio")
	if err != nil {
		t.Skip("gio is unavailable")
	}
	root := t.TempDir()
	receiver := filepath.Join(root, "capture \"'\\$` café")
	config := filepath.Join(root, "conf \"'\\$` %u.ini")
	browser := filepath.Join(root, "browser \"'\\$` %U")
	trace := filepath.Join(root, "argv")
	t.Setenv("TFSAZ_GIO_TRACE", trace)
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TFSAZ_GIO_TRACE\"\n"
	if err := os.WriteFile(receiver, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "test.desktop")
	if err := os.WriteFile(dest, []byte(desktopLinkHandlerEntry(receiver, config, browser)), 0600); err != nil {
		t.Fatal(err)
	}
	url := "https://example.invalid/a%20b?x=one&y=%27two%27#section"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, gio, "launch", dest, url).CombinedOutput(); err != nil {
		t.Fatalf("gio launch: %v\n%s", err, output)
	}
	want := strings.Join([]string{"route", "handler-open", config, browser, url, ""}, "\n")
	// gio launch returns after starting the receiver detached, so there is no
	// process exit to wait on; poll the trace file instead.
	for ctx.Err() == nil {
		got, err := os.ReadFile(trace)
		if err == nil && strings.Count(string(got), "\n") == 5 {
			if string(got) != want {
				t.Fatalf("argv = %q, want %q", got, want)
			}
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("desktop entry did not pass all five arguments to the receiver")
}
