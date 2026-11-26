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
		defer closeQuietly(f)
		h := sha256.New()
		_, err = io.Copy(h, f)
		return h.Sum(nil), err
	}
	left, e1 := sum(path)
	right, e2 := sum(executable)
	return e1 == nil && e2 == nil && bytes.Equal(left, right)
}

var shimNames = []string{"az", "git-remote-tfsaz"}

func shimPath(dir, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

func (a app) runShim(args []string) error {
	if len(args) != 3 || args[0] != "install" && args[0] != "uninstall" || args[1] != "--dir" || args[2] == "" {
		return usageError{errors.New("expected shim install --dir DIR or shim uninstall --dir DIR")}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if args[0] == "uninstall" {
		dir, err := filepath.Abs(args[2])
		if err != nil {
			return err
		}
		return uninstallShimLinks(a.stdout, dir, executable)
	}
	// A package manager can replace a versioned executable on upgrade. Link
	// to the stable "tfsaz" on PATH when it resolves to this same file.
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
	_, err = fmt.Fprintf(a.stdout, "Installed the az and git-remote-tfsaz shims in %s. Include this directory on the calling process's PATH.\n", dir)
	return err
}

func installShimLinks(dir, executable string) (err error) {
	var missing []string
	for _, name := range shimNames {
		path := shimPath(dir, name)
		_, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			missing = append(missing, path)
		case err != nil:
			return err
		case !sameBinary(path, executable):
			return fmt.Errorf("%s already exists and does not point to this TFSaz installation; choose a separate directory", path)
		}
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	created := make(map[string]os.FileInfo, len(missing))
	defer func() {
		if err == nil {
			return
		}
		for name, info := range created {
			if current, statErr := os.Lstat(name); statErr == nil && os.SameFile(current, info) && sameBinary(name, executable) {
				err = errors.Join(err, os.Remove(name))
			}
		}
	}()
	for _, path := range missing {
		if err := installShim(executable, path); err != nil {
			return fmt.Errorf("install shim: %w", err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		created[path] = info
	}
	return nil
}

// Uninstall removes only what install creates: a link to this TFSaz, or on
// Windows a copy of it. Any other file may be another tool's az.
func uninstallShimLinks(w io.Writer, dir, executable string) error {
	// Check every shim before removing any: the executable or a shim can be
	// a link through the other shim, such as the az link on macOS.
	var kept []error
	var remove []string
	for _, name := range shimNames {
		path := shimPath(dir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			kept = append(kept, err)
			continue
		}
		if reason := keepShimReason(path, info, executable); reason != "" {
			kept = append(kept, fmt.Errorf("left %s in place: %s", path, reason))
			continue
		}
		remove = append(remove, path)
	}
	removed := false
	for _, path := range remove {
		if err := os.Remove(path); err != nil {
			kept = append(kept, err)
			continue
		}
		removed = true
		if _, err := fmt.Fprintf(w, "Removed %s\n", path); err != nil {
			return err
		}
	}
	if !removed && len(kept) == 0 {
		if _, err := fmt.Fprintf(w, "Found no az or git-remote-tfsaz shim in %s.\n", dir); err != nil {
			return err
		}
	}
	return errors.Join(kept...)
}

func keepShimReason(path string, info os.FileInfo, executable string) string {
	if info.Mode()&os.ModeSymlink != 0 {
		if sameBinary(path, executable) {
			return ""
		}
		return "it does not link to this TFSaz executable"
	}
	if current, err := os.Stat(executable); err == nil && os.SameFile(info, current) {
		return "it is the running TFSaz executable itself"
	}
	if runtime.GOOS != "windows" || !info.Mode().IsRegular() {
		return "it is not a link to this TFSaz executable"
	}
	if sameBinary(path, executable) {
		return ""
	}
	return "it differs from this TFSaz executable; if it is a copy from an older TFSaz, delete it yourself"
}
