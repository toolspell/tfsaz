package az

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func installShim(executable, path string) error {
	err := os.Symlink(executable, path)
	if !errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) &&
		!errors.Is(err, windows.ERROR_NOT_SUPPORTED) &&
		!errors.Is(err, windows.ERROR_INVALID_FUNCTION) &&
		!errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return err
	}
	return copyWindowsShim(executable, path)
}

// Publish a complete executable without replacing a file created concurrently.
func copyWindowsShim(executable, path string) (err error) {
	source, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer closeQuietly(source)
	stage, err := os.CreateTemp(filepath.Dir(path), ".tfsaz-launcher-*")
	if err != nil {
		return err
	}
	defer removeStaged(stage.Name(), &err)
	_, err = io.Copy(stage, source)
	if err := errors.Join(err, stage.Close()); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(stage.Name())
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFile(from, to)
}
