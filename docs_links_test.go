package main

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

var (
	mdLinkPattern     = regexp.MustCompile(`\]\(<?([^)\s>]+)>?(?:\s+"[^"]*")?\)`)
	inlineCodePattern = regexp.MustCompile("`[^`]*`")
	ownURLPattern     = regexp.MustCompile(`https://github\.com/toolspell/tfsaz[^\s"'` + "`" + `)<>\]]*`)
	schemePattern     = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`)
	headingPattern    = regexp.MustCompile(`^ {0,3}#{1,6}\s+(.*?)(?:\s+#+)?\s*$`)
)

func TestDocLinks(t *testing.T) {
	c := linkChecker{anchors: map[string]map[string]bool{}}
	var files []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		p = filepath.ToSlash(p)
		// .reference-apps holds upstream checkouts whose links are not ours, and
		// testdata fixtures carry deliberately odd links and URLs.
		if d.IsDir() && (p == ".git" || p == ".reference-apps" || p == "testdata" || strings.HasSuffix(p, "/testdata")) {
			return filepath.SkipDir
		}
		if !d.IsDir() && (strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".md")) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.FromSlash(file))
		if err != nil {
			t.Fatal(err)
		}
		inFence := false
		for i, line := range strings.Split(string(data), "\n") {
			where := fmt.Sprintf("%s:%d", file, i+1)
			for _, u := range ownURLPattern.FindAllString(line, -1) {
				c.checkOwnURL(where, strings.TrimRight(u, ".,;:"))
			}
			if !strings.HasSuffix(file, ".md") {
				continue
			}
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				inFence = !inFence
			}
			if inFence {
				continue
			}
			for _, m := range mdLinkPattern.FindAllStringSubmatch(inlineCodePattern.ReplaceAllString(line, ""), -1) {
				if !schemePattern.MatchString(m[1]) {
					c.checkRelative(where, file, m[1])
				}
			}
		}
	}
	if len(c.problems) > 0 {
		t.Errorf("broken doc links:\n%s", strings.Join(c.problems, "\n"))
	}
}

// githubPages are repository pages GitHub serves that no file in the tree
// backs, so the checker cannot verify them.
var githubPages = map[string]bool{
	"/issues/new/choose":       true,
	"/security/advisories/new": true,
}

type linkChecker struct {
	anchors  map[string]map[string]bool
	problems []string
}

func (c *linkChecker) report(where, target, reason string) {
	c.problems = append(c.problems, fmt.Sprintf("%s: %s: %s", where, target, reason))
}

func (c *linkChecker) checkRelative(where, from, target string) {
	ref, anchor, _ := strings.Cut(target, "#")
	ref, err := url.PathUnescape(ref)
	if err != nil {
		c.report(where, target, "bad escape")
		return
	}
	dest := from
	if ref != "" {
		dest = path.Join(path.Dir(from), ref)
	}
	c.checkTarget(where, target, dest, anchor)
}

func (c *linkChecker) checkOwnURL(where, u string) {
	rest := strings.TrimPrefix(u, "https://github.com/toolspell/tfsaz")
	rest, anchor, _ := strings.Cut(rest, "#")
	switch {
	case rest == "" || rest == "/":
		if anchor == "readme" {
			anchor = ""
		}
		c.checkTarget(where, u, "README.md", anchor)
	case strings.HasPrefix(rest, "/blob/main/"), strings.HasPrefix(rest, "/tree/main/"):
		c.checkTarget(where, u, path.Clean(rest[len("/blob/main/"):]), anchor)
	case githubPages[rest]:
	default:
		c.report(where, u, "unsupported GitHub URL form")
	}
}

func (c *linkChecker) checkTarget(where, target, dest, anchor string) {
	if dest == ".." || strings.HasPrefix(dest, "../") {
		c.report(where, target, "points outside the repository")
		return
	}
	info, err := os.Stat(filepath.FromSlash(dest))
	if err != nil {
		c.report(where, target, "file not found")
		return
	}
	if anchor == "" {
		return
	}
	if info.IsDir() || !strings.HasSuffix(dest, ".md") {
		c.report(where, target, "anchor on a non-markdown target")
		return
	}
	if !c.headingAnchors(dest)[anchor] {
		c.report(where, target, "no heading with anchor #"+anchor+" in "+dest)
	}
}

func (c *linkChecker) headingAnchors(file string) map[string]bool {
	if anchors, ok := c.anchors[file]; ok {
		return anchors
	}
	anchors := map[string]bool{}
	seen := map[string]int{}
	data, _ := os.ReadFile(filepath.FromSlash(file))
	inFence := false
	for _, line := range strings.Split(string(data), "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		m := headingPattern.FindStringSubmatch(line)
		if inFence || m == nil {
			continue
		}
		slug := headingSlug(m[1])
		if n := seen[slug]; n > 0 {
			anchors[fmt.Sprintf("%s-%d", slug, n)] = true
		} else {
			anchors[slug] = true
		}
		seen[slug]++
	}
	c.anchors[file] = anchors
	return anchors
}

// headingSlug follows GitHub: lowercase, keep letters, digits, '-' and '_',
// turn each space into '-' and drop everything else.
func headingSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestHeadingSlug(t *testing.T) {
	for heading, want := range map[string]string{
		"5. Run the service on port 443":                "5-run-the-service-on-port-443",
		"What setup installs":                           "what-setup-installs",
		"Shared `dev.azure.com` hostname":               "shared-devazurecom-hostname",
		"Windows 11 x64 and Ubuntu 26.04 ARM64":         "windows-11-x64-and-ubuntu-2604-arm64",
		"Why stock T3 cannot reach Azure DevOps Server": "why-stock-t3-cannot-reach-azure-devops-server",
		"Request bodies, headers and output":            "request-bodies-headers-and-output",
	} {
		if got := headingSlug(heading); got != want {
			t.Errorf("headingSlug(%q) = %q, want %q", heading, got, want)
		}
	}
}

func TestCheckOwnURL(t *testing.T) {
	const repo = "https://github.com/toolspell/tfsaz"
	for u, wantOK := range map[string]bool{
		repo:                                     true,
		repo + "#readme":                         true,
		repo + "/blob/main/SECURITY.md":          true,
		repo + "/blob/main/SECURITY.md#security": true,
		repo + "/blob/main/SECURITY.md#no-such":  false,
		repo + "/tree/main/docs":                 true,
		repo + "/issues/new/choose":              true,
		repo + "/security/advisories/new":        true,
		repo + "/issues/12345":                   false,
		repo + "/blob/feature/SECURITY.md":       false,
	} {
		c := linkChecker{anchors: map[string]map[string]bool{}}
		c.checkOwnURL("test", u)
		if gotOK := len(c.problems) == 0; gotOK != wantOK {
			t.Errorf("checkOwnURL(%q) ok = %v, want %v (problems: %v)", u, gotOK, wantOK, c.problems)
		}
	}
}
