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
	e := &env{home: t.TempDir(), instance: "first"}
	o := oses[1]
	name := e.cloneName(o, "up")
	logFile := filepath.Join(e.runDir(name), "tart.log")
	putLiveFile(t, filepath.Join(e.runDir(name), "id_ed25519"))
	const body = "old line\n2\n3\n4\n5\nVNC server is running at vnc://:secret@127.0.0.1:5900\nVM failed to boot\n"
	if err := os.WriteFile(logFile, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("VM did not start")
	err := e.cleanupFailedVM(o, name, cause)
	kept := e.instancePath("failed-starts", name+".log")
	if got := readLiveFile(t, kept); got != body {
		t.Errorf("kept log = %q; want %q", got, body)
	}
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), kept) || !strings.Contains(err.Error(), "VM failed to boot") ||
		strings.Contains(err.Error(), "old line") || strings.Contains(err.Error(), "secret") {
		t.Errorf("startup error = %v", err)
	}
	if _, err := os.Stat(e.runDir(name)); !os.IsNotExist(err) {
		t.Errorf("failed clone's run directory remains: %v", err)
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
	putLiveFile(t, filepath.Join(e.tart().home, "test"))
	putLiveFile(t, filepath.Join(e.qemu().dir(qemuName), "disk.qcow2"))
	for _, name := range []string{tartName, qemuName} {
		putLiveFile(t, e.path("runs", name, "vm.json"))
		putLiveFile(t, e.path("router", "peers", name))
	}
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
	if err != nil || len(rows) != 2 || !rows[0].Legacy || !rows[1].Legacy ||
		rows[0].Name != tartName || rows[1].Name != qemuName {
		t.Fatalf("cloneRows = %v, %v; want both legacy clones", rows, err)
	}
	if got := baseCloneError(oses[0], []string{tartName}).Error(); !strings.Contains(got, "go run ./tools/live vm down "+tartName) {
		t.Errorf("base refusal = %q", got)
	}
	for _, name := range []string{tartName, qemuName} {
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
