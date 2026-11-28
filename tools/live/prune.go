package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func (e *env) orphanedSlots(slots slotRegistry) ([]string, error) {
	var orphans []string
	for _, id := range slices.Sorted(maps.Keys(slots)) {
		root := slots[id].Root
		if filepath.Clean(root) == e.root {
			continue
		}
		if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
			orphans = append(orphans, id)
		} else if err != nil {
			return nil, err
		}
	}
	return orphans, nil
}

func (e *env) prune() error {
	slots, err := e.readSlots()
	if err != nil {
		return err
	}
	orphans, err := e.orphanedSlots(slots)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range orphans {
		if err := e.pruneInstance(id, slots[id]); err != nil {
			errs = append(errs, fmt.Errorf("prune %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func (e *env) pruneInstance(id string, expected reservation) error {
	network := slotNetwork(expected.Slot)
	orphan := &env{instance: id, root: expected.Root, home: e.home, bed: e.bed, network: &network}
	unlock, err := orphan.lockInstance()
	if err != nil {
		return err
	}
	defer unlock()
	slots, err := e.readSlots()
	if err != nil {
		return err
	}
	if actual, ok := slots[id]; !ok || actual != expected {
		return nil
	}
	stillOrphaned, err := e.orphanedSlots(slotRegistry{id: expected})
	if err != nil || len(stillOrphaned) == 0 {
		return err
	}
	if err := orphan.removeClones(); err != nil {
		return err
	}
	if _, err := output(nil, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		step("could not reach Docker to remove containers, networks or locally built images for %s: %v", orphan.project(), err)
	} else if err := orphan.pruneDocker(); err != nil {
		return err
	}
	if err := os.RemoveAll(orphan.instancePath()); err != nil {
		return err
	}
	if err := orphan.releaseNetwork(); err != nil {
		return err
	}
	step("freed slot %d for orphaned instance %s", expected.Slot, id)
	return nil
}

func (e *env) pruneDocker() error {
	if err := e.composeDown(); err != nil {
		return err
	}
	filter := "label=com.docker.compose.project=" + e.project()
	containers, err := output(nil, "docker", "container", "ls", "--all", "--quiet", "--filter", filter)
	if err != nil {
		return err
	}
	if ids := strings.Fields(containers); len(ids) != 0 {
		if _, err := output(nil, "docker", append([]string{"container", "rm", "--force"}, ids...)...); err != nil {
			return err
		}
	}
	networks, err := output(nil, "docker", "network", "ls", "--quiet", "--filter", filter)
	if err != nil {
		return err
	}
	if ids := strings.Fields(networks); len(ids) != 0 {
		_, err = output(nil, "docker", append([]string{"network", "rm"}, ids...)...)
	}
	return err
}
