package az

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestRemoveStaleRouteRunOverlays(t *testing.T) {
	exited := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := strconv.Itoa(exited.Process.Pid)
	livePID := strconv.Itoa(os.Getpid())

	for _, tc := range []struct {
		name  string
		files map[string]string
		kept  bool
	}{
		{name: "overlay without lock from an older build", files: map[string]string{"git-1/git": ""}},
		{name: "run killed before it launched its process", files: map[string]string{"git-2.lock": "", "git-2/git": ""}},
		{name: "launched process exited", files: map[string]string{"git-3.lock": "", "git-3.pid": deadPID + "\n", "git-3/git": ""}},
		{name: "unreadable PID", files: map[string]string{"git-4.lock": "", "git-4.pid": "garbage", "git-4/git": ""}},
		{name: "PID file without lock", files: map[string]string{"git-5.pid": deadPID}},
		{name: "launched process outlived its run", files: map[string]string{"git-6.lock": "", "git-6.pid": livePID, "git-6/git": ""}, kept: true},
		{name: "unrelated file", files: map[string]string{"notes": ""}, kept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "tfsaz.json.run")
			for name, content := range tc.files {
				path := filepath.Join(parent, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				managerTestWrite(t, path, content)
			}
			if err := removeStaleRouteRunOverlays(parent); err != nil {
				t.Fatal(err)
			}
			for name := range tc.files {
				_, err := os.Lstat(filepath.Join(parent, filepath.FromSlash(name)))
				if tc.kept && err != nil {
					t.Fatalf("%s was removed: %v", name, err)
				}
				if !tc.kept && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("%s was kept: %v", name, err)
				}
			}
		})
	}
}

func TestRouteRunOverlayLock(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "tfsaz.json.run")
	run, err := makeRouteRunOverlay(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeStaleRouteRunOverlays(parent); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{run.dir, run.dir + ".lock"} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("cleanup removed %s of a running route run: %v", path, err)
		}
	}
	if err := run.recordChild(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := run.remove(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("route run left %v: %v", entries, err)
	}

	// A cleanup may remove a fresh lock file before its creator locks it.
	removed := run.dir + ".lock"
	if lock, err := tryRouteRunLock(removed); lock != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("locked a removed route run lock file: %v", err)
	}
	if _, err := os.Lstat(removed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recreated a removed route run lock file: %v", err)
	}
}
