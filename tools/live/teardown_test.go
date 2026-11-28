package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLiveCloneRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T)
	}{
		{"legacy inventory and deletion", checkLegacyCloneRows},
		{"mismatched metadata", checkMismatchedCloneRow},
	} {
		t.Run(tc.name, tc.check)
	}
}

func checkLegacyCloneRows(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	const tartName, qemuName = "tfsaz-ubuntu-a1b2c3", "tfsaz-windows-c4d5e6"
	const peerOnly, runOnly = "tfsaz-macos-peer", "tfsaz-macos-run"
	putLiveFile(t, filepath.Join(e.tart().home, "test"))
	putLiveFile(t, filepath.Join(e.qemu().dir(qemuName), "disk.qcow2"))
	for _, name := range []string{tartName, qemuName} {
		putLiveFile(t, e.path("runs", name, "vm.json"))
		putLiveFile(t, e.path("router", "peers", name))
	}
	putLiveFile(t, e.path("router", "peers", peerOnly))
	putLiveFile(t, e.path("runs", runOnly, "vm.json"))
	old := output
	t.Cleanup(func() { output = old })
	var tartCommands []string
	output = func(_ []string, bin string, args ...string) (string, error) {
		if bin != "tart" {
			t.Fatalf("unexpected command: %s %q", bin, args)
		}
		command := strings.Join(args, " ")
		tartCommands = append(tartCommands, command)
		switch command {
		case "list --source local --format json":
			return `[{"Name":"tfsaz-ubuntu-a1b2c3","State":"running"}]`, nil
		case "stop " + tartName + " --timeout 30", "delete " + tartName:
			return "", nil
		default:
			t.Fatalf("unexpected Tart command: %q", args)
			return "", nil
		}
	}
	rows, err := e.cloneRows()
	want := []string{peerOnly, runOnly, tartName, qemuName}
	if err != nil || len(rows) != len(want) {
		t.Fatalf("cloneRows = %v, %v; want %v", rows, err, want)
	}
	for i, row := range rows {
		if row.Name != want[i] || !row.Legacy || (i < 2 && (row.State != "missing" || row.OS != "macos")) {
			t.Errorf("legacy row %d = %+v; want %s", i, row, want[i])
		}
	}
	if got := baseCloneError(oses[0], []string{tartName}).Error(); !strings.Contains(got, "go run ./tools/live vm down "+tartName) {
		t.Errorf("base refusal = %q", got)
	}
	for _, name := range want {
		h, args, _, err := resolveHelp([]string{"vm", "down", name})
		if err != nil {
			t.Fatal(err)
		}
		call := liveInvocation{commandHelp: h}
		if err := call.readArgs(args, call.flagSet()); err != nil {
			t.Fatal(err)
		}
		if err := e.vmDown(call.args[0]); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{e.path("runs", name), e.path("router", "peers", name)} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("%s should be gone: %v", path, err)
			}
		}
	}
	if !slices.Contains(tartCommands, "delete "+tartName) {
		t.Errorf("Tart clone was not deleted: %v", tartCommands)
	}
	if _, err := os.Stat(e.qemu().dir(qemuName)); !os.IsNotExist(err) {
		t.Errorf("QEMU clone should be gone: %v", err)
	}
}

func checkMismatchedCloneRow(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	o := oses[2]
	name := e.cloneName(o, "record")
	putLiveFile(t, filepath.Join(e.qemu().dir(name), "disk.qcow2"))
	putLiveFile(t, filepath.Join(e.runDir(name), "vm.json"))
	info := vmInfo{Name: e.cloneName(o, "other"), OS: o.name, IP: "192.0.2.1", SSHKey: "other-key"}
	if err := writeJSON(filepath.Join(e.runDir(name), "vm.json"), info); err != nil {
		t.Fatal(err)
	}
	rows, err := e.cloneRows()
	if err != nil || len(rows) != 1 {
		t.Fatalf("cloneRows = %v, %v; want one inventory row", rows, err)
	}
	if row := rows[0]; row.vmInfo != (vmInfo{Name: name}) || row.State != "stopped" {
		t.Errorf("row = %+v; want name and provider state", row)
	}
}

func TestLiveInstanceTeardown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check func(*testing.T)
	}{
		{"current instance", checkInstanceCleanAndDown},
		{"orphaned instances", checkOrphanedInstances},
	} {
		t.Run(tc.name, tc.check)
	}
}

func checkInstanceCleanAndDown(t *testing.T) {
	e := &env{home: t.TempDir(), root: t.TempDir(), instance: "first"}
	if err := e.saveSlots(slotRegistry{"first": {Root: e.root, Slot: 0}}); err != nil {
		t.Fatal(err)
	}
	oldOutput, oldCmd := output, cmd
	t.Cleanup(func() { output, cmd = oldOutput, oldCmd })
	downs := 0
	cmd = func(_ string, _ []string, bin string, args ...string) error {
		if bin != "docker" || !slices.Equal(args, e.composeArgs([]string{"down", "--remove-orphans"})) {
			t.Fatalf("unexpected command: %s %q", bin, args)
		}
		downs++
		return nil
	}
	output = func(_ []string, bin string, args ...string) (string, error) {
		if bin == "docker" && slices.Equal(args, e.composeArgs([]string{"ps", "--status", "running", "-q", "router"})) {
			return "", errors.New("could not inspect the router")
		}
		if bin != "docker" || !slices.Equal(args, []string{"network", "ls", "--filter", "label=com.docker.compose.project=tfsaz-live-first", "--filter", "label=com.docker.compose.network=bed", "--quiet"}) {
			t.Fatalf("unexpected command: %s %q", bin, args)
		}
		return "", nil
	}
	otherClone := e.qemu().dir("tfsaz-live-second-windows-abc123")
	putLiveFile(t, filepath.Join(otherClone, "disk.qcow2"))
	serviceState := e.instancePath("mock-keys", "ca.pem")
	putLiveFile(t, serviceState)
	keptLog := e.instancePath("failed-starts", "clone.log")
	putLiveFile(t, keptLog)
	for _, action := range []string{"clean", "down"} {
		t.Run(action, func(t *testing.T) {
			clone := e.cloneName(oses[2], "abc123")
			putLiveFile(t, filepath.Join(e.qemu().dir(clone), "disk.qcow2"))
			putLiveFile(t, e.instancePath("router", "peers", clone))
			if err := e.runCommand(liveInvocation{commandHelp: commandHelp{path: action}}, io.Discard, false); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(e.qemu().dir(clone)); !os.IsNotExist(err) {
				t.Errorf("clone should be gone after %s: %v", action, err)
			}
			if _, err := os.Stat(otherClone); err != nil {
				t.Errorf("other instance's clone changed after %s: %v", action, err)
			}
			slots, err := e.readSlots()
			if err != nil {
				t.Fatal(err)
			}
			_, serviceStateErr := os.Stat(serviceState)
			_, keptLogErr := os.Stat(keptLog)
			switch action {
			case "clean":
				if downs != 0 || len(slots) != 1 || serviceStateErr != nil || keptLogErr != nil {
					t.Errorf("clean changed services or slot: down calls %d, slots %v, state %v", downs, slots, serviceStateErr)
				}
			case "down":
				if downs != 1 || len(slots) != 0 || !os.IsNotExist(serviceStateErr) || !os.IsNotExist(keptLogErr) {
					t.Errorf("down did not clear services or slot: down calls %d, slots %v, state %v", downs, slots, serviceStateErr)
				}
			}
		})
	}
}

func checkOrphanedInstances(t *testing.T) {
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
				if bin != "docker" || !slices.Equal(args[5:], []string{"down", "--remove-orphans"}) {
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
