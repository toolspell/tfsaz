package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

var instancePattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

func instanceID(root, override string) (string, error) {
	if override == "" {
		sum := sha256.Sum256([]byte(filepath.Clean(root)))
		return hex.EncodeToString(sum[:6]), nil
	}
	if !instancePattern.MatchString(override) {
		return "", errors.New("TFSAZ_LIVE_INSTANCE must be 1 to 16 lowercase letters or digits")
	}
	return override, nil
}

func (e *env) project() string { return "tfsaz-live-" + e.instance }

func (e *env) cloneName(o osDef, suffix string) string {
	return e.project() + "-" + o.name + "-" + suffix
}

func parseCloneName(name string) (string, osDef, bool) {
	rest, ok := strings.CutPrefix(name, "tfsaz-live-")
	if !ok {
		return "", osDef{}, false
	}
	instance, rest, _ := strings.Cut(rest, "-")
	osName, suffix, _ := strings.Cut(rest, "-")
	o, ok := guestOS(osName)
	return instance, o, ok && instancePattern.MatchString(instance) && validSuffix(suffix)
}

func acquireLock(path string, shared bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return takeLock(flock.New(path), shared)
}

func (e *env) lockInstance(readOnly bool) (func(), error) {
	path := e.instancePath("instance.lock")
	if !readOnly {
		return acquireLock(path, false)
	}
	unlock, err := takeLock(flock.New(path, flock.SetFlag(os.O_RDONLY)), true)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return unlock, err
}

func takeLock(lock *flock.Flock, shared bool) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	take := lock.TryLockContext
	if shared {
		take = lock.TryRLockContext
	}
	ok, err := take(ctx, 100*time.Millisecond)
	if !ok {
		_ = lock.Close()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("lock %s: %w", lock.Path(), err)
		}
		return nil, fmt.Errorf("%s is busy; retry after the other live command finishes", lock.Path())
	}
	return func() { _ = lock.Close() }, nil
}

type reservation struct {
	Root string `json:"root"`
	Slot int    `json:"slot"`
}

type slotRegistry map[string]reservation

func (e *env) withSlots(use func(slotRegistry) error) error {
	unlock, err := acquireLock(e.path("slots.lock"), false)
	if err != nil {
		return err
	}
	defer unlock()
	slots, err := e.ownedSlots()
	if err != nil {
		return err
	}
	return use(slots)
}

func (e *env) ownedSlots() (slotRegistry, error) {
	slots, err := e.readSlots()
	if err != nil {
		return nil, err
	}
	if held, ok := slots[e.instance]; ok && held.Root != e.root {
		return nil, fmt.Errorf("instance %q belongs to %s; choose a different TFSAZ_LIVE_INSTANCE or run down in that worktree", e.instance, held.Root)
	}
	return slots, nil
}

func (e *env) readSlots() (slotRegistry, error) {
	path := e.path("slots.json")
	slots := make(slotRegistry)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return slots, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &slots); err != nil || slots == nil {
		return nil, fmt.Errorf("invalid live slot registry %s", path)
	}
	used := make(map[int]bool)
	for id, entry := range slots {
		if !instancePattern.MatchString(id) || !filepath.IsAbs(entry.Root) ||
			entry.Slot < 0 || entry.Slot >= slotCount || used[entry.Slot] {
			return nil, fmt.Errorf("invalid reservation for %s in %s", id, path)
		}
		used[entry.Slot] = true
	}
	return slots, nil
}

func (e *env) saveSlots(slots slotRegistry) error {
	return writeJSON(e.path("slots.json"), slots)
}

func writeJSON(path string, value any) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".live-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	err = enc.Encode(value)
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (e *env) loadNetwork() error {
	slots, err := e.ownedSlots()
	if err != nil {
		return err
	}
	if entry, ok := slots[e.instance]; ok {
		network := slotNetwork(entry.Slot)
		e.network = &network
	}
	return nil
}

const upSuffix = "up"

func (e *env) reserveNetwork(networks []existingNetwork) error {
	return e.withSlots(func(slots slotRegistry) error {
		slot, err := selectSlot(slots, e.instance, networks)
		if err != nil {
			return err
		}
		if _, held := slots[e.instance]; !held {
			slots[e.instance] = reservation{Root: e.root, Slot: slot}
			if err := e.saveSlots(slots); err != nil {
				return err
			}
		}
		network := slotNetwork(slot)
		e.network = &network
		return nil
	})
}

func (e *env) releaseNetwork() error {
	return e.withSlots(func(slots slotRegistry) error {
		if _, held := slots[e.instance]; !held {
			return nil
		}
		delete(slots, e.instance)
		return e.saveSlots(slots)
	})
}

func (e *env) up(names []string, out io.Writer) error {
	if len(names) != 0 {
		if err := requireMac(); err != nil {
			return err
		}
	}
	var guests []osDef
	seen := make(map[string]bool)
	for _, name := range names {
		if seen[name] {
			continue
		}
		o, err := lookupOS(name)
		if err != nil {
			return err
		}
		guests = append(guests, o)
		seen[name] = true
	}
	if err := e.services("up"); err != nil {
		return err
	}
	type result struct {
		index int
		vm    vmInfo
		err   error
	}
	results := make(chan result, len(guests))
	for i, o := range guests {
		go func() {
			vm, err := e.upVM(o)
			results <- result{i, vm, err}
		}()
	}
	vms := make([]vmInfo, len(guests))
	errs := make([]error, len(guests))
	for range guests {
		got := <-results
		vms[got.index], errs[got.index] = got.vm, got.err
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%w; retry up to resume or run down to remove this instance", err)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Instance string   `json:"instance"`
		Subnet   string   `json:"subnet"`
		Ports    bedPorts `json:"ports"`
		VMs      []vmInfo `json:"vms"`
	}{e.instance, e.network.subnet.String(), e.network.ports, vms})
}

func (e *env) upVM(o osDef) (vmInfo, error) {
	name := e.cloneName(o, upSuffix)
	if _, err := os.Stat(e.runDir(name)); errors.Is(err, os.ErrNotExist) {
		return e.vmUp(o.name, upSuffix)
	} else if err != nil {
		return vmInfo{}, err
	}
	return e.startVM(name)
}

func (e *env) down() error {
	if err := e.removeClones(); err != nil {
		return fmt.Errorf("%w; retry down to finish removing this instance", err)
	}
	if err := e.services("down"); err != nil {
		return err
	}
	for _, dir := range []string{"router", "mock-keys", "Corefile"} {
		if err := os.RemoveAll(e.instancePath(dir)); err != nil {
			return err
		}
	}
	return e.releaseNetwork()
}
