package az

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestExportMacLinkHandler(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS applet tools")
	}
	root := t.TempDir()
	t.Setenv("TFSAZ_CONFIG", filepath.Join(root, "config with spaces.json"))
	dest := filepath.Join(root, "Tfsaz Link Handler.app")
	const browser = "com.example.RealBrowser"
	if err := exportMacLinkHandler(t.Context(), dest, strings.ToUpper(macLinkHandlerBundleID)); err == nil {
		t.Fatal("handler accepted its own bundle identifier")
	}
	if err := exportMacLinkHandler(t.Context(), dest, "not a bundle identifier"); err == nil {
		t.Fatal("handler accepted an invalid browser identifier")
	}
	if err := exportMacLinkHandler(t.Context(), filepath.Join(root, "bundle"), browser); err == nil {
		t.Fatal("handler accepted a filename without an .app extension")
	}
	if err := exportMacLinkHandler(t.Context(), dest, browser); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(dest, "Contents", "Info.plist")
	cmd := exec.CommandContext(t.Context(), "/usr/bin/plutil", "-convert", "json", "-o", "-", plist)
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		CFBundleIdentifier string
		CFBundleURLTypes   []struct {
			CFBundleURLSchemes []string
		}
	}
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if info.CFBundleIdentifier != macLinkHandlerBundleID || len(info.CFBundleURLTypes) != 1 ||
		!reflect.DeepEqual(info.CFBundleURLTypes[0].CFBundleURLSchemes, []string{"http", "https"}) {
		t.Fatalf("unexpected bundle metadata: %s", data)
	}
	cmd = exec.CommandContext(t.Context(), "/usr/bin/codesign", "--verify", "--strict", dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("verify app signature: %v: %s", err, out)
	}
	if err := exportMacLinkHandler(t.Context(), dest, browser); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected an existing destination error, got %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	failed := filepath.Join(root, "Failed.app")
	if err := exportMacLinkHandler(cancelled, failed, browser); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected a cancelled export, got %v", err)
	}
	if _, err := os.Lstat(failed); !os.IsNotExist(err) {
		t.Fatalf("cancelled export left a destination: %v", err)
	}
}

func TestMacLinkHandlerAppleScriptHandoff(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("requires osascript")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, `café ' " $ & \ config.json`)
	lookup := filepath.Join(dir, `tfsaz ' " $ & \ executable`)
	browserOpen := filepath.Join(dir, `open ' browser`)
	script := filepath.Join(dir, "handler.applescript")
	trace := filepath.Join(dir, "trace")
	t.Setenv("TFSAZ_HANDLER_TRACE", trace)
	const browser = "com.example.RealBrowser"
	const requested = "https://example.invalid/start?q=%27&value='$(echo swallowed)'"
	const target = "https://server.invalid/a%20b#anchor"

	writeScript := func(path, contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeScript(lookup, `#!/bin/sh
printf '%s\n' "$TFSAZ_CONFIG" "$@" > "$TFSAZ_HANDLER_TRACE"
if [ "$TFSAZ_HANDLER_FAIL" = yes ]; then exit 1; fi
printf '%s\n' "$TFSAZ_HANDLER_TARGET"
`)
	writeScript(browserOpen, `#!/bin/sh
printf '%s\n' "$@" >> "$TFSAZ_HANDLER_TRACE"
`)
	writeScript(script, macLinkHandlerSource(lookup, config, browserOpen, browser)+`
on run argv
	my routeLink(item 1 of argv)
end run
`)
	t.Setenv("TFSAZ_HANDLER_TARGET", target)
	for _, fail := range []bool{false, true} {
		t.Setenv("TFSAZ_HANDLER_FAIL", map[bool]string{false: "no", true: "yes"}[fail])
		cmd := exec.CommandContext(t.Context(), "/usr/bin/osascript", script, requested)
		out, err := cmd.CombinedOutput()
		if (err != nil) != fail {
			t.Fatalf("fail=%v, osascript returned %v: %s", fail, err, out)
		}
		want := []string{config, "route", "browser-target", requested}
		if !fail {
			want = append(want, "-b", browser, "--", target)
		}
		observed, err := os.ReadFile(trace)
		if err != nil {
			t.Fatal(err)
		}
		if string(observed) != strings.Join(want, "\n")+"\n" {
			t.Fatalf("fail=%v, handoff=%q, want %q", fail, observed, want)
		}
	}
}
