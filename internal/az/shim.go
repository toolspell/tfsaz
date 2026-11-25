package az

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func sameBinary(path, executable string) bool {
	a, err := os.Stat(path)
	if err != nil || !a.Mode().IsRegular() {
		return false
	}
	b, err := os.Stat(executable)
	if err != nil || !b.Mode().IsRegular() {
		return false
	}
	if os.SameFile(a, b) {
		return true
	}
	if a.Size() != b.Size() {
		return false
	}
	sum := func(name string) ([]byte, error) {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		h := sha256.New()
		_, err = io.Copy(h, f)
		return h.Sum(nil), err
	}
	left, e1 := sum(path)
	right, e2 := sum(executable)
	return e1 == nil && e2 == nil && bytes.Equal(left, right)
}

func (a app) runShim(args []string) error {
	if len(args) != 3 || args[0] != "install" || args[1] != "--dir" || args[2] == "" {
		return usageError{errors.New("expected shim install --dir DIR")}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	// A package manager can replace a versioned executable on upgrade. Link
	// to its stable tfsaz launcher when it resolves to this same file.
	if stable, err := exec.LookPath("tfsaz"); err == nil && filepath.IsAbs(stable) {
		current, err1 := os.Stat(executable)
		installed, err2 := os.Stat(stable)
		if err1 == nil && err2 == nil && os.SameFile(current, installed) {
			executable = stable
		}
	}
	dir, err := filepath.Abs(args[2])
	if err != nil {
		return err
	}
	if err := installShimLinks(dir, executable); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "Installed CLI and Git helper links in %s. Include this directory on the calling process's PATH.\n", dir)
	return err
}

func installShimLinks(dir, executable string) error {
	var missing []string
	for _, name := range []string{"az", "git-remote-tfsaz"} {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(dir, name)
		_, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			missing = append(missing, path)
		case err != nil:
			return err
		case !sameBinary(path, executable):
			return fmt.Errorf("%s already exists and does not point to this tfsaz installation; choose a separate directory", path)
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for i, path := range missing {
		if err := os.Symlink(executable, path); err != nil {
			for _, created := range missing[:i] {
				if target, readErr := os.Readlink(created); readErr == nil && target == executable {
					err = errors.Join(err, os.Remove(created))
				}
			}
			return fmt.Errorf("create tfsaz link (on Windows enable symbolic links or install executable copies): %w", err)
		}
	}
	return nil
}
