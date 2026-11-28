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
	"regexp"
	"slices"
	"strconv"
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

// changeSelection is what CI runs for the change since base, compared from
// the clone in the working directory, and why. Every error fails open to
// everything. pkgDirs lists the root module's package directories; it is
// called only when the diff has files.
func changeSelection(base string, pkgDirs func() ([]string, error)) (selection, string) {
	sha, reason := resolveBase(base)
	if reason != "" {
		return everything(), reason
	}
	files, err := gitPaths("diff", "--name-only", "--no-renames", "-z", sha, "HEAD", "--")
	if err != nil {
		return everything(), err.Error()
	}
	if len(files) == 0 {
		return everything(), "no changed files"
	}
	dirs, err := pkgDirs()
	if err != nil {
		return everything(), err.Error()
	}
	return selectFor(files, dirs, osVersions(sha)), fmt.Sprintf("%d changed files since %s", len(files), short(sha))
}

// osVersions is what narrows the test matrix OSes: the files at base and
// HEAD, or nil under act, which runs only the Ubuntu entry
// (ci-local), so a change for another OS still tests there.
func osVersions(base string) fileVersions {
	if os.Getenv("ACT") != "" {
		return nil
	}
	return gitVersions(base)
}

// gitVersions reads files at base and at HEAD from the clone. A file in a
// directory where, on either side, a Go file embeds files reads as an error
// and selects every OS, and so does every file once any Go file is a
// symlink: their bytes may also count as data or compile under another name.
func gitVersions(base string) fileVersions {
	dirs, dirsErr := aliasDirs(base)
	return func(file string) ([]byte, bool, []byte, bool, error) {
		if dirsErr != nil {
			return nil, false, nil, false, dirsErr
		}
		for _, dir := range dirs {
			if dir == "." || strings.HasPrefix(file, dir+"/") {
				return nil, false, nil, false, fmt.Errorf("%s: a Go file in %s embeds files, or a Go file is a symlink", file, dir)
			}
		}
		b, bOK, err := gitBlob(base, file)
		if err != nil {
			return nil, false, nil, false, err
		}
		h, hOK, err := gitBlob("HEAD", file)
		return b, bOK, h, hOK, err
	}
}

// aliasDirs lists the directories that hold, at base or HEAD, a Go file with
// a //go:embed directive, and "." for all when a Go file is a symlink.
func aliasDirs(base string) ([]string, error) {
	var dirs []string
	for _, rev := range []string{base, "HEAD"} {
		raw, err := exec.Command("git", "grep", "-l", "-z", "--fixed-strings", "//go:embed", rev, "--", "*.go").Output()
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit) && exit.ExitCode() == 1:
			// no match
		case err != nil:
			return nil, fmt.Errorf("git grep //go:embed %s: %v", rev, err)
		default:
			for _, f := range strings.Split(string(raw), "\x00") {
				if _, name, ok := strings.Cut(f, ":"); ok {
					dirs = append(dirs, path.Dir(name))
				}
			}
		}
		// ls-tree takes path prefixes, not globs, so filter the names here.
		entries, err := gitPaths("ls-tree", "-r", "-z", rev)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			// <mode> <type> <object>\t<path>
			if meta, name, ok := strings.Cut(e, "\t"); ok && strings.HasPrefix(meta, "120000 ") && strings.HasSuffix(name, ".go") {
				// A link may point into any package, so nothing narrows.
				dirs = append(dirs, ".")
			}
		}
	}
	return dirs, nil
}

// gitBlob returns a file's content at rev, or ok false when rev's tree has no
// such entry. A blob that cannot be read is an error, not a deletion.
func gitBlob(rev, file string) ([]byte, bool, error) {
	entry, err := git("ls-tree", "-z", rev, "--", file)
	if err != nil {
		return nil, false, err
	}
	if entry == "" {
		return nil, false, nil
	}
	out, err := git("cat-file", "blob", rev+":"+file)
	if err != nil {
		return nil, false, err
	}
	return []byte(out), true, nil
}

// ciChanges writes the Changes job's outputs: tests and mock (true|false),
// packages (for ci-test -packages) and matrix (the test jobs as JSON).
func ciChanges(base string) bool {
	s, why := changeSelection(base, func() ([]string, error) {
		root, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		return packageDirs(root)
	})
	matrix, err := json.Marshal(map[string]any{"include": s.matrix()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ci-changes:", err)
		return false
	}
	fmt.Fprintf(os.Stderr, "ci-changes: %s; tests: %s on %s, %d Windows shards; Go gates: %v; mock: %v\n",
		why, or(strings.Join(s.packages, " "), "none"), or(strings.Join(s.oses, " "), "no OS"), s.shards, s.goGates, s.mock)
	lines := fmt.Sprintf("tests=%v\nmock=%v\npackages=%s\nmatrix=%s\n", s.tests(), s.mock, strings.Join(s.packages, " "), matrix)
	fmt.Print(lines)
	if out := os.Getenv("GITHUB_OUTPUT"); out != "" {
		if err := appendFile(out, lines); err != nil {
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

// ciChecks is the CI checks job: the whole-tree gates and reports of ready
// for the root module, without the tests (the matrix runs those) but with the
// repo checks. The mock job covers the mock's module.
func ciChecks() bool {
	steps, err := treeSteps([]string{"."}, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ci-checks:", err)
		return false
	}
	steps = append(steps, step{name: "repo checks", run: cmd("go", "test", "-count=1", "-run", repoChecksRun, ".")})
	return runSteps("ci-checks", steps)
}

// ciMock is the CI mock job: the live-test mock module's race tests, Go gates
// and go mod tidy -diff.
func ciMock() bool {
	mods := []string{mockModule}
	steps := append(goGates(mods), tidy(mods)...)
	steps = append(steps, step{name: "go test -race " + modulePattern(mockModule), run: cmd("go", "-C", mockModule, "test", "-race", "-count=1", "./...")})
	return runSteps("ci-mock", steps)
}

// ciTest is a native test matrix entry: the root module's tests of packages
// (patterns, "./..." for all) with the race detector, or with shard i/n the
// i-th of n parts of their top-level tests. The parts take the sorted names in
// turn, so each test runs in exactly one part and the slow Git tests spread
// out. The mock job tests the mock's module.
func ciTest(shard string, packages []string) bool {
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	index, total, err := parseShard(shard)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ci-test:", err)
		return false
	}
	args := []string{"test", "-race", "-count=1"}
	if total > 1 {
		// -race here too, so the listing builds the binaries the run reuses.
		list := exec.Command("go", append([]string{"test", "-race", "-list", "."}, packages...)...)
		list.Stderr = os.Stderr
		out, err := list.Output()
		if err != nil {
			fmt.Fprintln(os.Stderr, "ci-test: listing the tests:", err)
			return false
		}
		names := listedTests(string(out))
		part := shardTests(names, index, total)
		fmt.Fprintf(os.Stderr, "ci-test: shard %d/%d runs %d of %d tests\n", index, total, len(part), len(names))
		if len(part) == 0 {
			return true
		}
		args = append(args, "-run", "^("+strings.Join(part, "|")+")$")
	}
	c := exec.Command("go", append(args, packages...)...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "ci-test: %s: %v\n", strings.Join(packages, " "), err)
		return false
	}
	return true
}

func parseShard(shard string) (int, int, error) {
	i, n, ok := strings.Cut(shard, "/")
	index, err1 := strconv.Atoi(i)
	total, err2 := strconv.Atoi(n)
	if !ok || err1 != nil || err2 != nil || total < 1 || index < 1 || index > total {
		return 0, 0, fmt.Errorf("shard %q is not i/n with 1 <= i <= n", shard)
	}
	return index, total, nil
}

// go test -list prints the matching tests, examples, fuzz tests and benchmarks
// of each package, then the package's status line. -run selects all but the
// benchmarks.
var listedName = regexp.MustCompile(`^(Test|Example|Fuzz)[\p{L}\p{Nd}_]*$`)

func listedTests(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); listedName.MatchString(name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// shardTests picks the index-th of total parts of names, which must be sorted.
// A name shared by several packages lands in one part and runs in each of them.
func shardTests(names []string, index, total int) []string {
	var part []string
	for i, name := range names {
		if i%total == index-1 {
			part = append(part, name)
		}
	}
	return part
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
