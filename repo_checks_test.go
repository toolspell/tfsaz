package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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

// agentContextBudget records what every agent session loads, in bytes
// (about 4 per token). The CI job "Agent context budget" fails a PR that
// raises a value unless a maintainer adds the agent-context-increase label.
const agentContextBudget = ".agents/context-budget.json"

// Growth of agentContextHintBytes or more in one value, about two lines of
// prose, gets agentContextAdvice; smaller growth, such as a reworded rule,
// fails without it. The CI job repeats both as HINT_BYTES and HINT_URL.
const (
	agentContextHintBytes = 200
	agentContextHintURL   = "https://github.com/toolspell/tfsaz/blob/main/docs/operations/development.md#agent-context-budget"
	agentContextAdvice    = `Before you record the growth and ask for the agent-context-increase label, move detail that matters only in one kind of task to the doc that owns it and leave a one-line pointer that says when to read it ("Before you X, read Y"): ` + agentContextHintURL
	agentContextWorkflow  = ".github/workflows/agent-context-budget.yml"
)

var agentContextNames = map[string]string{
	"agentsMdBytes":         "AGENTS.md",
	"claudeMdBytes":         "CLAUDE.md",
	"skillFrontmatterBytes": "The skills' frontmatter",
}

// agentContextHint names the values of got that exceed want by
// agentContextHintBytes or more and appends agentContextAdvice. It returns ""
// when none does. A value that want lacks is new and has no base to grow from.
func agentContextHint(want, got map[string]int) string {
	var grown []string
	for _, key := range slices.Sorted(maps.Keys(got)) {
		base, ok := want[key]
		if !ok || got[key]-base < agentContextHintBytes {
			continue
		}
		name := agentContextNames[key]
		if name == "" {
			name = key
		}
		grown = append(grown, fmt.Sprintf("%s grew by %d bytes.", name, got[key]-base))
	}
	if len(grown) == 0 {
		return ""
	}
	return strings.Join(append(grown, agentContextAdvice), " ")
}

const maxAgentFileLines = 300

// TestAgentContextBudget keeps agentContextBudget equal to the measured
// context, so the CI comparison cannot be dodged by leaving the file stale.
// CRLF is counted as LF, so a Windows checkout measures the same bytes.
func TestAgentContextBudget(t *testing.T) {
	read := func(file string) string {
		data, err := os.ReadFile(filepath.FromSlash(file))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	skills, err := filepath.Glob(filepath.FromSlash(".agents/skills/*/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) == 0 {
		t.Fatal("no skills found in .agents/skills")
	}
	got := map[string]int{
		"agentsMdBytes": len(read("AGENTS.md")),
		"claudeMdBytes": len(read("CLAUDE.md")),
	}
	for _, skill := range skills {
		m := skillFrontmatterPattern.FindStringSubmatch(read(filepath.ToSlash(skill)))
		if m == nil {
			t.Errorf("%s: no frontmatter between --- lines", skill)
			continue
		}
		got["skillFrontmatterBytes"] += len(m[1])
	}
	var want map[string]int
	if err := json.Unmarshal([]byte(read(agentContextBudget)), &want); err != nil {
		t.Fatalf("%s: %v", agentContextBudget, err)
	}
	if !maps.Equal(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		msg := agentContextBudget + " does not match the measured agent context."
		if hint := agentContextHint(want, got); hint != "" {
			msg += "\n" + hint
		}
		t.Errorf("%s\nUpdate it in the same commit (growth needs the maintainer's OK):\n%s", msg, gotJSON)
	}
	workflow := read(agentContextWorkflow)
	for _, line := range []string{fmt.Sprintf("HINT_BYTES: %d", agentContextHintBytes), "HINT_URL: " + agentContextHintURL} {
		if !strings.Contains(workflow, line+"\n") {
			t.Errorf("%s has no line %q; keep it equal to the constant in repo_checks_test.go", agentContextWorkflow, line)
		}
	}
}

func TestAgentContextHint(t *testing.T) {
	const at = agentContextHintBytes
	base := map[string]int{"agentsMdBytes": 1000, "skillFrontmatterBytes": 500}
	for _, tc := range []struct {
		name  string
		got   map[string]int
		grown string // the growth sentences before agentContextAdvice; empty for no hint
	}{
		{"unchanged", base, ""},
		{"shrunk", map[string]int{"agentsMdBytes": 400, "skillFrontmatterBytes": 500}, ""},
		{"rewording below the threshold", map[string]int{"agentsMdBytes": 1000 + at - 1, "skillFrontmatterBytes": 500 + at - 1}, ""},
		{"at the threshold", map[string]int{"agentsMdBytes": 1000 + at, "skillFrontmatterBytes": 500}, fmt.Sprintf("AGENTS.md grew by %d bytes.", at)},
		{"only the value over the threshold", map[string]int{"agentsMdBytes": 1050, "skillFrontmatterBytes": 900}, "The skills' frontmatter grew by 400 bytes."},
		{"both values", map[string]int{"agentsMdBytes": 1300, "skillFrontmatterBytes": 900}, "AGENTS.md grew by 300 bytes. The skills' frontmatter grew by 400 bytes."},
		{"new value", map[string]int{"agentsMdBytes": 1000, "skillFrontmatterBytes": 500, "claudeMdBytes": 5000}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := ""
			if tc.grown != "" {
				want = tc.grown + " " + agentContextAdvice
			}
			if got := agentContextHint(base, tc.got); got != want {
				t.Errorf("agentContextHint() = %q, want %q", got, want)
			}
		})
	}
}

var skillFrontmatterPattern = regexp.MustCompile(`^---\n([\s\S]*?)\n---`)

// TestAgentFileLines keeps every agent-facing file short enough to read in
// one pass.
func TestAgentFileLines(t *testing.T) {
	files := []string{"AGENTS.md", "CLAUDE.md"}
	err := filepath.WalkDir(".agents", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(p) == ".md" {
			files = append(files, filepath.ToSlash(p))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	for _, file := range files {
		data, err := os.ReadFile(filepath.FromSlash(file))
		if err != nil {
			t.Fatal(err)
		}
		if lines := bytes.Count(data, []byte("\n")) + 1; lines > maxAgentFileLines {
			problems = append(problems, fmt.Sprintf("%s: %d lines", file, lines))
		}
	}
	if len(problems) > 0 {
		t.Errorf("agent-facing files over %d lines; split or trim them:\n%s", maxAgentFileLines, strings.Join(problems, "\n"))
	}
}

var t3PinPattern = regexp.MustCompile(`name:\s*"t3code",[^}]*?tag:\s*"([^"]+)"`)

var t3PinDocs = []string{"README.md", "docs/internals/t3-compatibility.md", "docs/user/t3-features.md", "docs/user/az-compatibility.md"}

// missingT3Pin returns the docs whose content lacks the pinned T3 tag.
func missingT3Pin(tag string, docs map[string]string) []string {
	var missing []string
	for _, file := range slices.Sorted(maps.Keys(docs)) {
		if !strings.Contains(docs[file], tag) {
			missing = append(missing, file)
		}
	}
	return missing
}

// TestT3PinInDocs keeps the pages that state which T3 revision TFSaz follows
// in step with the pin in tools/syncrepos/main.go.
func TestT3PinInDocs(t *testing.T) {
	src, err := os.ReadFile(filepath.FromSlash("tools/syncrepos/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := t3PinPattern.FindSubmatch(src)
	if m == nil {
		t.Fatal("no t3code tag found in tools/syncrepos/main.go")
	}
	tag := string(m[1])
	docs := map[string]string{}
	for _, file := range t3PinDocs {
		data, err := os.ReadFile(filepath.FromSlash(file))
		if err != nil {
			t.Fatal(err)
		}
		docs[file] = string(data)
	}
	if missing := missingT3Pin(tag, docs); len(missing) > 0 {
		t.Errorf("these files do not name the T3 pin %s from tools/syncrepos/main.go; update them (update-references skill, step 3.2):\n%s", tag, strings.Join(missing, "\n"))
	}
}

func TestMissingT3Pin(t *testing.T) {
	const tag = "v1.2.3-nightly.1"
	for _, tc := range []struct {
		name string
		docs map[string]string
		want []string
	}{
		{"all name the pin", map[string]string{"a.md": "at " + tag, "b.md": tag}, nil},
		{"one lacks it", map[string]string{"a.md": "at " + tag, "b.md": "v1.2.2-nightly.9"}, []string{"b.md"}},
		{"all lack it", map[string]string{"b.md": "", "a.md": "c478fde9fe"}, []string{"a.md", "b.md"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := missingT3Pin(tag, tc.docs); !slices.Equal(got, tc.want) {
				t.Errorf("missingT3Pin() = %v, want %v", got, tc.want)
			}
		})
	}
}
