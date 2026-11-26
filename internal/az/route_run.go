package az

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gofrs/flock"
)

// Each route run overlay git-X has a lock file git-X.lock, held while the run
// lives, and git-X.pid with the launched process. The lock file comes first and
// goes last, so an overlay without one is a leftover of a build before locks.
type routeRunOverlay struct {
	dir     string
	release func() error
}

func (o routeRunOverlay) pidFile() string { return o.dir + ".pid" }

// A killed run leaves its launched process running, which still uses the
// overlay through GIT_EXEC_PATH. Its overlay stays until that process exits.
func (o routeRunOverlay) recordChild(pid int) error {
	return os.WriteFile(o.pidFile(), []byte(strconv.Itoa(pid)+"\n"), 0600)
}

func (o routeRunOverlay) remove() error {
	return errors.Join(os.RemoveAll(o.dir), removeIfPresent(o.pidFile()), o.release())
}

// A run that ends concurrently may remove the empty parent between the
// MkdirAll and the CreateTemp, and a concurrent cleanup may take or remove a
// fresh lock file before its creator locks it, so retry a few times.
func makeRouteRunOverlay(parent string) (routeRunOverlay, error) {
	var last error
	for range 3 {
		if err := os.MkdirAll(parent, 0700); err != nil {
			return routeRunOverlay{}, err
		}
		file, err := os.CreateTemp(parent, "git-*.lock")
		if errors.Is(err, os.ErrNotExist) {
			last = err
			continue
		}
		if err != nil {
			return routeRunOverlay{}, err
		}
		path := file.Name()
		if err := file.Close(); err != nil {
			return routeRunOverlay{}, errors.Join(err, removeIfPresent(path))
		}
		lock, err := tryRouteRunLock(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return routeRunOverlay{}, err
		}
		if lock == nil {
			last = fmt.Errorf("another process holds %s", path)
			continue
		}
		overlay := routeRunOverlay{dir: strings.TrimSuffix(path, ".lock"), release: func() error { return releaseSetupLock(lock) }}
		if err := os.Mkdir(overlay.dir, 0700); err != nil {
			return routeRunOverlay{}, errors.Join(err, overlay.release())
		}
		return overlay, nil
	}
	return routeRunOverlay{}, fmt.Errorf("create a route run overlay in %s: %w", parent, last)
}

// Only CreateTemp creates a lock file. Recreating one that a cleanup removed
// would let a cleanup that saw it missing delete the overlay its new holder
// creates next.
func tryRouteRunLock(path string) (*flock.Flock, error) {
	return tryLockFile(path, flock.SetFlag(os.O_RDONLY))
}

// removeStaleRouteRunOverlays removes the overlays of runs that ended without
// cleaning up, such as after a kill. It keeps the overlays of running runs and
// of launched processes that outlived their run.
func removeStaleRouteRunOverlays(parent string) error {
	entries, err := os.ReadDir(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var errs []error
	for _, entry := range entries {
		base := strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".lock"), ".pid")
		if !strings.HasPrefix(base, "git-") || seen[base] {
			continue
		}
		seen[base] = true
		if err := removeStaleRouteRunOverlay(filepath.Join(parent, base)); err != nil {
			errs = append(errs, fmt.Errorf("remove stale route run overlay %s: %w", filepath.Join(parent, base), err))
		}
	}
	return errors.Join(errs...)
}

func removeStaleRouteRunOverlay(dir string) error {
	lockPath := dir + ".lock"
	if _, err := os.Lstat(lockPath); errors.Is(err, os.ErrNotExist) {
		return errors.Join(os.RemoveAll(dir), removeIfPresent(dir+".pid"))
	} else if err != nil {
		return err
	}
	lock, err := tryRouteRunLock(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || lock == nil {
		return err
	}
	overlay := routeRunOverlay{dir: dir, release: func() error { return releaseSetupLock(lock) }}
	if data, err := os.ReadFile(overlay.pidFile()); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && routeRunProcessAlive(pid) {
			return lock.Close()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(err, lock.Close())
	}
	return overlay.remove()
}
