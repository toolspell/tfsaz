package az

import (
	"errors"
	"os"

	"github.com/gofrs/flock"
	"golang.org/x/sys/windows"
)

// Windows refuses to delete a file that is open. A process that opened the
// file after the unlock holds it now and removes it on its own release.
func releaseSetupLock(lock *flock.Flock) error {
	if err := lock.Close(); err != nil {
		return err
	}
	err := os.Remove(lock.Path())
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return nil
	}
	return err
}
