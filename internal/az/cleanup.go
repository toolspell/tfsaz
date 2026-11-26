package az

import (
	"errors"
	"io"
	"os"
)

// closeQuietly closes something whose close error tells the caller nothing:
// a response body or file that was only read, a file the success path already
// closed and checked, a connection or listener being shut down, or anything an
// error path discards.
func closeQuietly(c io.Closer) { _ = c.Close() }

// removeIfPresent removes a file or an empty directory that may already be gone.
func removeIfPresent(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// removeStaged removes a staged temporary file or empty directory and adds a
// failure to *err. After a hard-link publish the staged name still names the
// output, so a failed removal would leave a stray copy behind unreported.
func removeStaged(path string, err *error) {
	if rmErr := removeIfPresent(path); rmErr != nil {
		*err = errors.Join(*err, rmErr)
	}
}

// removeStagedTree is removeStaged for a staging directory with contents.
func removeStagedTree(path string, err *error) {
	if rmErr := os.RemoveAll(path); rmErr != nil {
		*err = errors.Join(*err, rmErr)
	}
}
