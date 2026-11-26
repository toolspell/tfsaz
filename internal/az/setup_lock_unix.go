//go:build !windows

package az

import (
	"errors"
	"os"

	"github.com/gofrs/flock"
)

// Removing the file before the unlock leaves a waiter that opened it earlier
// with an unlinked file, which acquireSetupLock rejects.
func releaseSetupLock(lock *flock.Flock) error {
	err := os.Remove(lock.Path())
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return errors.Join(err, lock.Close())
}
