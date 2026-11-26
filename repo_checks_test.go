package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	usesPattern        = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(.*)$`)
	pinnedUsesPattern  = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}\s+#\s*v\d`)
	pinnedImagePattern = regexp.MustCompile(`^docker://[^@\s]+@sha256:[0-9a-f]{64}\s+#\s*v\d`)
)

// TestPinnedActions requires every remote action and reusable workflow to be
// pinned to a full commit SHA with a version comment, because tags and
// branches can be moved to code nobody reviewed.
func TestPinnedActions(t *testing.T) {
	var problems []string
	files := 0
	for _, dir := range []string{".github/workflows", ".github/actions"} {
		if _, err := os.Stat(filepath.FromSlash(dir)); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(filepath.FromSlash(dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || dir == ".github/workflows" && filepath.Dir(p) != filepath.FromSlash(dir) {
				return nil
			}
			if ext := filepath.Ext(p); ext != ".yml" && ext != ".yaml" {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files++
			problems = append(problems, unpinnedUses(filepath.ToSlash(p), data)...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if files == 0 {
		t.Fatal("no workflow files found in .github/workflows")
	}
	if len(problems) > 0 {
		t.Errorf("unpinned uses (pin a full 40-hex commit SHA followed by a # vX comment):\n%s", strings.Join(problems, "\n"))
	}
}

// unpinnedUses accepts local actions (./...) because their code is reviewed in
// the same commit, and docker:// images only when pinned by digest.
func unpinnedUses(file string, data []byte) []string {
	var problems []string
	for i, line := range strings.Split(string(data), "\n") {
		m := usesPattern.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		ref := strings.NewReplacer(`"`, "", "'", "").Replace(strings.TrimSpace(m[1]))
		switch {
		case strings.HasPrefix(ref, "./"):
		case strings.HasPrefix(ref, "docker://") && pinnedImagePattern.MatchString(ref):
		case !strings.HasPrefix(ref, "docker://") && pinnedUsesPattern.MatchString(ref):
		default:
			problems = append(problems, fmt.Sprintf("%s:%d: %s", file, i+1, ref))
		}
	}
	return problems
}

func TestUnpinnedUses(t *testing.T) {
	sha := strings.Repeat("a", 40)
	digest := strings.Repeat("b", 64)
	for _, tc := range []struct {
		name   string
		line   string
		pinned bool
	}{
		{"step with SHA and version", "      - uses: actions/checkout@" + sha + " # v7", true},
		{"reusable workflow", "    uses: org/repo/.github/workflows/ci.yml@" + sha + " # v1.2.3", true},
		{"CRLF line ending", "      - uses: actions/checkout@" + sha + " # v7\r", true},
		{"local action", "      - uses: ./.github/actions/setup", true},
		{"quoted local action", `      - uses: "./.github/actions/setup"`, true},
		{"docker image by digest", "      - uses: docker://alpine@sha256:" + digest + " # v3.20", true},
		{"tag", "      - uses: actions/checkout@v7", false},
		{"branch", "      - uses: actions/checkout@main", false},
		{"short SHA", "      - uses: actions/checkout@3d3c42e # v7", false},
		{"uppercase SHA", "      - uses: actions/checkout@" + strings.Repeat("A", 40) + " # v7", false},
		{"SHA without comment", "      - uses: actions/checkout@" + sha, false},
		{"comment without version", "      - uses: actions/checkout@" + sha + " # pinned", false},
		{"quoted SHA", `      - uses: "actions/checkout@` + sha + `" # v7`, true},
		{"no ref", "      - uses: actions/checkout", false},
		{"docker image by tag", "      - uses: docker://alpine:3.20", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := unpinnedUses("test.yml", []byte("steps:\n"+tc.line+"\n"))
			if got := len(problems) == 0; got != tc.pinned {
				t.Errorf("pinned = %v, want %v (problems: %q)", got, tc.pinned, problems)
			}
		})
	}
}

// TestNoEmDashInMarkdown checks every markdown file Git tracks or would track,
// so gitignored folders such as reference checkouts stay out.
func TestNoEmDashInMarkdown(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	out, err := exec.CommandContext(t.Context(), "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.md").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var problems []string
	for _, file := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if file == "" {
			continue
		}
		data, err := os.ReadFile(filepath.FromSlash(file))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range bytes.Split(data, []byte("\n")) {
			if bytes.ContainsRune(line, '\u2014') {
				problems = append(problems, fmt.Sprintf("%s:%d", file, i+1))
			}
		}
	}
	if len(problems) > 0 {
		t.Errorf("em dash (U+2014) in markdown; use a colon, comma, parentheses or a new sentence:\n%s", strings.Join(problems, "\n"))
	}
}
