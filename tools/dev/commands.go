package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

var gitleaksFlags = []string{"--config", ".gitleaks.toml", "--redact", "--no-banner", "--verbose", "--exit-code", "1"}

// lintNew fails on lint issues in lines changed since the merge base with
// base. The existing code has findings that the current phase may not fix
// (docs/backlog.md); this keeps them out of the gate without hiding new ones.
func lintNew(base string) step {
	return step{name: "golangci-lint, new issues", run: eachTarget(func(goos string) func(*bytes.Buffer) error {
		return toolFor(goos, "golangci-lint", "run", "--allow-parallel-runners", "--new-from-merge-base="+base, "./...")
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
func gitleaksChanges(logRange string, scanLog bool) step {
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
			args := append(slices.Clone(s), gitleaksFlags...)
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
		steps = append(steps, vet(), lintNew(base))
	}
	steps = append(steps, tidy(p.modules)...)
	if p.workflows {
		steps = append(steps, actionlint())
	}
	steps = append(steps,
		gitleaksChanges(mergeBase+"..HEAD", !onBase),
		step{name: "go test " + strings.Join(p.packages, " "), run: cmd("go", append([]string{"test", "-count=1"}, p.packages...)...)},
	)
	fmt.Printf("check: %d changed files since %s (%s).\n", len(files), base, short(mergeBase))
	return runSteps("check", steps)
}

func ready() bool {
	goFiles, err := gitPaths("ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ready:", err)
		return false
	}
	goFiles = slices.DeleteFunc(goFiles, func(f string) bool { return !fileExists(f) })
	test := []string{"test", "-count=1"}
	testName := "go test -race ./..."
	if ok, why := raceSupported(); ok {
		test = append(test, "-race")
	} else {
		testName = "go test ./... (no -race: " + why + ")"
	}
	modules := []string{"."}
	for _, name := range tools {
		modules = append(modules, "tools/"+name)
	}
	steps := []step{
		{name: "gofmt", run: gofmtCheck(goFiles, readWorktree)},
		vet(),
		lintNew("main"),
		actionlint(),
		gitleaksChanges("", true),
		{name: testName, run: cmd("go", append(test, "./...")...)},
		{name: "golangci-lint, all findings", report: true, run: eachTarget(lintSummary)},
		{name: "govulncheck", report: true, run: eachTarget(func(goos string) func(*bytes.Buffer) error {
			return toolFor(goos, "govulncheck", "./...")
		})},
		{name: "deadcode", report: true, run: eachTarget(func(goos string) func(*bytes.Buffer) error {
			return toolFor(goos, "deadcode", "./...")
		})},
	}
	steps = append(steps, tidy(modules)...)
	return runSteps("ready", steps)
}

var lintSummaryStart = regexp.MustCompile(`(?m)^(\d+ issues:|0 issues\.)`)

// lintSummary prints only golangci-lint's per-linter counts; the full list is
// one `golangci-lint run` away and would bury the other results.
func lintSummary(goos string) func(*bytes.Buffer) error {
	return func(out *bytes.Buffer) error {
		var full bytes.Buffer
		err := toolFor(goos, "golangci-lint", "run", "--allow-parallel-runners", "--max-issues-per-linter=0", "--max-same-issues=0", "./...")(&full)
		text := full.String()
		if loc := lintSummaryStart.FindStringIndex(text); loc != nil {
			out.WriteString(text[loc[0]:])
			return err
		}
		out.WriteString(text)
		return err
	}
}

func staged() bool {
	files, err := stagedFiles()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pre-commit:", err)
		return false
	}
	var goFiles []string
	repoChecks := false
	for _, f := range files {
		if strings.HasSuffix(f, ".go") {
			goFiles = append(goFiles, f)
		}
		if strings.HasSuffix(f, ".go") || strings.HasSuffix(f, ".md") || strings.HasPrefix(f, ".github/") {
			repoChecks = true
		}
	}
	steps := []step{{name: "gitleaks --staged", run: tool("gitleaks", append([]string{"git", "--pre-commit", "--staged"}, append(gitleaksFlags, ".")...)...)}}
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

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
