package main

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

func git(args ...string) (string, error) {
	c := exec.Command("git", args...)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// gitPaths runs a git command whose output is NUL-separated paths. -z keeps
// paths raw; without it Git quotes non-ASCII names, which then match no file.
func gitPaths(args ...string) ([]string, error) {
	out, err := git(args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

func repoRoot() (string, error) {
	out, err := git("rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

// changedFiles lists the files that differ from the merge base with base,
// committed, staged, unstaged or untracked.
func changedFiles(base string) (files []string, mergeBase string, err error) {
	out, err := git("merge-base", "HEAD", base)
	if err != nil {
		return nil, "", err
	}
	mergeBase = strings.TrimSpace(out)
	tracked, err := gitPaths("diff", "--name-only", "--no-renames", "-z", mergeBase, "--")
	if err != nil {
		return nil, "", err
	}
	untracked, err := untrackedFiles()
	if err != nil {
		return nil, "", err
	}
	files = append(tracked, untracked...)
	slices.Sort(files)
	return slices.Compact(files), mergeBase, nil
}

func untrackedFiles() ([]string, error) {
	return gitPaths("ls-files", "--others", "--exclude-standard", "-z")
}

func stagedFiles() ([]string, error) {
	return gitPaths("diff", "--cached", "--name-only", "--diff-filter=ACMR", "-z")
}

// A plan is what the fast check runs for a set of changed files.
type plan struct {
	goFiles   []string // changed Go files that still exist, for gofmt
	goChanged bool     // root module Go code, go.mod, go.sum or lint config changed
	modules   []string // modules whose go mod tidy state may have changed
	packages  []string // root module packages to test
	workflows bool     // a workflow file changed
}

// planFor maps changed files to checks. pkgDirs are the root module's package
// directories relative to the root, "." for the root package.
func planFor(files []string, exists func(string) bool, pkgDirs []string) plan {
	var p plan
	pkgs := map[string]bool{
		// The root package always runs: it imports internal/az, and its repo
		// checks (doc links, em dashes, pinned actions) read the whole tree.
		".": true,
	}
	allPkgs := false
	mods := map[string]bool{}
	for _, f := range files {
		if mod := toolModule(f); mod != "" {
			mods[mod] = true
			continue
		}
		switch {
		case f == "go.mod" || f == "go.sum":
			p.goChanged, allPkgs = true, true
			mods["."] = true
		case f == ".golangci.yml":
			p.goChanged = true
		case strings.HasSuffix(f, ".go"):
			p.goChanged = true
			mods["."] = true
			if exists(f) {
				p.goFiles = append(p.goFiles, f)
			}
		case strings.HasPrefix(f, ".github/workflows/"):
			p.workflows = true
		}
		if dir := packageOf(f, pkgDirs); dir != "" {
			pkgs[dir] = true
		}
	}
	if allPkgs {
		p.packages = []string{"./..."}
	} else {
		for dir := range pkgs {
			if dir == "." {
				p.packages = append(p.packages, ".")
			} else {
				p.packages = append(p.packages, "./"+dir)
			}
		}
		slices.Sort(p.packages)
	}
	for m := range mods {
		p.modules = append(p.modules, m)
	}
	slices.Sort(p.modules)
	return p
}

// toolModule returns the tool module directory a file belongs to, if any.
func toolModule(file string) string {
	for _, name := range tools {
		dir := "tools/" + name
		if strings.HasPrefix(file, dir+"/") {
			return dir
		}
	}
	return ""
}

// packageOf returns the deepest package directory that contains file, so a
// change under internal/az/testdata tests internal/az. The root package
// contains everything and is always tested anyway.
func packageOf(file string, pkgDirs []string) string {
	dir := path.Dir(file)
	best := ""
	for _, pkg := range pkgDirs {
		if pkg == "." {
			continue
		}
		if (dir == pkg || strings.HasPrefix(dir, pkg+"/")) && len(pkg) > len(best) {
			best = pkg
		}
	}
	return best
}

func packageDirs(root string) ([]string, error) {
	out, err := exec.Command("go", "list", "-f", "{{.Dir}}", "./...").Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %v", err)
	}
	var dirs []string
	for _, abs := range strings.Fields(string(out)) {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return nil, err
		}
		dirs = append(dirs, filepath.ToSlash(rel))
	}
	return dirs, nil
}

// gofmtCheck reports files whose content gofmt would change. read returns a
// file's content, so the hook can check the staged version.
func gofmtCheck(files []string, read func(string) ([]byte, error)) func(*bytes.Buffer) error {
	return func(out *bytes.Buffer) error {
		var bad []string
		for _, f := range files {
			src, err := read(f)
			if err != nil {
				return err
			}
			// Git for Windows checks files out with CRLF by default; the
			// index, which is what gets committed, holds LF.
			src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
			formatted, err := format.Source(src)
			if err != nil {
				fmt.Fprintf(out, "%s: %v\n", f, err)
				bad = append(bad, f)
				continue
			}
			if !bytes.Equal(src, formatted) {
				fmt.Fprintln(out, f)
				bad = append(bad, f)
			}
		}
		if len(bad) > 0 {
			fmt.Fprintln(out, "run: gofmt -w", strings.Join(bad, " "))
			return fmt.Errorf("%d files need gofmt", len(bad))
		}
		return nil
	}
}

func readWorktree(f string) ([]byte, error) { return os.ReadFile(filepath.FromSlash(f)) }

func readStaged(f string) ([]byte, error) {
	out, err := git("cat-file", "blob", ":"+f)
	return []byte(out), err
}

func fileExists(f string) bool {
	_, err := os.Stat(filepath.FromSlash(f))
	return err == nil
}
