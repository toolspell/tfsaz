package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

var gitleaksFlags = []string{"--config", ".gitleaks.toml", "--redact", "--no-banner", "--exit-code", "1"}

// gitleaksArgs adds --verbose locally, which prints each finding redacted with
// its file and commit. CI leaves it out: a finding there is already public,
// and the log shows only the count.
func gitleaksArgs(verbose bool) []string {
	if verbose {
		return append(slices.Clone(gitleaksFlags), "--verbose")
	}
	return gitleaksFlags
}

// lint fails on any golangci-lint finding in the tree. A deliberate exception
// is a rule in .golangci.yml or a //nolint, each with its reason.
func lint() step {
	return step{name: "golangci-lint", run: eachTarget(func(goos string) func(*bytes.Buffer) error {
		return toolFor(goos, "golangci-lint", "run", "--allow-parallel-runners", "--max-issues-per-linter=0", "--max-same-issues=0", "./...")
	})}
}

func vet() step {
	return step{name: "go vet", run: eachTarget(func(goos string) func(*bytes.Buffer) error {
		return goEnv(goos, "vet", "./...")
	})}
}

func tidy(modules []string) []step {
	var steps []step
	for _, m := range modules {
		steps = append(steps, step{name: "go mod tidy -diff " + m, run: cmd("go", "-C", m, "mod", "tidy", "-diff")})
	}
	return steps
}

func actionlint() step {
	return step{name: "actionlint", run: tool("actionlint")}
}

// gitleaksChanges scans commits in logRange (all history when empty), then
// staged, unstaged and untracked changes.
func gitleaksChanges(logRange string, scanLog, verbose bool) step {
	return step{name: "gitleaks", run: func(out *bytes.Buffer) error {
		var scans [][]string
		if scanLog {
			args := []string{"git"}
			if logRange != "" {
				args = append(args, "--log-opts="+logRange)
			}
			scans = append(scans, args)
		}
		scans = append(scans, []string{"git", "--pre-commit"}, []string{"git", "--pre-commit", "--staged"})
		untracked, err := untrackedFiles()
		if err != nil {
			return err
		}
		for _, f := range untracked {
			scans = append(scans, []string{"dir", f})
		}
		var failed error
		for _, s := range scans {
			args := append(slices.Clone(s), gitleaksArgs(verbose)...)
			if s[0] == "git" {
				args = append(args, ".")
			}
			if err := tool("gitleaks", args...)(out); err != nil {
				failed = fmt.Errorf("gitleaks %s: %v", strings.Join(s, " "), err)
			}
		}
		return failed
	}}
}

func check(base string) bool {
	files, mergeBase, err := changedFiles(base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check:", err)
		return false
	}
	head, _ := git("rev-parse", "HEAD")
	onBase := mergeBase == strings.TrimSpace(head)
	if onBase {
		fmt.Printf("check: HEAD is at the merge base with %s; checking uncommitted changes only.\n", base)
	}
	if len(files) == 0 {
		fmt.Println("check: no changes.")
		return true
	}
	root, _ := os.Getwd()
	pkgDirs, err := packageDirs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "check:", err)
		return false
	}
	p := planFor(files, fileExists, pkgDirs)

	var steps []step
	if len(p.goFiles) > 0 {
		steps = append(steps, step{name: "gofmt", run: gofmtCheck(p.goFiles, readWorktree)})
	}
	if p.goChanged {
		steps = append(steps, vet(), lint())
	}
	steps = append(steps, tidy(p.modules)...)
	if p.workflows {
		steps = append(steps, actionlint())
	}
	steps = append(steps,
		gitleaksChanges(mergeBase+"..HEAD", !onBase, true),
		step{name: "go test " + strings.Join(p.packages, " "), run: cmd("go", append([]string{"test", "-count=1"}, p.packages...)...)},
	)
	fmt.Printf("check: %d changed files since %s (%s).\n", len(files), base, short(mergeBase))
	return runSteps("check", steps)
}

func ready() bool {
	steps, err := treeSteps(false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ready:", err)
		return false
	}
	test := []string{"test", "-count=1"}
	testName := "go test -race ./..."
	if ok, why := raceSupported(); ok {
		test = append(test, "-race")
	} else {
		testName = "go test ./... (no -race: " + why + ")"
	}
	steps = append(steps, step{name: testName, run: cmd("go", append(test, "./...")...)})
	return runSteps("ready", steps)
}

// treeSteps are the whole-tree checks that ready and ci-checks share.
func treeSteps(ci bool) ([]step, error) {
	goFiles, err := gitPaths("ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go")
	if err != nil {
		return nil, err
	}
	goFiles = slices.DeleteFunc(goFiles, func(f string) bool { return !fileExists(f) })
	modules := []string{"."}
	for _, name := range toolModules() {
		modules = append(modules, "tools/"+name)
	}
	steps := []step{
		{name: "gofmt", run: gofmtCheck(goFiles, readWorktree)},
		vet(),
		actionlint(),
		gitleaksChanges("", true, !ci),
		{name: "govulncheck", run: eachTarget(func(goos string) func(*bytes.Buffer) error {
			return toolFor(goos, "govulncheck", "./...")
		})},
		lint(),
		{name: "deadcode", report: true, run: eachTarget(func(goos string) func(*bytes.Buffer) error {
			return toolFor(goos, "deadcode", "./...")
		})},
	}
	return append(steps, tidy(modules)...), nil
}

func staged() bool {
	files, err := stagedFiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pre-commit:", err)
		return false
	}
	var goFiles []string
	for _, f := range files {
		if strings.HasSuffix(f, ".go") {
			goFiles = append(goFiles, f)
		}
	}
	// The naming check reads every text file, so any staged file can fail it.
	repoChecks := len(files) > 0
	steps := []step{{name: "gitleaks --staged", run: tool("gitleaks", append([]string{"git", "--pre-commit", "--staged"}, append(gitleaksArgs(true), ".")...)...)}}
	if len(goFiles) > 0 {
		steps = append(steps, step{name: "gofmt (staged)", run: gofmtCheck(goFiles, readStaged)})
	}
	if repoChecks {
		steps = append(steps, step{name: "repo checks", run: cmd("go", "test", "-count=1", "-run", repoChecksRun, ".")})
	}
	return runSteps("pre-commit", steps)
}

func setup(buildTools bool) bool {
	run := func(name string, args ...string) bool {
		fmt.Printf("setup: %s %s\n", name, strings.Join(args, " "))
		c := exec.Command(name, args...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "setup: %v\n", err)
			return false
		}
		return true
	}
	ok := run("git", "config", "core.hooksPath", ".githooks")
	ok = run("go", "mod", "download") && ok
	for _, name := range tools {
		ok = run("go", "-C", "tools/"+name, "mod", "download") && ok
	}
	if buildTools {
		for _, name := range tools {
			fmt.Printf("setup: build %s\n", name)
			if _, err := toolPath(name); err != nil {
				fmt.Fprintf(os.Stderr, "setup: %v\n", err)
				ok = false
			}
		}
	}
	if ok {
		fmt.Println("setup: done.")
	}
	return ok
}

// undoSetup removes the core.hooksPath that setup sets. A value setup did not
// write is left alone. Downloaded modules and built tools stay in Go's caches.
func undoSetup() bool {
	out, err := exec.Command("git", "config", "--local", "--get", "core.hooksPath").Output()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		fmt.Println("setup -undo: core.hooksPath is not set; nothing to undo.")
		return true
	case err != nil:
		fmt.Fprintf(os.Stderr, "setup -undo: git config --get core.hooksPath: %v\n", err)
		return false
	}
	if value := strings.TrimSpace(string(out)); value != ".githooks" {
		fmt.Printf("setup -undo: core.hooksPath is %q, not set by setup; left unchanged.\n", value)
		return true
	}
	fmt.Println("setup -undo: git config --local --unset core.hooksPath")
	c := exec.Command("git", "config", "--local", "--unset", "core.hooksPath")
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "setup -undo: %v\n", err)
		return false
	}
	fmt.Println("setup -undo: done.")
	return true
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
