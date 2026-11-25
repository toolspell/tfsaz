//go:build !windows

package az

import "os"

func installShim(executable, path string) error {
	return os.Symlink(executable, path)
}
