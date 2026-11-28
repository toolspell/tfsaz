package main

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// windowsShards is how many runners split the Windows tests when internal/az
// runs: its Git-heavy tests take about five times as long there as on Ubuntu.
const windowsShards = 3

// A selection is what CI runs for a change: the root module packages the test
// matrix tests (none skips it, "./..." is everything), the Windows shard
// count, whether Checks runs the Go gates and whether the Mock job runs. The
// coverage contract in docs/operations/development.md#path-filter defines it.
type selection struct {
	packages []string
	oses     []string // the GOOS the matrix tests on, in runner order
	shards   int
	goGates  bool
	mock     bool
}

// everything is the fail-open selection: all tests, all gates.
func everything() selection {
	return selection{packages: []string{"./..."}, oses: allOSes(), shards: windowsShards, goGates: true, mock: true}
}

func (s selection) tests() bool { return len(s.packages) > 0 }

// selectFor maps changed files to a selection. pkgDirs are the root module's
// package directories relative to the root, "." for the root package. A file
// nobody classified selects everything, so a new kind of file runs all of CI
// until it is listed here. versions reads a file at the base and the head to
// narrow the OSes; nil tests on every OS.
func selectFor(files []string, pkgDirs []string, versions fileVersions) selection {
	var s selection
	pkgs := map[string]bool{}
	oses := map[string]bool{}
	all := false
	for _, f := range files {
		c := classify(f, pkgDirs)
		all = all || c.all
		for _, p := range c.packages {
			pkgs[p] = true
		}
		if c.all || len(c.packages) > 0 {
			fileOSes := allOSes()
			if versions != nil && narrowsOSes(f) {
				fileOSes = goFileOSes(f, versions)
			}
			for _, goos := range fileOSes {
				oses[goos] = true
			}
		}
		s.shards = max(s.shards, c.shards)
		s.goGates = s.goGates || c.goGates
		s.mock = s.mock || c.mock
	}
	switch {
	case all:
		s.packages = []string{"./..."}
	case len(pkgs) > 0:
		// The root package always runs with the matrix: it imports
		// internal/az, and its repo checks read the whole tree.
		pkgs["."] = true
		for p := range pkgs {
			s.packages = append(s.packages, p)
		}
		slices.Sort(s.packages)
	}
	for _, goos := range allOSes() {
		if oses[goos] {
			s.oses = append(s.oses, goos)
		}
	}
	if !s.tests() || !oses["windows"] {
		s.shards = 0
	}
	return s
}

// A class is what one changed file selects.
type class struct {
	all      bool     // every root module package
	packages []string // or these
	shards   int
	goGates  bool
	mock     bool
}

func classify(f string, pkgDirs []string) class {
	full := class{all: true, shards: windowsShards, goGates: true}
	inTestdata := strings.Contains("/"+path.Dir(f)+"/", "/testdata/")
	switch {
	case nonCode(f):
		return class{}
	case f == ".golangci.yml" || toolModule(f) != "":
		return class{goGates: true}
	case strings.HasPrefix(f, mockModule+"/"):
		// The mock's own gates run in the Mock job; tools/live's tests read
		// live-tests/.
		return class{packages: []string{".", "./tools/live"}, shards: 1, mock: true}
	case strings.HasPrefix(f, "tools/dev/"), strings.HasPrefix(f, ".github/workflows/"):
		full.mock = true
		return full
	case inTestdata:
		dir := packageOf(f, pkgDirs)
		if dir == "" && strings.HasPrefix(f, "testdata/") {
			dir = "."
		}
		if dir == "" {
			full.mock = true
			return full
		}
		c := class{packages: []string{pkgPattern(dir)}, shards: 1, goGates: true}
		if strings.HasPrefix(f, "internal/") {
			c.shards = windowsShards
		}
		return c
	case f == "go.mod":
		// Its go line is the Go version every job, Mock included, sets up.
		full.mock = true
		return full
	case strings.HasPrefix(f, "internal/"), f == "go.sum",
		!strings.Contains(f, "/") && strings.HasSuffix(f, ".go"):
		return full
	case strings.HasPrefix(packageOf(f, pkgDirs), "tools/"), strings.HasPrefix(f, "live-tests/harness/"):
		return class{packages: []string{".", "./tools/...", "./live-tests/harness/..."}, shards: 1, goGates: true}
	case strings.HasPrefix(f, "live-tests/"):
		return class{packages: []string{".", "./tools/live"}, shards: 1}
	}
	full.mock = true
	return full
}

func pkgPattern(dir string) string {
	if dir == "." {
		return "."
	}
	return "./" + dir
}

// nonCode reports files no test, Go gate or mock check reads: the docs, agent
// and review bot files, templates and repository settings, and the actionlint
// config, which only actionlint reads and Checks runs it on every change. The root package's
// repo checks read them, and Checks runs those on every change. Markdown under
// testdata/ is a fixture.
func nonCode(f string) bool {
	if strings.Contains("/"+path.Dir(f)+"/", "/testdata/") {
		return false
	}
	for _, prefix := range []string{"docs/", ".agents/", ".claude/", ".githooks/", ".macroscope/", ".github/ISSUE_TEMPLATE/", ".github/scripts/"} {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	switch f {
	case ".claude", ".coderabbit.config.ts", ".github/pull_request_template.md", ".github/labels.json", ".github/dependabot.yml", ".github/actionlint.yaml", ".gitleaks.toml":
		return true
	}
	return strings.HasSuffix(f, ".md")
}

// matrixEntry is one test job: a runner and its shard.
type matrixEntry struct {
	OS    string `json:"os"`
	Shard string `json:"shard"`
}

// matrix lists the test jobs on the selected OSes: macOS and Ubuntu once
// each, Windows in shards.
func (s selection) matrix() []matrixEntry {
	entries := []matrixEntry{}
	if !s.tests() {
		return entries
	}
	for _, r := range runners {
		if !slices.Contains(s.oses, r.goos) {
			continue
		}
		if r.goos != "windows" {
			entries = append(entries, matrixEntry{r.label, "1/1"})
			continue
		}
		for i := 1; i <= s.shards; i++ {
			entries = append(entries, matrixEntry{r.label, fmt.Sprintf("%d/%d", i, s.shards)})
		}
	}
	return entries
}
