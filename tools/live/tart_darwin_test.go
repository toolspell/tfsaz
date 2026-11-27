package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMacOSBootWaitsForTartWithoutListingDisks(t *testing.T) {
	e := &env{home: t.TempDir(), instance: "first"}
	o := oses[1]
	name := e.cloneName(o, "up")
	if err := os.MkdirAll(e.tart().home, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(e.runDir(name), "tart.log")
	putLiveFile(t, logFile)
	if err := os.WriteFile(logFile, []byte("VNC server is running at vnc://:old@127.0.0.1:5000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tart"), []byte("#!/bin/sh\nprintf '%s\\n' 'VNC server is running at vnc://:new@127.0.0.1:5001'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	oldOutput := output
	t.Cleanup(func() { output = oldOutput })
	listed := false
	output = func(_ []string, program string, args ...string) (string, error) {
		if program == "tart" && strings.Join(args, " ") == "list --source local --format json" && !listed {
			listed = true
			return "[]", nil
		}
		return "", errors.New("unexpected command after Tart started")
	}
	if err := e.startVMProcess(o, name); err != nil {
		t.Fatal(err)
	}
	if log := readLiveFile(t, logFile); !strings.Contains(log, "127.0.0.1:5001") || strings.Contains(log, "127.0.0.1:5000") {
		t.Error("startup log did not contain only the new VNC announcement")
	}
}
