package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// resolveBase returns rev as a commit SHA, or "" with the reason when there is
// nothing to compare against: a manual run, the first push of a branch (all
// zeros) or a force push whose old head this clone never fetched.
func resolveBase(rev string) (string, string) {
	if rev == "" || strings.Trim(rev, "0") == "" {
		return "", "no base commit"
	}
	out, err := git("rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", fmt.Sprintf("base %s is not in this clone", rev)
	}
	return strings.TrimSpace(out), ""
}

// testsNeeded reports whether a change can affect the native test matrix.
// Unknown files count as code, so a new kind of file runs the tests until it
// is listed here. The root package's repo checks read markdown and workflows;
// ci-checks runs those on every change.
func testsNeeded(files []string) bool {
	for _, f := range files {
		if !testIrrelevant(f) {
			return true
		}
	}
	return false
}

func testIrrelevant(f string) bool {
	if strings.Contains("/"+path.Dir(f)+"/", "/testdata/") {
		return false
	}
	if toolModule(f) != "" {
		return true
	}
	for _, prefix := range []string{"docs/", ".agents/", ".claude/", ".githooks/", ".github/ISSUE_TEMPLATE/"} {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	switch f {
	case ".claude", ".github/pull_request_template.md", ".github/labels.json", ".github/dependabot.yml", ".gitleaks.toml", ".golangci.yml":
		return true
	}
	return strings.HasSuffix(f, ".md")
}

// ciChanges writes tests=true|false for the workflow's test matrix. Every
// error fails open: the matrix runs and the log says why.
func ciChanges(base string) bool {
	tests, why := true, ""
	if sha, reason := resolveBase(base); reason != "" {
		why = reason
	} else if files, err := gitPaths("diff", "--name-only", "--no-renames", "-z", sha, "HEAD", "--"); err != nil {
		why = err.Error()
	} else if len(files) == 0 {
		why = "no changed files"
	} else {
		tests = testsNeeded(files)
		why = fmt.Sprintf("%d changed files since %s", len(files), short(sha))
	}
	fmt.Fprintf(os.Stderr, "ci-changes: %s; native tests needed: %v\n", why, tests)
	line := fmt.Sprintf("tests=%v\n", tests)
	fmt.Print(line)
	if out := os.Getenv("GITHUB_OUTPUT"); out != "" {
		if err := appendFile(out, line); err != nil {
			fmt.Fprintln(os.Stderr, "ci-changes:", err)
			return false
		}
	}
	return true
}

func appendFile(name, text string) error {
	f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, text)
	return errors.Join(err, f.Close())
}

// ciChecks is the CI checks job: the whole-tree gates and reports of ready,
// without the tests (the matrix runs those) but with the repo checks.
func ciChecks() bool {
	steps, err := treeSteps(true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ci-checks:", err)
		return false
	}
	steps = append(steps, step{name: "repo checks", run: cmd("go", "test", "-count=1", "-run", repoChecksRun, ".")})
	return runSteps("ci-checks", steps)
}

// actImage is a community image for the ubuntu-24.04 runner label; multi-arch,
// so it runs natively on arm64 hosts.
const actImage = "catthehacker/ubuntu:act-24.04"

// actArgs runs only the CI workflow: labels.yml would write to GitHub. Without
// a GITHUB_TOKEN secret act asks the gh CLI for the user's token, so it gets
// an empty one; the env, secret and var files are emptied the same way. act
// would run the macOS and Windows matrix entries in the Linux image, which
// proves nothing about those systems, so only the Ubuntu entry runs.
func actArgs(arch, eventPath string, remote bool, passthrough []string) []string {
	args := []string{
		"push",
		"--workflows", ".github/workflows/test.yml",
		"--eventpath", eventPath,
		"--secret", "GITHUB_TOKEN=",
		"--env-file", os.DevNull,
		"--secret-file", os.DevNull,
		"--var-file", os.DevNull,
		"--container-architecture", arch,
		"-P", "ubuntu-24.04=" + actImage,
		"-P", "ubuntu-latest=" + actImage,
		"--matrix", "os:ubuntu-latest",
	}
	if remote {
		// The cache server would listen on this machine, which a remote engine cannot reach.
		args = append(args, "--no-cache-server")
	}
	return append(args, passthrough...)
}

// ciLocal runs the CI workflow with act in Docker, as a push whose previous
// head is the merge base with base, so the gates see this branch's changes.
func ciLocal(base, context string, passthrough []string) bool {
	fail := func(err error) bool {
		fmt.Fprintln(os.Stderr, "ci-local:", err)
		return false
	}
	ctxArgs := []string{}
	if context != "" {
		ctxArgs = []string{"--context", context}
	}
	engineArch, err := output("docker", append(ctxArgs, "info", "--format", "{{.Architecture}}")...)
	if err != nil {
		return fail(err)
	}
	arch := "linux/amd64"
	if engineArch == "aarch64" || engineArch == "arm64" {
		arch = "linux/arm64"
	}
	inspect := []string{"context", "inspect", "--format", "{{.Endpoints.docker.Host}}"}
	if context != "" {
		inspect = append(inspect, context)
	}
	dockerHost, err := output("docker", inspect...)
	if err != nil {
		return fail(err)
	}
	mergeBase, err := git("merge-base", "HEAD", base)
	if err != nil {
		return fail(err)
	}
	actPath, err := toolPath("act")
	if err != nil {
		return fail(err)
	}

	dir, err := os.MkdirTemp("", "tfsaz-ci-")
	if err != nil {
		return fail(err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, "ci-local:", err)
		}
	}()
	clone := filepath.Join(dir, "repo")
	head, err := snapshotClone(clone)
	if err != nil {
		return fail(err)
	}
	event, err := json.Marshal(map[string]any{"ref": "refs/heads/main", "before": strings.TrimSpace(mergeBase), "after": head})
	if err != nil {
		return fail(err)
	}
	eventPath := filepath.Join(dir, "event.json")
	if err := os.WriteFile(eventPath, event, 0o600); err != nil {
		return fail(err)
	}

	fmt.Fprintf(os.Stderr, "ci-local: engine %s (%s), image %s, before %s, head %s\n", or(context, "default"), arch, actImage, short(strings.TrimSpace(mergeBase)), short(head))
	c := exec.Command(actPath, actArgs(arch, eventPath, context != "", passthrough)...)
	c.Dir = clone
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	// act reads DOCKER_HOST, not docker contexts. A token in the environment
	// must not reach the containers either.
	c.Env = append(withoutEnv(os.Environ(), "GITHUB_TOKEN", "GH_TOKEN", "DOCKER_HOST"), "DOCKER_HOST="+dockerHost)
	if err := c.Run(); err != nil {
		return fail(err)
	}
	return true
}

// snapshotClone clones the repository into dir and commits the uncommitted
// and untracked changes there, so act sees the working tree as one commit. A
// worktree cannot be handed to act directly: its .git file points outside.
func snapshotClone(dir string) (string, error) {
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	head = strings.TrimSpace(head)
	if _, err := git("clone", "--quiet", "--no-hardlinks", ".", dir); err != nil {
		return "", err
	}
	if _, err := git("-C", dir, "checkout", "--quiet", "--detach", head); err != nil {
		return "", err
	}
	tracked, err := gitPaths("diff", "--name-only", "--no-renames", "-z", "HEAD", "--")
	if err != nil {
		return "", err
	}
	untracked, err := untrackedFiles()
	if err != nil {
		return "", err
	}
	changed := append(tracked, untracked...)
	if len(changed) == 0 {
		return head, nil
	}
	for _, f := range changed {
		if err := copyPath(filepath.FromSlash(f), filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			return "", err
		}
	}
	if _, err := git("-C", dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := git("-C", dir, "-c", "user.name=ci-local", "-c", "user.email=ci-local@localhost", "-c", "commit.gpgsign=false",
		"commit", "--quiet", "--no-verify", "--allow-empty", "-m", "ci-local: uncommitted changes"); err != nil {
		return "", err
	}
	out, err := git("-C", dir, "rev-parse", "HEAD")
	fmt.Fprintf(os.Stderr, "ci-local: %d uncommitted or untracked files committed in the temporary clone\n", len(changed))
	return strings.TrimSpace(out), err
}

// copyPath mirrors src at dst: a deleted file is removed, a symlink is
// recreated, a regular file is copied with its mode.
func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if os.IsNotExist(err) {
		return os.RemoveAll(dst)
	}
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode().Perm())
}

func output(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %v", name, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func withoutEnv(env []string, names ...string) []string {
	var kept []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, n := range names {
			if strings.EqualFold(name, n) {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, kv)
		}
	}
	return kept
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
