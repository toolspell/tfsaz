package az

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
)

const macLinkHandlerBundleID = "com.toolspell.tfsaz.link-handler"

var macBrowserBundleID = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)

func exportMacLinkHandler(ctx context.Context, dest, browserBundleID string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("use route handler-export to export a native link handler on this platform")
	}
	if !macBrowserBundleID.MatchString(browserBundleID) || strings.EqualFold(browserBundleID, macLinkHandlerBundleID) {
		return errors.New("specify the bundle ID of a browser other than the tfsaz link handler")
	}
	if !validLinkPath(dest) || !strings.EqualFold(filepath.Ext(dest), ".app") || len(filepath.Base(dest)) <= len(".app") {
		return errors.New("specify a new .app bundle path")
	}
	dest, err := filepath.Abs(dest)
	if err != nil {
		return fmt.Errorf("resolve link handler destination: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate tfsaz executable: %w", err)
	}
	config, err := configPath()
	if err != nil {
		return err
	}
	if !absoluteLinkPath(executable) || !absoluteLinkPath(config) {
		return errors.New("the tfsaz executable and config must have valid absolute paths")
	}

	// Reserve the destination before building and publish the entire Contents
	// directory at once. Mkdir also refuses an existing symlink or empty bundle.
	if err := os.Mkdir(dest, 0755); err != nil {
		return fmt.Errorf("create link handler destination: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = os.Remove(dest)
		}
	}()

	stage, err := os.MkdirTemp(filepath.Dir(dest), ".tfsaz-handler-")
	if err != nil {
		return fmt.Errorf("stage link handler: %w", err)
	}
	defer os.RemoveAll(stage)
	bundle := filepath.Join(stage, filepath.Base(dest))
	source := macLinkHandlerSource(executable, config, "/usr/bin/open", browserBundleID)
	script := filepath.Join(stage, "open.applescript")
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		return fmt.Errorf("write link handler script: %w", err)
	}
	if err := macLinkHandlerTool(ctx, "/usr/bin/osacompile", "-o", bundle, script); err != nil {
		return err
	}
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	if err := macLinkHandlerTool(ctx, "/usr/bin/plutil", "-replace", "CFBundleIdentifier", "-string", macLinkHandlerBundleID, plist); err != nil {
		return err
	}
	urlTypes := `[{"CFBundleURLName":"Web site URL","CFBundleTypeRole":"Viewer","LSHandlerRank":"Alternate","CFBundleURLSchemes":["http","https"]}]`
	if err := macLinkHandlerTool(ctx, "/usr/bin/plutil", "-replace", "CFBundleURLTypes", "-json", urlTypes, plist); err != nil {
		return err
	}
	if err := macLinkHandlerTool(ctx, "/usr/bin/plutil", "-replace", "LSUIElement", "-bool", "true", plist); err != nil {
		return err
	}
	if err := macLinkHandlerTool(ctx, "/usr/bin/codesign", "--force", "--sign", "-", bundle); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(bundle, "Contents"), filepath.Join(dest, "Contents")); err != nil {
		return fmt.Errorf("install link handler bundle: %w", err)
	}
	finished = true
	return nil
}

func macLinkHandlerSource(executable, config, open, browserBundleID string) string {
	quoted := func(s string) string {
		// Escape AppleScript syntax here. The applet shell-quotes each value
		// independently after AppleScript has decoded the string.
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return fmt.Sprintf(`on open location requestedURL
	try
		my routeLink(requestedURL)
	on error
		display dialog "tfsaz could not open this link." buttons {"OK"} default button "OK" with title "tfsaz" giving up after 15
	end try
end open location

on routeLink(requestedURL)
	set lookupCommand to "TFSAZ_CONFIG=" & quoted form of %s & " " & quoted form of %s & " route browser-target " & quoted form of requestedURL
	set targetURL to do shell script lookupCommand
	if targetURL is "" then error "No browser target"
	do shell script (quoted form of %s & " -b " & quoted form of %s & " -- " & quoted form of targetURL)
end routeLink
`, quoted(config), quoted(executable), quoted(open), quoted(browserBundleID))
}

func macLinkHandlerTool(ctx context.Context, path string, args ...string) error {
	if output, err := exec.CommandContext(ctx, path, args...).CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, string(output)))
		if message != "" {
			return fmt.Errorf("could not prepare link handler with %s: %w: %.512s", filepath.Base(path), err, message)
		}
		return fmt.Errorf("could not prepare link handler with %s: %w", filepath.Base(path), err)
	}
	return nil
}
