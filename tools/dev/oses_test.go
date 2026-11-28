package main

import (
	"fmt"
	"slices"
	"testing"
)

// osCase is a change between two commits of a fixture package and the OSes
// the test matrix must run on for it. An empty content deletes a file.
type osCase struct {
	name string
	base map[string]string
	head map[string]string
	oses []string
	why  string
}

// osSelection returns the selection for the change from tc.base to tc.base
// with tc.head applied, read from memory, so each case costs no git fixture.
// TestGoFileOSesThroughGit runs the first cases through a real diff.
func osSelection(t *testing.T, tc osCase) selection {
	t.Helper()
	head := map[string]string{}
	for name, content := range tc.base {
		head[name] = content
	}
	var files []string
	for name, content := range tc.head {
		if content != tc.base[name] {
			files = append(files, name)
		}
		if content == "" {
			delete(head, name)
		} else {
			head[name] = content
		}
	}
	if len(files) == 0 {
		t.Fatalf("fixture %q has no change", tc.name)
	}
	slices.Sort(files)
	versions := func(file string) ([]byte, bool, []byte, bool, error) {
		b, bOK := tc.base[file]
		h, hOK := head[file]
		return []byte(b), bOK && b != "", []byte(h), hOK, nil
	}
	return selectFor(files, testPkgDirs, versions)
}

// gitOSSelection runs tc through changeSelection on a real git fixture.
func gitOSSelection(t *testing.T, tc osCase) selection {
	t.Helper()
	f := newGitFixture(t)
	base := f.commit(tc.base)
	f.commit(tc.head)
	got, why := changeSelection(base, func() ([]string, error) { return testPkgDirs, nil })
	if why == "no changed files" {
		t.Fatalf("fixture %q has no change", tc.name)
	}
	return got
}

func goSrc(constraint string) string {
	if constraint == "" {
		return "package az\n"
	}
	return "//go:build " + constraint + "\n\npackage az\n"
}

var osCases = []osCase{
	{
		name: "file name suffix",
		base: map[string]string{"internal/az/route_windows.go": goSrc("")},
		head: map[string]string{"internal/az/route_windows.go": goSrc("") + "\nfunc f() {}\n"},
		oses: []string{"windows"},
		why:  "_windows.go builds only for GOOS=windows",
	},
	{
		name: "build tag change moves a file",
		base: map[string]string{"internal/az/route_unix.go": goSrc("linux")},
		head: map[string]string{"internal/az/route_unix.go": goSrc("darwin")},
		oses: []string{"darwin", "linux"},
		why:  "the base builds it for linux, the head for darwin; both sides count",
	},
}

var (
	darwinOnly  = []string{"darwin"}
	linuxOnly   = []string{"linux"}
	windowsOnly = []string{"windows"}
	allRunners  = []string{"darwin", "linux", "windows"}
)

// editCase changes one Go file whose constraint is the same on both sides.
func editCase(name, file, constraint string, oses []string, why string) osCase {
	return osCase{
		name: name,
		base: map[string]string{file: goSrc(constraint)},
		head: map[string]string{file: goSrc(constraint) + "\nfunc f() {}\n"},
		oses: oses,
		why:  why,
	}
}

// deleteCase deletes one Go file.
func deleteCase(name, file, constraint string, oses []string, why string) osCase {
	return osCase{
		name: name,
		base: map[string]string{file: goSrc(constraint)},
		head: map[string]string{file: ""},
		oses: oses,
		why:  why,
	}
}

func init() {
	const az = "internal/az/"
	osCases = append(osCases,
		editCase("suffix darwin", az+"a_darwin.go", "", darwinOnly, "_darwin.go builds only for GOOS=darwin"),
		editCase("suffix linux", az+"a_linux.go", "", linuxOnly, "_linux.go builds only for GOOS=linux"),
		editCase("suffix windows", az+"a_windows.go", "", windowsOnly, "_windows.go builds only for GOOS=windows"),
		editCase("suffix windows amd64", az+"a_windows_amd64.go", "", windowsOnly, "the Windows runner is amd64"),
		editCase("suffix darwin arm64", az+"a_darwin_arm64.go", "", darwinOnly, "the macOS runner is arm64"),
		editCase("suffix linux arm64", az+"a_linux_arm64.go", "", allRunners, "no runner is linux/arm64, so nothing builds it and every OS runs"),
		editCase("suffix windows arm64", az+"a_windows_arm64.go", "", allRunners, "no runner is windows/arm64, so every OS runs"),
		editCase("suffix freebsd", az+"a_freebsd.go", "", allRunners, "no runner is freebsd, so every OS runs"),
		editCase("shared file", az+"a.go", "", allRunners, "no constraint builds on all three runners"),

		editCase("tag or", az+"a.go", "darwin || linux", []string{"darwin", "linux"}, "|| builds on darwin and linux"),
		editCase("tag not", az+"a.go", "!windows", []string{"darwin", "linux"}, "!windows builds on darwin and linux"),
		editCase("tag and", az+"a.go", "linux && amd64", linuxOnly, "only the linux runner is amd64 among linux"),
		editCase("tag and arm64", az+"a.go", "darwin && arm64", darwinOnly, "the macOS runner is arm64"),
		editCase("tag unix", az+"a.go", "unix", []string{"darwin", "linux"}, "unix is true on darwin and linux"),
		editCase("tag windows without cgo", az+"a.go", "windows && !cgo", allRunners, "cgo is on, so no runner builds it and every OS runs"),
		editCase("tag cgo", az+"a.go", "cgo", allRunners, "cgo is on for every runner"),
		editCase("tag custom", az+"a.go", "tfsaztest", allRunners, "no runner sets tfsaztest, so every OS runs"),
		editCase("tag ignore", az+"a.go", "ignore", allRunners, "no runner builds ignore files, so every OS runs"),
		editCase("tag contradicts suffix", az+"a_windows.go", "linux", allRunners, "the suffix and the tag exclude each other, nothing builds it, so every OS runs"),
		editCase("tag restates suffix", az+"a_windows.go", "windows", windowsOnly, "the tag agrees with the suffix"),

		editCase("test suffix", az+"a_windows_test.go", "", windowsOnly, "_windows_test.go builds only for GOOS=windows"),
		editCase("test suffix linux", az+"a_linux_test.go", "", linuxOnly, "_linux_test.go builds only for GOOS=linux"),
		editCase("test shared", az+"a_test.go", "", allRunners, "a plain _test.go builds everywhere"),
		editCase("test tag", az+"a_test.go", "darwin", darwinOnly, "the tag of a _test.go file counts"),
		editCase("test tag custom", az+"a_test.go", "tfsaztest", allRunners, "no runner sets tfsaztest, so every OS runs"),
		editCase("test suffix and tag", az+"a_linux_test.go", "amd64", linuxOnly, "linux suffix and an amd64 tag build on the linux runner"),

		deleteCase("deleted darwin", az+"a_darwin.go", "", darwinOnly, "the base builds it for darwin"),
		deleteCase("deleted linux", az+"a_linux.go", "", linuxOnly, "the base builds it for linux"),
		deleteCase("deleted windows", az+"a_windows.go", "", windowsOnly, "the base builds it for windows"),
		deleteCase("deleted shared", az+"a.go", "", allRunners, "the base builds it everywhere"),
		deleteCase("deleted tag", az+"a.go", "linux || windows", []string{"linux", "windows"}, "the base builds it for linux and windows"),
		deleteCase("deleted custom tag", az+"a.go", "tfsaztest", allRunners, "no runner builds it, so every OS runs"),

		osCase{
			name: "added windows file",
			base: map[string]string{"docs/note.txt": "x"},
			head: map[string]string{az + "a_windows.go": goSrc("")},
			oses: windowsOnly,
			why:  "only the head has the file, and it builds for windows",
		},
		osCase{
			name: "added shared file",
			base: map[string]string{"docs/note.txt": "x"},
			head: map[string]string{az + "a.go": goSrc("")},
			oses: allRunners,
			why:  "only the head has the file, and it builds everywhere",
		},
		osCase{
			name: "tag change moves windows to darwin",
			base: map[string]string{az + "a.go": goSrc("windows")},
			head: map[string]string{az + "a.go": goSrc("darwin")},
			oses: []string{"darwin", "windows"},
			why:  "the base builds it for windows, the head for darwin",
		},
		osCase{
			name: "tag change widens",
			base: map[string]string{az + "a.go": goSrc("linux")},
			head: map[string]string{az + "a.go": goSrc("!windows")},
			oses: []string{"darwin", "linux"},
			why:  "the head adds darwin to the base's linux",
		},
		osCase{
			name: "tag change keeps the OS",
			base: map[string]string{az + "a.go": goSrc("linux")},
			head: map[string]string{az + "a.go": goSrc("linux && amd64")},
			oses: linuxOnly,
			why:  "both sides build only on linux",
		},
		osCase{
			name: "tag removed",
			base: map[string]string{az + "a.go": goSrc("windows")},
			head: map[string]string{az + "a.go": goSrc("")},
			oses: allRunners,
			why:  "the head builds everywhere",
		},
		osCase{
			name: "tag change to custom tag",
			base: map[string]string{az + "a.go": goSrc("windows")},
			head: map[string]string{az + "a.go": goSrc("tfsaztest")},
			oses: windowsOnly,
			why:  "the base builds it for windows; the head builds nowhere and adds nothing, the base side still counts",
		},
		osCase{
			name: "file that does not parse",
			base: map[string]string{az + "a.go": goSrc("")},
			head: map[string]string{az + "a.go": "this is not go\n"},
			oses: allRunners,
			why:  "an unparsable head is an error, so every OS runs",
		},
		osCase{
			name: "broken build line",
			base: map[string]string{az + "a.go": goSrc("")},
			head: map[string]string{az + "a.go": goSrc("windows &&")},
			oses: allRunners,
			why:  "a //go:build line that does not parse is an error, so every OS runs",
		},

		osCase{
			name: "union windows and darwin",
			base: map[string]string{az + "a_windows.go": goSrc(""), az + "b_darwin.go": goSrc("")},
			head: map[string]string{az + "a_windows.go": goSrc("") + "\nfunc f() {}\n", az + "b_darwin.go": goSrc("") + "\nfunc g() {}\n"},
			oses: []string{"darwin", "windows"},
			why:  "each file adds its own OS",
		},
		osCase{
			name: "windows file and non-Go file",
			base: map[string]string{az + "a_windows.go": goSrc("")},
			head: map[string]string{az + "a_windows.go": goSrc("") + "\nfunc f() {}\n", az + "data.txt": "x"},
			oses: allRunners,
			why:  "a non-Go file in the package selects every OS",
		},
		osCase{
			name: "testdata Go file",
			base: map[string]string{az + "testdata/a_windows.go": goSrc("")},
			head: map[string]string{az + "testdata/a_windows.go": goSrc("") + "\nfunc f() {}\n"},
			oses: allRunners,
			why:  "a Go file under testdata is a fixture and selects every OS",
		},
		editCase("tools/live windows file", "tools/live/x_windows.go", "", windowsOnly, "tools/live is a root module package, so its suffix narrows"),
		editCase("tools/dev windows file", "tools/dev/x_windows.go", "", allRunners, "Go files under tools/dev select every OS"),
		editCase("mock module windows file", mockModule+"/x_windows.go", "", allRunners, "the mock module is a separate module and selects every OS"),
	)
}

func TestMatrixByOS(t *testing.T) {
	win := func(n int) []matrixEntry {
		var e []matrixEntry
		for i := 1; i <= n; i++ {
			e = append(e, matrixEntry{"windows-latest", fmt.Sprintf("%d/%d", i, n)})
		}
		return e
	}
	tests := []struct {
		name string
		base map[string]string
		head map[string]string
		want []matrixEntry
		why  string
	}{
		{
			name: "windows file keeps three shards",
			base: map[string]string{"internal/az/a_windows.go": goSrc("")},
			head: map[string]string{"internal/az/a_windows.go": goSrc("") + "\nfunc f() {}\n"},
			want: win(3),
			why:  "internal/az on Windows runs in 3 shards and no other OS runs",
		},
		{
			name: "darwin file drops Windows",
			base: map[string]string{"internal/az/a_darwin.go": goSrc("")},
			head: map[string]string{"internal/az/a_darwin.go": goSrc("") + "\nfunc f() {}\n"},
			want: []matrixEntry{{"macos-latest", "1/1"}},
			why:  "only macOS runs, Windows has no entry",
		},
		{
			name: "linux file drops Windows",
			base: map[string]string{"internal/az/a_linux.go": goSrc("")},
			head: map[string]string{"internal/az/a_linux.go": goSrc("") + "\nfunc f() {}\n"},
			want: []matrixEntry{{"ubuntu-latest", "1/1"}},
			why:  "only Ubuntu runs, Windows has no entry",
		},
		{
			name: "shared file runs everywhere",
			base: map[string]string{"internal/az/a.go": goSrc("")},
			head: map[string]string{"internal/az/a.go": goSrc("") + "\nfunc f() {}\n"},
			want: append([]matrixEntry{{"macos-latest", "1/1"}, {"ubuntu-latest", "1/1"}}, win(3)...),
			why:  "all OSes with Windows in 3 shards",
		},
		{
			name: "tools/live windows file has one shard",
			base: map[string]string{"tools/live/x_windows.go": goSrc("")},
			head: map[string]string{"tools/live/x_windows.go": goSrc("") + "\nfunc f() {}\n"},
			want: win(1),
			why:  "tools/live needs a single Windows shard",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := osSelection(t, osCase{name: tc.name, base: tc.base, head: tc.head}).matrix()
			if !slices.Equal(got, tc.want) {
				t.Errorf("matrix %v, want %v (%s)", got, tc.want, tc.why)
			}
		})
	}
}

func TestGoFileOSes(t *testing.T) {
	for _, tc := range osCases {
		t.Run(tc.name, func(t *testing.T) {
			got := osSelection(t, tc)
			if !slices.Equal(got.oses, tc.oses) {
				t.Errorf("OSes %q, want %q (%s)", got.oses, tc.oses, tc.why)
			}
		})
	}
}

func TestGoFileOSesThroughGit(t *testing.T) {
	for _, tc := range osCases[:2] {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitOSSelection(t, tc); !slices.Equal(got.oses, tc.oses) {
				t.Errorf("OSes %q, want %q (%s)", got.oses, tc.oses, tc.why)
			}
		})
	}
}
