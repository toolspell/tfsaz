package az

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestWindowsHandlerCommand(t *testing.T) {
	const executable = `C:\Project files\tfsaz.exe`
	const config = `C:\Max's files\café & ‘links’.json`
	const browser = `D:\Browser files\browser.exe`
	const want = `"C:\Project files\tfsaz.exe" route handler-open "C:\Max's files\café & ‘links’.json" "D:\Browser files\browser.exe" "%1"`
	command, err := windowsHandlerCommand(executable, config, browser)
	if err != nil || command != want {
		t.Fatalf("command = %q, %v, want %q", command, err, want)
	}
	script := windowsHandlerScript("TfsazLinks.fixture", command, false)
	if strings.Contains(script, config) || !strings.Contains(script, base64.StdEncoding.EncodeToString([]byte(want))) {
		t.Fatal("PowerShell script must read the command as data")
	}
	for _, path := range []string{`C:config.json`, `C:\path\%1\config.json`, `C:\path\%USERNAME%\config.json`, `C:\path\"config.json`, `C:\folder\`} {
		if _, err := windowsHandlerCommand(executable, path, browser); err == nil {
			t.Errorf("accepted unsafe association file path %q", path)
		}
	}
}

func TestExportWindowsLinkHandler(t *testing.T) {
	dir := t.TempDir()
	browser := filepath.Join(dir, "chosen browser.exe")
	if err := os.WriteFile(browser, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TFSAZ_CONFIG", filepath.Join(dir, "config with spaces.json"))
	dest := filepath.Join(dir, "new handler")
	if err := exportDesktopLinkHandler(t.Context(), dest, browser); err != nil {
		t.Fatal(err)
	}
	if err := exportDesktopLinkHandler(t.Context(), dest, browser); !errors.Is(err, os.ErrExist) {
		t.Fatalf("repeated export = %v, want existing directory error", err)
	}
	install, err := os.ReadFile(filepath.Join(dest, "Install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	uninstall, err := os.ReadFile(filepath.Join(dest, "Uninstall.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`\$id = 'TfsazLinks\.[a-f0-9]{24}'`).Match(install) ||
		string(uninstall) != strings.Replace(string(install), "$action = 'Install'", "$action = 'Uninstall'", 1) {
		t.Fatal("installer and uninstaller do not agree on the exported registration")
	}
}
