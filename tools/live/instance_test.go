package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestLiveInstanceLock(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	unlock, err := e.lockInstance()
	if err != nil {
		t.Fatal(err)
	}
	path := e.path("instance-locks", "first.lock")
	other := flock.New(path)
	if ok, err := other.TryLock(); ok || err != nil {
		t.Errorf("second TryLock = %t, %v; want contention", ok, err)
		_ = other.Close()
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	if release, err := takeLock(ctx, other, false); err == nil || !strings.Contains(err.Error(), "is busy") {
		t.Errorf("takeLock with expired deadline = %v; want busy error", err)
		if release != nil {
			release()
		}
	}
	unlock()
	if release, err := e.lockInstance(); err != nil {
		t.Fatal(err)
	} else {
		release()
	}
}

func TestLiveBaseLock(t *testing.T) {
	builder := &env{home: t.TempDir(), instance: "builder"}
	other := &env{home: builder.home, instance: "other"}
	for _, base := range oses {
		t.Run(base.name, func(t *testing.T) {
			unlock, err := builder.lockBase(base, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(unlock)
			for _, guest := range oses {
				path := other.path("base-locks", guest.name+".lock")
				for _, shared := range []bool{false, true} {
					probe := flock.New(path)
					tryLock := probe.TryLock
					if shared {
						tryLock = probe.TryRLock
					}
					got, err := tryLock()
					_ = probe.Close()
					if want := guest.name != base.name; got != want || err != nil {
						t.Errorf("lock %s (shared=%t) = %t, %v; want %t", path, shared, got, err, want)
					}
				}
			}
		})
	}
}

func TestLiveInstanceLockPolicy(t *testing.T) {
	locked := strings.Split("up,down,clean,setup,verify,services up,services down,mock reset,vm up,vm start,vm stop,vm down", ",")
	for _, h := range liveCommands {
		t.Run(h.path, func(t *testing.T) {
			command, action, _ := strings.Cut(h.path, " ")
			if got, want := locksInstance(command, action), slices.Contains(locked, h.path); got != want {
				t.Errorf("locksInstance(%s) = %t; want %t", h.path, got, want)
			}
		})
	}
}

func TestLivePrune(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(fmt.Sprintf("Docker offline=%t", offline), func(t *testing.T) {
			home := t.TempDir()
			e := &env{home: home, instance: "current", root: filepath.Join(home, "current"), bed: filepath.Join(home, "bed")}
			orphan := &env{home: home, instance: "orphan", bed: e.bed, root: filepath.Join(home, "gone")}
			existing := t.TempDir()
			slots := slotRegistry{
				"current": {Root: e.root, Slot: 0},
				"orphan":  {Root: orphan.root, Slot: 1},
				"orphan2": {Root: existing, Slot: 2},
				"other":   {Root: filepath.Join(home, "gone-too"), Slot: 3},
			}
			if err := e.saveSlots(slots); err != nil {
				t.Fatal(err)
			}
			clone := orphan.cloneName(oses[2], "abc123")
			putLiveFile(t, filepath.Join(orphan.runDir(clone), "vm.json"))
			peer := orphan.instancePath("router", "peers", clone)
			putLiveFile(t, peer)
			putLiveFile(t, filepath.Join(orphan.qemu().dir(clone), "disk.qcow2"))
			protected := &env{home: home, instance: "orphan2"}
			otherClone := filepath.Join(e.qemu().dir(protected.cloneName(oses[2], "abc123")), "disk.qcow2")
			putLiveFile(t, otherClone)
			legacy := filepath.Join(e.qemu().dir("tfsaz-windows-abc123"), "disk.qcow2")
			putLiveFile(t, legacy)
			putLiveFile(t, filepath.Join(e.runDir(e.cloneName(oses[0], "abc123")), "vm.json"))

			oldOutput, oldCmd := output, cmd
			t.Cleanup(func() { output, cmd = oldOutput, oldCmd })
			var dock []string
			output = func(_ []string, bin string, args ...string) (string, error) {
				t.Helper()
				if bin != "docker" {
					t.Fatalf("unexpected command: %s %q", bin, args)
				}
				command := strings.Join(args, " ")
				dock = append(dock, command)
				if offline {
					return "", errors.New("Docker engine stopped")
				}
				switch command {
				case "info --format {{.ServerVersion}}":
					return "test", nil
				case "container ls --all --quiet --filter label=com.docker.compose.project=tfsaz-live-orphan":
					return "id1\nid2", nil
				case "network ls --quiet --filter label=com.docker.compose.project=tfsaz-live-orphan":
					return "network-id", nil
				case "container rm --force id1 id2", "network rm network-id",
					"container ls --all --quiet --filter label=com.docker.compose.project=tfsaz-live-other",
					"network ls --quiet --filter label=com.docker.compose.project=tfsaz-live-other":
					return "", nil
				default:
					if slices.Equal(args, orphan.composeArgs([]string{"ps", "--status", "running", "-q", "router"})) {
						return "", nil
					}
					t.Fatalf("unexpected Docker command: %q", args)
					return "", nil
				}
			}
			var downProjects []string
			cmd = func(_ string, _ []string, bin string, args ...string) error {
				t.Helper()
				if len(args) < 5 {
					t.Fatalf("unexpected command: %s %q", bin, args)
				}
				if slices.Equal(args[5:], []string{"ps", "--format", "  {{.Service}}\t{{.State}}"}) {
					return nil
				}
				if bin != "docker" || !slices.Equal(args[5:], []string{"down", "--remove-orphans", "--rmi", "local"}) {
					t.Fatalf("unexpected command: %s %q", bin, args)
				}
				downProjects = append(downProjects, args[2])
				return nil
			}

			var status bytes.Buffer
			_ = e.status(&status)
			if got := status.String(); !strings.Contains(got, "orphaned instance orphan (slot 1") ||
				!strings.Contains(got, "orphaned instance other (slot 3") || !strings.Contains(got, "go run ./tools/live prune") ||
				strings.Contains(got, "orphaned instance current") || strings.Contains(got, "orphaned instance orphan2") {
				t.Errorf("status reported %q", got)
			}

			warnings := captureLiveStderr(t)
			call := liveInvocation{commandHelp: commandHelp{path: "prune"}}
			if err := e.runCommand(call, io.Discard, false); err != nil {
				t.Fatal(err)
			}
			delete(slots, "orphan")
			delete(slots, "other")
			if got, err := e.readSlots(); err != nil || !maps.Equal(got, slots) {
				t.Errorf("slots = %v, %v; want %v", got, err, slots)
			}
			for _, path := range []string{orphan.instancePath(), orphan.qemu().dir(clone), peer} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Errorf("%s should be gone: %v", path, err)
				}
			}
			for _, path := range []string{otherClone, legacy, e.instancePath()} {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("%s should remain: %v", path, err)
				}
			}
			if offline {
				if len(downProjects) != 0 || !strings.Contains(readLiveFile(t, warnings), "could not reach Docker to remove containers, networks or locally built images for tfsaz-live-orphan") {
					t.Errorf("offline cleanup did not warn; down projects %v", downProjects)
				}
			} else if !slices.Equal(downProjects, []string{"tfsaz-live-orphan", "tfsaz-live-other"}) ||
				!slices.Contains(dock, "container rm --force id1 id2") || !slices.Contains(dock, "network rm network-id") {
				t.Errorf("unexpected project cleanup %v, %v", downProjects, dock)
			}
			commandsBefore, downBefore := len(dock), len(downProjects)
			if err := e.runCommand(call, io.Discard, false); err != nil || len(dock) != commandsBefore || len(downProjects) != downBefore {
				t.Errorf("second prune was not a no-op: %v, commands %v, down %v", err, dock[commandsBefore:], downProjects[downBefore:])
			}
		})
	}
}

func putLiveFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func captureLiveStderr(t *testing.T) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = file
	t.Cleanup(func() {
		os.Stderr = old
		_ = file.Close()
	})
	return file.Name()
}

func readLiveFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
