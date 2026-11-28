package main

import (
	"bytes"
	"go/build"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io"
	"path"
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
// ok is false on a side where the file does not exist. An error, such as an
// unreadable blob or a file another package embeds, makes the file select
// every OS.
type fileVersions func(file string) (base []byte, baseOK bool, head []byte, headOK bool, err error)

// narrowTags are the build tags whose meaning on each runner go/build knows
// from GOOS, GOARCH and cgo alone. Any other tag in a constraint (race,
// tfsaztest, a go1.N version or an architecture feature like arm64.v8.0)
// depends on how the tests build or on the target toolchain, so the file
// selects every OS.
var narrowTags = map[string]bool{"darwin": true, "linux": true, "windows": true, "unix": true, "amd64": true, "arm64": true, "cgo": true}

// goFileOSes returns the GOOS whose build includes a root module Go file on
// either side of the diff, so an added, edited or deleted file and a changed
// build constraint all count. The Go toolchain's own matcher (go/build)
// evaluates file name suffixes and build constraints with cgo on, as the race
// tests build. A file no runner builds, a tag outside narrowTags and any error
// select every OS.
func goFileOSes(file string, versions fileVersions) []string {
	base, baseOK, head, headOK, err := versions(file)
	if err != nil {
		return allOSes()
	}
	type side struct {
		src []byte
		ok  bool
	}
	sides := []side{{base, baseOK}, {head, headOK}}
	for _, sd := range sides {
		if sd.ok && !onlyNarrowTags(sd.src) {
			return allOSes()
		}
	}
	var oses []string
	for _, r := range runners {
		match := false
		for _, sd := range sides {
			if !sd.ok {
				continue
			}
			m, err := buildsOn(r, file, sd.src)
			if err != nil {
				return allOSes()
			}
			match = match || m
		}
		if match {
			oses = append(oses, r.goos)
		}
	}
	if len(oses) == 0 {
		return allOSes()
	}
	return oses
}

// onlyNarrowTags reports whether the file parses as Go and every build
// constraint comment before its package clause uses only narrowTags. Go's
// parser finds the header comments, so a byte order mark or a block comment
// cannot hide a constraint; taking every line comment there is a superset of
// what go/build reads.
func onlyNarrowTags(src []byte) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return false
	}
	for _, group := range f.Comments {
		if group.Pos() > f.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) && !constraint.IsPlusBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil || !narrowExpr(expr) {
				return false
			}
		}
	}
	return true
}

func narrowExpr(x constraint.Expr) bool {
	switch x := x.(type) {
	case *constraint.TagExpr:
		return narrowTags[x.Tag]
	case *constraint.NotExpr:
		return narrowExpr(x.X)
	case *constraint.AndExpr:
		return narrowExpr(x.X) && narrowExpr(x.Y)
	case *constraint.OrExpr:
		return narrowExpr(x.X) && narrowExpr(x.Y)
	}
	return false
}

func buildsOn(r runner, file string, src []byte) (bool, error) {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled = r.goos, r.goarch, true
	ctx.BuildTags, ctx.ToolTags = nil, nil
	ctx.OpenFile = func(string) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(src)), nil }
	dir, name := path.Split(file)
	return ctx.MatchFile(dir, name)
}
