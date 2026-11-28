package main

import (
	"bytes"
	"go/build"
	"io"
	"path"
	"strings"
)

// A runner is one test matrix OS with the system and architecture Go builds
// for there.
type runner struct {
	label, goos, goarch string
}

var runners = []runner{
	{"macos-latest", "darwin", "arm64"},
	{"ubuntu-latest", "linux", "amd64"},
	{"windows-latest", "windows", "amd64"},
}

// allOSes is every runner's GOOS.
func allOSes() []string {
	var oses []string
	for _, r := range runners {
		oses = append(oses, r.goos)
	}
	return oses
}

// A fileVersions returns a file's content at the change base and at the head;
// ok is false on a side where the file does not exist.
type fileVersions func(file string) (base []byte, baseOK bool, head []byte, headOK bool, err error)

// goFileOSes returns the GOOS whose build includes a root module Go file on
// either side of the diff, so an added, edited or deleted file and a changed
// build constraint all count. The Go toolchain's own matcher (go/build)
// evaluates file name suffixes and //go:build lines with cgo on, as the race
// tests build. A file no runner builds, such as one behind a custom tag the
// tests set themselves, and any error select every OS.
func goFileOSes(file string, versions fileVersions) []string {
	base, baseOK, head, headOK, err := versions(file)
	if err != nil {
		return allOSes()
	}
	var oses []string
	for _, r := range runners {
		for _, side := range []struct {
			src []byte
			ok  bool
		}{{base, baseOK}, {head, headOK}} {
			if !side.ok {
				continue
			}
			match, err := buildsOn(r, file, side.src)
			if err != nil {
				return allOSes()
			}
			if match {
				oses = append(oses, r.goos)
				break
			}
		}
	}
	if len(oses) == 0 {
		return allOSes()
	}
	return oses
}

func buildsOn(r runner, file string, src []byte) (bool, error) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = r.goos, r.goarch, true
	ctx.BuildTags = nil
	ctx.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(src)), nil }
	dir, name := path.Split(file)
	return ctx.MatchFile(dir, name)
}

// narrowsOSes reports whether a file's OSes come from its build constraints:
// a Go file of the root module outside tools/dev, which changes how CI checks.
// Every other file that selects tests, Go files of the mock module and
// fixtures included, selects every OS.
func narrowsOSes(f string) bool {
	return strings.HasSuffix(f, ".go") && !strings.Contains("/"+path.Dir(f)+"/", "/testdata/") &&
		!strings.HasPrefix(f, mockModule+"/") && !strings.HasPrefix(f, "tools/dev/") && toolModule(f) == ""
}
