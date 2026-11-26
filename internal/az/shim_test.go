package az

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestShimUninstall(t *testing.T) {
	write := func(t *testing.T, path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	copiesKept := shimNames
	if runtime.GOOS == "windows" {
		copiesKept = nil
	}
	for _, tc := range []struct {
		name string
		// setup returns the executable that uninstall runs as.
		setup func(t *testing.T, bin, exe string) string
		kept  []string
		want  string
	}{
		{"installed shims", func(t *testing.T, bin, exe string) string {
			if err := installShimLinks(bin, exe); err != nil {
				t.Fatal(err)
			}
			return exe
		}, nil, "Removed"},
		{"no shims", func(t *testing.T, bin, exe string) string { return exe }, nil, "Found no az or git-remote-tfsaz shim"},
		{"foreign az beside a shim", func(t *testing.T, bin, exe string) string {
			write(t, shimPath(bin, "az"), "other")
			if err := installShim(exe, shimPath(bin, "git-remote-tfsaz")); err != nil {
				t.Fatal(err)
			}
			return exe
		}, []string{"az"}, "Removed"},
		{"copies of this TFSaz", func(t *testing.T, bin, exe string) string {
			for _, name := range shimNames {
				write(t, shimPath(bin, name), "current fixture")
			}
			return exe
		}, copiesKept, ""},
		{"stale copy", func(t *testing.T, bin, exe string) string {
			write(t, shimPath(bin, "az"), "older fixture")
			return exe
		}, []string{"az"}, ""},
		{"this TFSaz itself", func(t *testing.T, bin, exe string) string {
			write(t, shimPath(bin, "az"), "current fixture")
			return shimPath(bin, "az")
		}, []string{"az"}, ""},
		{"link to another executable", func(t *testing.T, bin, exe string) string {
			other := filepath.Join(filepath.Dir(exe), "other")
			write(t, other, "other")
			if err := os.Symlink(other, shimPath(bin, "az")); err != nil {
				t.Skip("symbolic links unavailable:", err)
			}
			return exe
		}, []string{"az"}, ""},
		{"link to another installation with the same bytes", func(t *testing.T, bin, exe string) string {
			other := filepath.Join(filepath.Dir(exe), "other")
			write(t, other, "current fixture")
			if err := os.Symlink(other, shimPath(bin, "az")); err != nil {
				t.Skip("symbolic links unavailable:", err)
			}
			return exe
		}, []string{"az"}, ""},
		{"started through the az link", func(t *testing.T, bin, exe string) string {
			for _, name := range shimNames {
				if err := os.Symlink(exe, shimPath(bin, name)); err != nil {
					t.Skip("symbolic links unavailable:", err)
				}
			}
			return shimPath(bin, "az")
		}, nil, "Removed"},
		{"helper linked through the az link", func(t *testing.T, bin, exe string) string {
			az := shimPath(bin, "az")
			if err := os.Symlink(exe, az); err != nil {
				t.Skip("symbolic links unavailable:", err)
			}
			if err := installShimLinks(bin, az); err != nil {
				t.Fatal(err)
			}
			return exe
		}, nil, "Removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, "tfsaz")
			write(t, exe, "current fixture")
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			executable := tc.setup(t, bin, exe)
			var out bytes.Buffer
			err := uninstallShimLinks(&out, bin, executable)
			if (err != nil) != (len(tc.kept) != 0) || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("uninstall: %v, stdout %q", err, &out)
			}
			for _, name := range shimNames {
				path := shimPath(bin, name)
				_, statErr := os.Lstat(path)
				if kept := slices.Contains(tc.kept, name); kept != (statErr == nil) ||
					kept && !strings.Contains(err.Error(), "left "+path+" in place") {
					t.Errorf("%s: kept %v, want %v, error %v", name, statErr == nil, kept, err)
				}
			}
			if data, err := os.ReadFile(exe); err != nil || string(data) != "current fixture" {
				t.Fatalf("TFSaz executable changed: %q, %v", data, err)
			}
		})
	}
	a := app{stdout: io.Discard, stderr: io.Discard}
	for _, args := range [][]string{
		{"shim", "uninstall"}, {"shim", "remove", "--dir", t.TempDir()},
		{"shim", "install"}, {"shim", "install", "--dir", ""}, {"shim", "install", "--path", t.TempDir()},
	} {
		if err := a.run(t.Context(), args); ExitCode(err) != 2 {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// Package managers can replace a versioned executable on upgrade, so install
// links to the stable TFSaz on PATH only when it is this same file.
func TestShimInstallTarget(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		// stable creates the TFSaz on PATH and reports whether shims should link to it.
		stable func(t *testing.T, path string) bool
	}{
		{"PATH has this executable", func(t *testing.T, path string) bool {
			if err := os.Link(exe, path); err != nil {
				t.Skip("hard links unavailable:", err)
			}
			return true
		}},
		{"PATH has another executable", func(t *testing.T, path string) bool {
			if err := os.WriteFile(path, []byte("other"), 0700); err != nil {
				t.Fatal(err)
			}
			return false
		}},
		{"PATH has none", func(t *testing.T, path string) bool { return false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stable := shimPath(filepath.Join(dir, "pkg"), "tfsaz")
			if err := os.Mkdir(filepath.Dir(stable), 0700); err != nil {
				t.Fatal(err)
			}
			want := exe
			if tc.stable(t, stable) {
				want = stable
			}
			t.Setenv("PATH", filepath.Dir(stable))
			bin := filepath.Join(dir, "bin")
			var out bytes.Buffer
			a := app{stdout: &out, stderr: io.Discard}
			// A second install finds its own shims and changes nothing.
			for range 2 {
				if err := a.runShim([]string{"install", "--dir", bin}); err != nil {
					t.Fatalf("install: %v", err)
				}
			}
			if !strings.Contains(out.String(), "shims in "+bin) {
				t.Errorf("install output %q does not name %s", &out, bin)
			}
			for _, name := range shimNames {
				target, err := os.Readlink(shimPath(bin, name))
				if err != nil {
					t.Skip("install copied the executable instead of linking it:", err)
				}
				if target != want {
					t.Errorf("%s links to %s, want %s", name, target, want)
				}
			}
		})
	}
}
