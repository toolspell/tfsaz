package az

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validLinkPath(path string) bool {
	return path != "" && utf8.ValidString(path) && !strings.ContainsFunc(path, unicode.IsControl)
}

func absoluteLinkPath(path string) bool {
	return filepath.IsAbs(path) && validLinkPath(path)
}

func validateLinkBrowser(browser, executable string) error {
	if !absoluteLinkPath(browser) {
		return errors.New("specify the absolute path to the browser executable")
	}
	resolved, err := filepath.EvalSymlinks(browser)
	if err != nil {
		return errors.New("could not locate the selected browser executable")
	}
	for _, path := range []string{browser, resolved} {
		switch strings.ToLower(filepath.Base(path)) {
		case "xdg-open", "gio", "sensible-browser", "open", "explorer.exe", "rundll32.exe":
			return errors.New("choose a browser executable that opens links directly")
		}
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(browser), ".exe") {
		return errors.New("select a browser .exe")
	}
	info, err := os.Stat(browser)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
		return errors.New("selected browser must be an executable file")
	}
	if sameBinary(browser, executable) {
		return errors.New("choose a browser other than tfsaz")
	}
	return nil
}

func linkHandlerPaths(browser string) (executable, config string, err error) {
	executable, err = os.Executable()
	if err != nil || !absoluteLinkPath(executable) {
		return "", "", errors.New("could not locate the tfsaz executable")
	}
	if err := validateLinkBrowser(browser, executable); err != nil {
		return "", "", err
	}
	config, err = configPath()
	if err != nil || !absoluteLinkPath(config) {
		return "", "", errors.New("link handler configuration requires an absolute path")
	}
	return executable, config, nil
}

func openDesktopLink(ctx context.Context, configFile, browser, raw string) error {
	if !absoluteLinkPath(configFile) {
		return errors.New("link handler configuration requires an absolute path")
	}
	target, err := browserTargetAt(raw, configFile)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := validateLinkBrowser(browser, executable); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// The browser can remain open after this one-shot handler exits.
	cmd := exec.Command(browser, target)
	if err := cmd.Start(); err != nil {
		return errors.New("could not start the selected browser")
	}
	return cmd.Process.Release()
}
