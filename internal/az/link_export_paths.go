//go:build linux || windows

package az

import (
	"errors"
	"os"
)

// The Linux and Windows handler exports name a browser executable; macOS
// selects the browser by bundle ID instead.
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
