package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFailedVMUpKeepsLog(t *testing.T) {
	for _, archiveOK := range []bool{true, false} {
		name := "archive available"
		if !archiveOK {
			name = "archive blocked"
		}
		t.Run(name, func(t *testing.T) { checkFailedVMUpCleanup(t, archiveOK) })
	}
}

func checkFailedVMUpCleanup(t *testing.T, archiveOK bool) {
	t.Helper()
	e := &env{home: t.TempDir(), instance: "first"}
	o := oses[1]
	name := e.cloneName(o, "up")
	logFile := filepath.Join(e.runDir(name), "tart.log")
	putLiveFile(t, filepath.Join(e.runDir(name), "id_ed25519"))
	putLiveFile(t, filepath.Join(e.tart().home, "test"))
	oldOutput := output
	t.Cleanup(func() { output = oldOutput })
	deleted := false
	output = func(_ []string, bin string, args ...string) (string, error) {
		if bin != "tart" {
			t.Fatalf("unexpected command: %s %q", bin, args)
		}
		switch strings.Join(args, " ") {
		case "list --source local --format json":
			return `[{"Name":"` + name + `","State":"stopped"}]`, nil
		case "stop " + name + " --timeout 30":
			return "", nil
		case "delete " + name:
			deleted = true
			return "", nil
		}
		t.Fatalf("unexpected Tart command: %q", args)
		return "", nil
	}
	const body = "old line\n2\n3\n4\n5\nVNC server is running at vnc://:secret@127.0.0.1:5900\nVM failed to boot\n"
	if err := os.WriteFile(logFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("VM did not start")
	kept := e.instancePath("failed-starts", name+".log")
	if !archiveOK {
		putLiveFile(t, filepath.Dir(kept))
	}
	err := e.cleanupFailedVM(o, name, cause)
	if archiveOK {
		if got := readLiveFile(t, kept); got != body || !strings.Contains(err.Error(), kept) {
			t.Errorf("kept log = %q; startup error = %v", got, err)
		}
	} else if !strings.Contains(err.Error(), "save startup log") {
		t.Errorf("archive error missing from %v", err)
	}
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), "VM failed to boot") ||
		strings.Contains(err.Error(), "old line") || strings.Contains(err.Error(), "secret") {
		t.Errorf("startup error = %v", err)
	}
	if _, err := os.Stat(e.runDir(name)); !os.IsNotExist(err) {
		t.Errorf("failed clone's run directory remains: %v", err)
	}
	if !deleted {
		t.Error("failed clone was not deleted")
	}
}

func TestLiveRemovePeer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		psOut   string
		psErr   error
		wantCmd bool
	}{
		{name: "running router", psOut: "router-id", wantCmd: true},
		{name: "stopped router"},
		{name: "Docker stopped", psErr: errors.New("Docker engine stopped")},
		{name: "Docker stopped with partial output", psOut: "router-id", psErr: errors.New("Docker engine stopped")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			network := slotNetwork(0)
			e := &env{home: t.TempDir(), instance: "first", network: &network}
			name := e.cloneName(oses[0], "abc123")
			peer := e.instancePath("router", "peers", name)
			putLiveFile(t, peer)
			warnings := captureLiveStderr(t)
			oldOutput, oldCmd := output, cmd
			t.Cleanup(func() { output, cmd = oldOutput, oldCmd })
			queries, updates := 0, 0
			output = func(_ []string, bin string, args ...string) (string, error) {
				if bin != "docker" || !slices.Equal(args, e.composeArgs([]string{"ps", "--status", "running", "-q", "router"})) {
					t.Fatalf("unexpected command: %s %q", bin, args)
				}
				queries++
				return tc.psOut, tc.psErr
			}
			cmd = func(_ string, _ []string, bin string, args ...string) error {
				if bin != "docker" || !slices.Equal(args, e.composeArgs([]string{"exec", "-T", "router", "peer", "remove", name})) {
					t.Fatalf("unexpected command: %s %q", bin, args)
				}
				updates++
				return os.Remove(peer)
			}
			for range 2 {
				if err := e.removePeer(name); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(peer); !os.IsNotExist(err) {
				t.Errorf("peer file remained: %v", err)
			}
			if queries != 1 || (updates == 1) != tc.wantCmd {
				t.Errorf("router queried %d times, updated %d times", queries, updates)
			}
			if got := strings.Contains(readLiveFile(t, warnings), "the router was not updated"); got != (tc.psErr != nil) {
				t.Errorf("unexpected warning: %s", readLiveFile(t, warnings))
			}
		})
	}
}

func TestLiveLegacyClones(t *testing.T) {
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

func TestLiveCloneRowsRejectMismatchedMetadata(t *testing.T) {
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

func TestLiveCleanAndDown(t *testing.T) {
	e := &env{home: t.TempDir(), root: t.TempDir(), instance: "first"}
	if err := e.saveSlots(slotRegistry{"first": {Root: e.root, Slot: 0}}); err != nil {
		t.Fatal(err)
	}
	oldOutput, oldCmd := output, cmd
	t.Cleanup(func() { output, cmd = oldOutput, oldCmd })
	downs := 0
	cmd = func(_ string, _ []string, bin string, args ...string) error {
		if bin != "docker" || !slices.Equal(args, e.composeArgs([]string{"down", "--remove-orphans", "--rmi", "local"})) {
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
