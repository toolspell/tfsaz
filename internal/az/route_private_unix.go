//go:build !windows

package az

import "os"

func protectRouteDirectory(dir string) error {
	return os.Chmod(dir, 0700)
}
