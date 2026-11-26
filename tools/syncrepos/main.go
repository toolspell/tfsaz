// Command syncrepos fetches pinned, read-only reference checkouts into the
// gitignored .reference-apps/ folder. Run it from anywhere in the repository:
//
//	go run ./tools/syncrepos [--repo NAME]
//	go run ./tools/syncrepos -latest [--repo NAME]
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
)

const refDir = ".reference-apps"

type referenceRepo struct {
	name   string
	url    string
	tag    string
	commit string
	// Non-cone sparse-checkout patterns. Every pattern without "!" must name a
	// path that exists at the commit; sync fails otherwise, so a re-pin notices
	// upstream moves.
	paths []string
	// Release channels -latest reports, each a full-match pattern on tag names.
	channels []channel
}

type channel struct {
	name    string
	pattern *regexp.Regexp
}

func tagPattern(expr string) *regexp.Regexp { return regexp.MustCompile(`^(?:` + expr + `)$`) }

// Re-pin deliberately: docs cite these revisions.
var repos = []referenceRepo{
	{
		name:   "t3code",
		url:    "https://github.com/pingdotgg/t3code.git",
		tag:    "v0.1.13-nightly.20261125.5258",
		commit: "bf961f8121c6336915ecf7b3f97ce6e68ab38fd7",
		paths: []string{
			"/packages/source-control-azure-devops/",
			"/packages/source-control-core/",
			"/docs/user/source-control.md",
		},
		channels: []channel{
			{"stable", tagPattern(`v\d+\.\d+\.\d+`)},
			{"nightly", tagPattern(`v\d+\.\d+\.\d+-nightly\.\d+\.\d+`)},
		},
	},
	{
		name:   "azure-cli",
		url:    "https://github.com/Azure/azure-cli.git",
		tag:    "azure-cli-2.93.0",
		commit: "51552fd56854d33f646a031bbbf024cf1fbc4d42",
		paths: []string{
			"/src/azure-cli/azure/cli/__main__.py",
			"/src/azure-cli/azure/cli/command_modules/profile/",
			"/src/azure-cli/azure/cli/command_modules/util/",
			"/src/azure-cli-core/azure/cli/core/__init__.py",
			"/src/azure-cli-core/azure/cli/core/_output.py",
			"/src/azure-cli-core/azure/cli/core/_profile.py",
			"/src/azure-cli-core/azure/cli/core/azclierror.py",
			"/src/azure-cli-core/azure/cli/core/commands/__init__.py",
			"/src/azure-cli-core/azure/cli/core/parser.py",
			"/src/azure-cli-core/azure/cli/core/util.py",
			"!tests/",
		},
		channels: []channel{{"release", tagPattern(`azure-cli-\d+\.\d+\.\d+`)}},
	},
	{
		name:   "azure-devops-cli-extension",
		url:    "https://github.com/Azure/azure-devops-cli-extension.git",
		tag:    "20261117.1",
		commit: "3ea8b2f20761f5433edb469c9d55235db37fa7f2",
		paths: []string{
			"/azure-devops/azext_devops/__init__.py",
			"/azure-devops/azext_devops/dev/boards/",
			"/azure-devops/azext_devops/dev/common/",
			"/azure-devops/azext_devops/dev/repos/",
			"/azure-devops/azext_devops/dev/team/",
			"/azure-devops/azext_devops/devops_sdk/client.py",
			"/azure-devops/azext_devops/devops_sdk/connection.py",
			"/azure-devops/azext_devops/devops_sdk/exceptions.py",
			"/azure-devops/azext_devops/devops_sdk/v5_0/location/",
			"!tests/",
		},
		channels: []channel{{"release", tagPattern(`\d{8}\.\d+`)}},
	},
	{
		// The CLI framework under azure-cli, at the version azure-cli pins:
		// output formats, --query, argument parsing and exit codes.
		name:   "knack",
		url:    "https://github.com/microsoft/knack.git",
		tag:    "v0.14.0",
		commit: "696251f16f59da8cea0e98284369cc541a2beac5",
		paths: []string{
			"/knack/",
			"!/knack/testsdk/",
		},
		// The latest tag is informational: pin what azure-cli-core requires.
		channels: []channel{{"release", tagPattern(`v\d+\.\d+\.\d+`)}},
	},
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "syncrepos:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	selected, latest, err := selectRepos(repos, args, stderr)
	if err != nil {
		return err
	}
	if latest {
		return reportLatest(selected, lsRemoteTags, stdout)
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	for _, repo := range selected {
		if err := syncRepo(filepath.Join(root, refDir, repo.name), repo, stdout); err != nil {
			return fmt.Errorf("%s: %w", repo.name, err)
		}
	}
	return nil
}

// selectRepos returns every entry, or the one --repo names, and whether
// -latest asks for a release report instead of a sync.
func selectRepos(all []referenceRepo, args []string, stderr io.Writer) ([]referenceRepo, bool, error) {
	fs := flag.NewFlagSet("syncrepos", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("repo", "", "sync only this entry: "+strings.Join(names(all), ", "))
	latest := fs.Bool("latest", false, "print each pin next to the latest upstream release tags; changes nothing")
	if err := fs.Parse(args); err != nil {
		return nil, false, err
	}
	if fs.NArg() > 0 {
		return nil, false, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *name == "" {
		return all, *latest, nil
	}
	for _, repo := range all {
		if repo.name == *name {
			return []referenceRepo{repo}, *latest, nil
		}
	}
	return nil, false, fmt.Errorf("unknown repo %q; known: %s", *name, strings.Join(names(all), ", "))
}

// tagLister returns tag name to commit for a remote.
type tagLister func(url string) (map[string]string, error)

func lsRemoteTags(url string) (map[string]string, error) {
	out, err := exec.Command("git", "ls-remote", "--tags", url).Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-remote %s: %w", url, err)
	}
	return parseTags(string(out)), nil
}

// parseTags reads git ls-remote --tags output. An annotated tag's peeled
// "^{}" line wins, so the result is always a commit.
func parseTags(out string) map[string]string {
	tags := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		name, ok := strings.CutPrefix(ref, "refs/tags/")
		if !ok {
			continue
		}
		if base, peeled := strings.CutSuffix(name, "^{}"); peeled {
			tags[base] = sha
		} else if _, seen := tags[name]; !seen {
			tags[name] = sha
		}
	}
	return tags
}

var digitRuns = regexp.MustCompile(`\d+`)

// newerTag compares tags by their numeric runs, so 2.100.0 > 2.93.0 and
// nightly dates and build numbers order correctly.
func newerTag(a, b string) bool {
	x, y := digitRuns.FindAllString(a, -1), digitRuns.FindAllString(b, -1)
	for i := range min(len(x), len(y)) {
		m, _ := strconv.ParseUint(x[i], 10, 64)
		n, _ := strconv.ParseUint(y[i], 10, 64)
		if m != n {
			return m > n
		}
	}
	return len(x) > len(y)
}

func reportLatest(selected []referenceRepo, list tagLister, stdout io.Writer) error {
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "REPO\tCHANNEL\tPINNED\tLATEST\tCOMMIT\tSTATUS"); err != nil {
		return err
	}
	for _, repo := range selected {
		tags, err := list(repo.url)
		if err != nil {
			return fmt.Errorf("%s: %w", repo.name, err)
		}
		for _, ch := range repo.channels {
			latest := ""
			for tag := range tags {
				if ch.pattern.MatchString(tag) && (latest == "" || newerTag(tag, latest)) {
					latest = tag
				}
			}
			pinned, status := "-", "not pinned"
			if ch.pattern.MatchString(repo.tag) {
				pinned, status = repo.tag, "current"
				if latest != repo.tag {
					status = "newer release"
				}
			}
			commit := "-"
			if latest == "" {
				latest, status = "-", "no matching tag"
			} else {
				commit = short(tags[latest])
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", repo.name, ch.name, pinned, latest, commit, status); err != nil {
				return err
			}
		}
	}
	return w.Flush()
}

func names(all []referenceRepo) []string {
	out := make([]string, len(all))
	for i, repo := range all {
		out[i] = repo.name
	}
	return out
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find the repository root: %w", err)
	}
	root := filepath.FromSlash(strings.TrimSpace(string(out)))
	if _, err := os.Stat(filepath.Join(root, "tools", "syncrepos")); err != nil {
		return "", fmt.Errorf("%s is not the tfsaz repository; run syncrepos from inside it", root)
	}
	return root, nil
}

func syncRepo(dir string, repo referenceRepo, stdout io.Writer) error {
	if verify(dir, repo) == nil {
		_, err := fmt.Fprintf(stdout, "%s/%s is at %s (%s).\n", refDir, repo.name, repo.tag, short(repo.commit))
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		// Only this tool writes here, so a broken entry is recreated.
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		if err := git("", "init", "--quiet", dir); err != nil {
			return err
		}
		if err := git(dir, "remote", "add", "origin", repo.url); err != nil {
			return err
		}
	} else {
		if err := git(dir, "remote", "set-url", "origin", repo.url); err != nil {
			return err
		}
		if gitOut(dir, "rev-parse", "--verify", "--quiet", "HEAD") != "" {
			// Local edits would block the sparse update; references stay read-only.
			if err := git(dir, "reset", "--quiet", "--hard"); err != nil {
				return err
			}
		}
	}
	// Patterns go in before the checkout, so excluded blobs are never downloaded.
	if err := git(dir, append([]string{"sparse-checkout", "set", "--no-cone"}, repo.paths...)...); err != nil {
		return err
	}
	if gitOut(dir, "cat-file", "-t", repo.commit) != "commit" {
		if _, err := fmt.Fprintf(stdout, "Fetching %s at %s (%s)...\n", repo.url, repo.tag, short(repo.commit)); err != nil {
			return err
		}
		if err := git(dir, "fetch", "--quiet", "--depth", "1", "--filter=blob:none", "origin", repo.commit); err != nil {
			return err
		}
	}
	if err := git(dir, "-c", "advice.detachedHead=false", "checkout", "--quiet", "--force", "--detach", repo.commit); err != nil {
		return err
	}
	if err := git(dir, "clean", "-ffdxq"); err != nil {
		return err
	}
	if err := verify(dir, repo); err != nil {
		return err
	}
	_, err := fmt.Fprintf(stdout, "%s/%s synced to %s (%s).\n", refDir, repo.name, repo.tag, short(repo.commit))
	return err
}

// verify checks the pinned HEAD, the sparse patterns, a clean tree and that
// every included path exists.
func verify(dir string, repo referenceRepo) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return errors.New("no checkout")
	}
	if head := gitOut(dir, "rev-parse", "HEAD"); head != repo.commit {
		return fmt.Errorf("HEAD is %q, want %s", head, repo.commit)
	}
	if got := strings.Fields(gitOut(dir, "sparse-checkout", "list")); !slices.Equal(got, repo.paths) {
		return fmt.Errorf("sparse patterns are %q, want %q", got, repo.paths)
	}
	if status := gitOut(dir, "status", "--porcelain", "--ignored"); status != "" {
		return fmt.Errorf("working tree is not clean:\n%s", status)
	}
	for _, p := range repo.paths {
		if strings.HasPrefix(p, "!") {
			continue
		}
		rel := filepath.FromSlash(strings.Trim(p, "/"))
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			return fmt.Errorf("sparse path %s is missing at %s", p, short(repo.commit))
		}
	}
	return nil
}

func git(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// gitOut returns trimmed stdout, or "" when git fails.
func gitOut(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func short(commit string) string { return commit[:min(len(commit), 10)] }
