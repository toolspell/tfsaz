package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var gitIdentityEnv = []string{
	"GIT_AUTHOR_NAME=Test User", "GIT_AUTHOR_EMAIL=tester@mock.invalid",
	"GIT_COMMITTER_NAME=Test User", "GIT_COMMITTER_EMAIL=tester@mock.invalid",
	"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
}

// seedEnv fixes the dates so seeded commit IDs are the same on every start.
var seedEnv = append([]string{"GIT_AUTHOR_DATE=2026-11-02T09:00:00Z", "GIT_COMMITTER_DATE=2026-11-02T09:00:00Z"}, gitIdentityEnv...)

func git(dir string, env []string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

func createBareRepo(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if _, err := git("", gitIdentityEnv, nil, "init", "-q", "--bare", "-b", "main", dir); err != nil {
		return err
	}
	_, err := git(dir, gitIdentityEnv, nil, "config", "http.receivepack", "true")
	return err
}

func seedRepoContent(bare, name string, branches []branchSeed) error {
	work, err := os.MkdirTemp("", "seed-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	write := func(files map[string]string) error {
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			full := filepath.Join(work, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(full, []byte(files[p]), 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	steps := [][]string{{"init", "-q", "-b", "main"}}
	for _, step := range steps {
		if _, err := git(work, seedEnv, nil, step...); err != nil {
			return err
		}
	}
	if err := write(map[string]string{"README.md": "# " + name + "\n\nMock repository.\n", "docs/guide.md": "Guide\n"}); err != nil {
		return err
	}
	if err := commitAll(work, "Initial commit"); err != nil {
		return err
	}
	for _, b := range branches {
		if _, err := git(work, seedEnv, nil, "checkout", "-q", "-b", b.name, "main"); err != nil {
			return err
		}
		if err := write(b.files); err != nil {
			return err
		}
		if err := commitAll(work, "Change on "+b.name); err != nil {
			return err
		}
	}
	_, err = git(work, seedEnv, nil, "push", "-q", bare, "--all")
	return err
}

func commitAll(work, message string) error {
	if _, err := git(work, seedEnv, nil, "add", "-A"); err != nil {
		return err
	}
	_, err := git(work, seedEnv, nil, "commit", "-q", "-m", message)
	return err
}

// head returns the commit a ref points to, or "" when it does not exist.
func head(dir, ref string) string {
	out, err := git(dir, gitIdentityEnv, nil, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func defaultBranch(dir string) string {
	out, err := git(dir, gitIdentityEnv, nil, "symbolic-ref", "HEAD")
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(out)
	if head(dir, ref) == "" {
		return ""
	}
	return ref
}

func mergeBase(dir, a, b string) string {
	out, err := git(dir, gitIdentityEnv, nil, "merge-base", a, b)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

type change struct {
	Type, Path, Object, Original string
}

func diff(dir, from, to string) ([]change, error) {
	out, err := git(dir, gitIdentityEnv, nil, "diff-tree", "-r", "-z", "--no-renames", "--raw", from, to)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var changes []change
	for i := 0; i+1 < len(fields); i += 2 {
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) < 5 {
			continue
		}
		c := change{Path: "/" + fields[i+1], Object: meta[3], Original: meta[2]}
		switch meta[4] {
		case "A":
			c.Type, c.Original = "add", ""
		case "D":
			c.Type, c.Object = "delete", meta[2]
		default:
			c.Type = "edit"
		}
		changes = append(changes, c)
	}
	return changes, nil
}

func blob(dir, object string) ([]byte, error) {
	out, err := git(dir, gitIdentityEnv, nil, "cat-file", "blob", object)
	return []byte(out), err
}

func objectAt(dir, commit, path string) string {
	out, err := git(dir, gitIdentityEnv, nil, "rev-parse", "--verify", "-q", commit+":"+strings.TrimPrefix(path, "/"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// merge completes a PR the way the server does: a merge commit with both
// parents, or a squash commit with only the target as parent.
func merge(dir, source, target, message string, squash bool) (string, error) {
	out, err := git(dir, gitIdentityEnv, nil, "merge-tree", "--write-tree", target, source)
	if err != nil {
		return "", fmt.Errorf("merge conflict: %w", err)
	}
	tree := strings.SplitN(out, "\n", 2)[0]
	args := []string{"commit-tree", tree, "-p", target}
	if !squash {
		args = append(args, "-p", source)
	}
	out, err = git(dir, gitIdentityEnv, []byte(message), args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func updateRef(dir, ref, commit, old string) error {
	_, err := git(dir, gitIdentityEnv, nil, "update-ref", ref, commit, old)
	return err
}

func deleteRef(dir, ref string) error {
	_, err := git(dir, gitIdentityEnv, nil, "update-ref", "-d", ref)
	return err
}

// serveGitHTTP runs git http-backend for a repository; rest is the part
// after /_git/{repo}, such as /info/refs.
func (s *server) serveGitHTTP(w http.ResponseWriter, r *http.Request, rp *repo, rest string) {
	execPath, err := git("", nil, nil, "--exec-path")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h := &cgi.Handler{
		Path: filepath.Join(strings.TrimSpace(execPath), "git-http-backend"),
		Env: append([]string{
			"GIT_PROJECT_ROOT=" + s.gitRoot, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + s.viewer.Account,
		}, gitIdentityEnv...),
	}
	clone := r.Clone(r.Context())
	clone.URL.Path = "/" + filepath.Base(rp.Dir) + rest
	clone.URL.RawPath = ""
	h.ServeHTTP(w, clone)
}
