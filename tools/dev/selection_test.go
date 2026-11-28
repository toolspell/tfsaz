package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testPkgDirs are the root module's package directories as go list reports
// them for this repository.
var testPkgDirs = []string{".", "internal/az", "live-tests/harness", "tools/dev", "tools/live", "tools/syncrepos"}

var (
	all       = []string{"./..."}
	toolsPkgs = []string{".", "./live-tests/harness/...", "./tools/..."}
	livePkgs  = []string{".", "./tools/live"}
)

// selectionCase is one row of the coverage contract in
// docs/operations/development.md#path-filter: the changed files and what CI
// runs for them.
type selectionCase struct {
	name     string
	files    []string
	packages []string
	shards   int
	goGates  bool
	mock     bool
}

// contractRows is how many of selectionCases are the one-per-row cases.
const contractRows = 9

// selectionCases has one case per contract row, in the contract's order,
// then the cases for every path class and union.
var selectionCases = []selectionCase{
	{"non-code", []string{"README.md", "docs/user/install.md", ".agents/context-budget.json", ".github/dependabot.yml"}, nil, 0, false, false},
	{"lint config and tool pins", []string{".golangci.yml", "tools/golangci-lint/go.sum"}, nil, 0, true, false},
	{"root module code", []string{"internal/az/cli.go"}, all, 3, true, false},
	{"CI tooling", []string{"tools/dev/ci.go"}, all, 3, true, true},
	{"other root module tools", []string{"tools/live/help.go"}, toolsPkgs, 1, true, false},
	{"live-test files", []string{"live-tests/router/Dockerfile"}, livePkgs, 1, false, false},
	{"testdata", []string{"tools/live/testdata/x.json"}, []string{".", "./tools/live"}, 1, true, false},
	{"go.mod", []string{"go.mod"}, all, 3, true, true},
	{"unknown file", []string{".gitignore"}, all, 3, true, true},

	// Every path class of the contract, with more representative paths.
	{"non-code markdown at root and nested", []string{"README.md", "AGENTS.md", "SECURITY.md", "internal/az/README.md", "tools/live/notes.md", "live-tests/router/README.md", "live-tests/devops-server-mock/README.md"}, nil, 0, false, false},
	{"non-code docs", []string{"docs/user/install.md", "docs/internals/testing.md", "docs/diagram.png"}, nil, 0, false, false},
	{"non-code agents", []string{".agents/skills/live-tests/SKILL.md", ".agents/context-budget.json"}, nil, 0, false, false},
	{"non-code .claude directory", []string{".claude/settings.json", ".claude/skills/x/SKILL.md"}, nil, 0, false, false},
	{"non-code .claude link", []string{".claude"}, nil, 0, false, false},
	{"non-code githooks", []string{".githooks/pre-commit"}, nil, 0, false, false},
	{"non-code github scripts", []string{".github/scripts/check.sh", ".github/scripts/nested/x.js"}, nil, 0, false, false},
	{"non-code review bots", []string{".macroscope/rules.md", ".macroscope/macroscope.yml", ".coderabbit.config.ts"}, nil, 0, false, false},
	{"non-code templates", []string{".github/ISSUE_TEMPLATE/bug.yml", ".github/ISSUE_TEMPLATE/config.yml", ".github/pull_request_template.md"}, nil, 0, false, false},
	{"non-code repository settings", []string{".github/labels.json", ".github/dependabot.yml", ".gitleaks.toml"}, nil, 0, false, false},

	{"tool module golangci-lint", []string{"tools/golangci-lint/go.mod", "tools/golangci-lint/tools.go"}, nil, 0, true, false},
	{"tool module govulncheck", []string{"tools/govulncheck/go.mod"}, nil, 0, true, false},
	{"tool module deadcode", []string{"tools/deadcode/go.sum"}, nil, 0, true, false},
	{"tool module actionlint", []string{"tools/actionlint/go.mod"}, nil, 0, true, false},
	{"tool module gitleaks", []string{"tools/gitleaks/go.sum"}, nil, 0, true, false},
	{"tool module act", []string{"tools/act/go.mod"}, nil, 0, true, false},

	{"internal non-Go file", []string{"internal/az/embedded.json"}, all, 3, true, false},
	{"internal nested Go file", []string{"internal/az/sub/x.go"}, all, 3, true, false},
	{"root Go file", []string{"main.go", "repo_checks_test.go"}, all, 3, true, false},
	{"go.sum", []string{"go.sum"}, all, 3, true, false},

	{"tools/dev", []string{"tools/dev/selection.go"}, all, 3, true, true},
	{"tools/dev fixture", []string{"tools/dev/testdata/x.json"}, all, 3, true, true},
	{"workflow fixture", []string{".github/workflows/testdata/x.yml"}, all, 3, true, true},
	{"unknown file under tools", []string{"tools/ci-env.sh"}, all, 3, true, true},
	{"unknown file in a removed tools directory", []string{"tools/old/main.go"}, all, 3, true, true},
	{"workflows", []string{".github/workflows/test.yml", ".github/workflows/labels.yml"}, all, 3, true, true},

	{"tools/syncrepos", []string{"tools/syncrepos/main.go"}, toolsPkgs, 1, true, false},
	{"live-tests/harness", []string{"live-tests/harness/harness.go", "live-tests/harness/sub/x.go"}, toolsPkgs, 1, true, false},

	{"live-tests router", []string{"live-tests/router/Dockerfile"}, livePkgs, 1, false, false},
	{"live-tests dns", []string{"live-tests/dns/zone.conf"}, livePkgs, 1, false, false},
	{"live-tests compose", []string{"live-tests/compose.yaml"}, livePkgs, 1, false, false},
	{"live-tests vms", []string{"live-tests/vms/linux.pkr.hcl"}, livePkgs, 1, false, false},

	{"mock Go file", []string{"live-tests/devops-server-mock/main.go"}, livePkgs, 1, false, true},
	{"mock non-Go file", []string{"live-tests/devops-server-mock/Dockerfile", "live-tests/devops-server-mock/go.mod"}, livePkgs, 1, false, true},
	{"mock testdata", []string{"live-tests/devops-server-mock/testdata/x.json"}, livePkgs, 1, false, true},
	{"mock markdown is non-code", []string{"live-tests/devops-server-mock/docs.md"}, nil, 0, false, false},

	{"testdata internal/az", []string{"internal/az/testdata/fixture.json", "internal/az/testdata/sub/x.txt"}, []string{".", "./internal/az"}, 3, true, false},
	{"testdata live-tests/harness", []string{"live-tests/harness/testdata/x.json"}, []string{".", "./live-tests/harness"}, 1, true, false},
	{"testdata root", []string{"testdata/x.json"}, []string{"."}, 1, true, false},
	{"testdata markdown is not non-code", []string{"internal/az/testdata/x.md"}, []string{".", "./internal/az"}, 3, true, false},
	{"testdata root markdown is not non-code", []string{"testdata/x.md"}, []string{"."}, 1, true, false},
	{"testdata outside any package", []string{"scripts/testdata/x.json"}, all, 3, true, true},

	// Unions of several rows.
	{"union docs and tools/live", []string{"docs/user/install.md", "tools/live/help.go"}, toolsPkgs, 1, true, false},
	{"union tools/live and internal/az", []string{"tools/live/help.go", "internal/az/cli.go"}, all, 3, true, false},
	{"union mock and harness", []string{"live-tests/devops-server-mock/main.go", "live-tests/harness/harness.go"}, []string{".", "./live-tests/harness/...", "./tools/...", "./tools/live"}, 1, true, true},
	{"union lint config and docs", []string{".golangci.yml", "docs/user/install.md"}, nil, 0, true, false},
	{"union router and mock", []string{"live-tests/router/Dockerfile", "live-tests/devops-server-mock/main.go"}, livePkgs, 1, false, true},
	{"union CI tooling and docs", []string{"tools/dev/ci.go", "README.md"}, all, 3, true, true},
	{"union internal/az and harness testdata", []string{"internal/az/testdata/a.json", "live-tests/harness/testdata/b.json"}, []string{".", "./internal/az", "./live-tests/harness"}, 3, true, false},
	{"union tool module and live-tests", []string{"tools/gitleaks/go.mod", "live-tests/dns/zone.conf"}, livePkgs, 1, true, false},
	{"union non-code only", []string{"docs/a.md", ".githooks/pre-commit", ".gitleaks.toml"}, nil, 0, false, false},
	{"union unknown file wins", []string{"docs/a.md", ".gitignore"}, all, 3, true, true},
}

func (tc selectionCase) check(t *testing.T, got selection) {
	t.Helper()
	if !slices.Equal(got.packages, tc.packages) || got.shards != tc.shards || got.goGates != tc.goGates || got.mock != tc.mock {
		t.Errorf("files %q: got packages %q, %d shards, Go gates %v, mock %v; want %q, %d, %v, %v",
			tc.files, got.packages, got.shards, got.goGates, got.mock, tc.packages, tc.shards, tc.goGates, tc.mock)
	}
}

func TestSelectFor(t *testing.T) {
	for _, tc := range selectionCases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, selectFor(tc.files, testPkgDirs, nil)) })
	}
}

// gitFixture is a throwaway repository in a temporary directory that the test
// works in. Its first commit is the base every later change is diffed against.
type gitFixture struct {
	t    *testing.T
	dir  string
	base string
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	dir := t.TempDir()
	isolateGit(t)
	// ci-local runs these tests under act, which turns OS narrowing off.
	t.Setenv("ACT", "")
	t.Chdir(dir)
	f := &gitFixture{t: t, dir: dir}
	f.git("init", "--quiet", "--initial-branch=main")
	f.commit(map[string]string{"README.md": "base\n"})
	f.base = f.git("rev-parse", "HEAD")
	return f
}

func (f *gitFixture) git(args ...string) string {
	f.t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@localhost", "-c", "commit.gpgsign=false"}, args...)...)
	c.Dir = f.dir
	out, err := c.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// deleted as a file's content in commit removes the file; an empty string is
// an empty file.
const deleted = "\x00deleted"

// commit writes files (deleted removes one) and commits them, returning the
// new head.
func (f *gitFixture) commit(files map[string]string) string {
	f.t.Helper()
	for name, content := range files {
		p := filepath.Join(f.dir, filepath.FromSlash(name))
		if content == deleted {
			if err := os.Remove(p); err != nil {
				f.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	f.git("add", "-A")
	f.git("commit", "--quiet", "--allow-empty", "-m", "change")
	return f.git("rev-parse", "HEAD")
}

// changes commits files and returns the selection against base and its reason.
func (f *gitFixture) changes(base string, files ...string) (selection, string) {
	f.t.Helper()
	content := map[string]string{}
	for _, name := range files {
		content[name] = "change\n"
	}
	f.commit(content)
	return changeSelection(base, func() ([]string, error) { return testPkgDirs, nil })
}

func TestChangeSelection(t *testing.T) {
	// One case per contract row through a real diff; TestSelectFor covers
	// the rest without a fixture.
	for _, tc := range selectionCases[:contractRows] {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitFixture(t)
			got, why := f.changes(f.base, tc.files...)
			if !strings.Contains(why, "changed files since") {
				t.Errorf("reason %q, want a diff", why)
			}
			tc.check(t, got)
		})
	}
	t.Run("no base fails open", func(t *testing.T) {
		f := newGitFixture(t)
		got, why := f.changes("", "README.md")
		if why != "no base commit" || !slices.Equal(got.packages, all) || !got.mock {
			t.Errorf("got %+v, %q", got, why)
		}
	})
}
