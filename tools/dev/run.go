package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// targets are the systems tfsaz supports. Vet and lint type-check each one
// from any host, so a change to a _windows.go file is checked on macOS too.
var targets = []string{"darwin", "linux", "windows"}

// tools are the pinned check tools, each in its module directory under tools/.
var tools = []string{"golangci-lint", "govulncheck", "deadcode", "actionlint", "gitleaks"}

// toolModules adds act, which only ci-local uses; setup does not download or
// build it, because its Docker client dependencies are large.
func toolModules() []string { return append(slices.Clone(tools), "act") }

// repoChecksRun selects the root-package tests that check the whole
// repository: doc links and anchors, em dashes in markdown, pinned actions.
const repoChecksRun = "TestDocLinks|TestNoEmDashInMarkdown|TestPinnedActions"

// A step is one check. A gate fails the command; a report only prints.
type step struct {
	name   string
	report bool
	run    func(out *bytes.Buffer) error
}

func cmd(name string, args ...string) func(*bytes.Buffer) error {
	return func(out *bytes.Buffer) error { return runInto(out, nil, name, args...) }
}

func runInto(out *bytes.Buffer, env []string, name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdout, c.Stderr = out, out
	if env != nil {
		c.Env = append(os.Environ(), env...)
	}
	return c.Run()
}

// toolPath builds a pinned tool for the host and returns its cached
// executable. Running that file directly, rather than through go tool, lets
// GOOS select the target the tool analyzes instead of the one it is built for.
func toolPath(name string) (string, error) {
	c := exec.Command("go", "tool", "-modfile="+toolModfile(name), "-n", name)
	c.Env = append(os.Environ(), "GOOS=", "GOARCH=")
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("build %s: %v\n%s", name, err, stderr.String())
	}
	return strings.TrimSpace(string(out)), nil
}

func toolModfile(name string) string {
	return filepath.Join("tools", name, "go.mod")
}

func tool(name string, args ...string) func(*bytes.Buffer) error {
	return toolFor("", name, args...)
}

func toolFor(goos, name string, args ...string) func(*bytes.Buffer) error {
	return func(out *bytes.Buffer) error {
		path, err := toolPath(name)
		if err != nil {
			return err
		}
		var env []string
		if goos != "" {
			env = []string{"GOOS=" + goos}
		}
		return runInto(out, env, path, args...)
	}
}

// eachTarget runs one step body per supported system, one after another, so
// parallel golangci-lint runs do not wait on each other's lock.
func eachTarget(body func(goos string) func(*bytes.Buffer) error) func(*bytes.Buffer) error {
	return func(out *bytes.Buffer) error {
		var failed []string
		for _, goos := range targets {
			fmt.Fprintf(out, "-- GOOS=%s\n", goos)
			if err := body(goos)(out); err != nil {
				fmt.Fprintf(out, "%v\n", err)
				failed = append(failed, goos)
			}
		}
		if len(failed) > 0 {
			return fmt.Errorf("failed for %s", strings.Join(failed, ", "))
		}
		return nil
	}
}

func goEnv(goos string, args ...string) func(*bytes.Buffer) error {
	return func(out *bytes.Buffer) error {
		return runInto(out, []string{"GOOS=" + goos}, "go", args...)
	}
}

type result struct {
	step
	err      error
	duration time.Duration
	output   string
}

// runSteps runs all steps in parallel and prints each as it finishes: a
// passing gate as one line, a failing gate and every report with its output.
func runSteps(title string, steps []step) bool {
	if len(steps) == 0 {
		fmt.Printf("%s: nothing to check.\n", title)
		return true
	}
	started := time.Now()
	results := make(chan result)
	var wg sync.WaitGroup
	for _, s := range steps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out bytes.Buffer
			t := time.Now()
			err := s.run(&out)
			results <- result{s, err, time.Since(t), out.String()}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	var failed, reports []string
	for r := range results {
		status := "ok"
		switch {
		case r.report:
			status = "report"
			reports = append(reports, r.name)
		case r.err != nil:
			status = "FAIL"
			failed = append(failed, r.name)
		}
		fmt.Printf("%-6s %s (%.1fs)\n", status, r.name, r.duration.Seconds())
		if r.report || r.err != nil {
			if text := strings.TrimRight(r.output, "\n"); text != "" {
				fmt.Println(indent(text))
			}
			if r.report && r.err != nil {
				fmt.Println(indent(fmt.Sprintf("(exit: %v)", r.err)))
			}
		}
	}
	fmt.Printf("%s: %d steps in %.1fs", title, len(steps), time.Since(started).Seconds())
	if len(reports) > 0 {
		fmt.Printf(", reports printed for %s", strings.Join(reports, ", "))
	}
	if len(failed) > 0 {
		fmt.Printf(", FAILED: %s\n", strings.Join(failed, ", "))
		return false
	}
	fmt.Println(", all gates passed.")
	return true
}

func indent(text string) string {
	return "    " + strings.ReplaceAll(text, "\n", "\n    ")
}

// raceSupported reports whether go test -race can run here: it needs cgo and
// a C compiler, which Windows x64 gets only from MinGW-w64.
func raceSupported() (bool, string) {
	out, err := exec.Command("go", "env", "CGO_ENABLED", "CC").Output()
	if err != nil {
		return false, "go env failed"
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "1" {
		return false, "cgo is disabled"
	}
	if _, err := exec.LookPath(fields[1]); err != nil {
		return false, fmt.Sprintf("C compiler %q not found", fields[1])
	}
	return true, ""
}
