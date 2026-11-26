// naming-allow-file: the test cases show the forms the check rejects
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestNaming enforces docs/internals/naming.md on every text file Git tracks
// or would track: no misspelled product name, TFSaz for the product, `tfsaz`
// only for literals, and none of the names the doc rules out. Dated records in
// docs/verification keep their wording.
func TestNaming(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	out, err := exec.CommandContext(t.Context(), "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var problems []string
	for _, file := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if file == "" || strings.HasPrefix(file, "docs/verification/") || namingSkipped.MatchString(file) {
			continue
		}
		// Regular files only: symlinks such as .claude/skills point at folders
		// that are listed on their own.
		if info, err := os.Lstat(filepath.FromSlash(file)); err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(filepath.FromSlash(file))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range namingProblems(file, string(data)) {
			problems = append(problems, fmt.Sprintf("%s:%d: %s", file, p.line, p.reason))
		}
	}
	if len(problems) > 0 {
		t.Errorf("naming problems (see docs/internals/naming.md; mark a deliberate use with \"naming-allow: <reason>\" on the line or the line above):\n%s", strings.Join(problems, "\n"))
	}
}

var (
	namingSkipped    = regexp.MustCompile(`(^|/)(LICENSE|go\.sum)$|\.(png|ico|icns|jpe?g|gif|webp)$`)
	namingLineMarker = regexp.MustCompile(`naming-allow:\s*\S`)
	namingFileMarker = regexp.MustCompile(`naming-allow-file:\s*\S`)
	// Every spelling of the name, also split by a space, hyphen or underscore.
	namingName = regexp.MustCompile(`(?i)tfs[ _-]?az`)
	// What may follow a lowercase tfsaz in code: a command or option, which
	// makes it the command line, or a version, as `--version` prints it.
	namingCommand = regexp.MustCompile(`^ (init|profile|repo|route|doctor|shim|help|trace|account|devops|repos|boards|rest|--[a-z]|[0-9]|%s|["'` + "`" + `])`)
	// Commands written with the product name, as in "run TFSaz init".
	namingProductAsCommand = regexp.MustCompile(`^ (init|repo|route|doctor|shim|--[a-z])\b`)
	namingFence            = regexp.MustCompile("^\\s*(```|~~~)")
	namingCodeSpan         = regexp.MustCompile("`[^`]*`")
)

// namingPhrases are the "Not" forms of docs/internals/naming.md that a pattern
// can find without false positives.
var namingPhrases = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	{regexp.MustCompile(`(?i)\bon-prem\b`), `write "on-premises"`},
	{regexp.MustCompile(`(^|[^-\w./])TFS([^-\w./]|\.(\W|$)|$)`), "write Azure DevOps Server; TFS only in literal identifiers"},
	{regexp.MustCompile(`(^|[^\w./])VSTS([^\w.]|$)|(?i)\bAzure DevOps cloud\b`), "write Azure DevOps Services"},
	{regexp.MustCompile(`(?i)\b(organization alias|Azure-style organization)\b`), "write Azure-style alias"},
	{regexp.MustCompile(`(?i)\bsynthetic (host(name)?|name)\b`), "write alias host"},
	{regexp.MustCompile(`(?i)\bAzure-style (URL|link)s?\b`), "write synthetic URL"},
	{regexp.MustCompile(`(?i)\b(real|on-prem) URLs?\b`), "write native URL"},
	{regexp.MustCompile(`(?i)\b(stock|real|Microsoft's) ` + "`az`"), "write Microsoft's Azure CLI"},
	{regexp.MustCompile(`(?i)\bTFSaz (launcher|remote helper)\b|\baz wrapper\b`), "write the `az` shim or the Git remote helper"},
	{regexp.MustCompile(`\bthe CLI\b`), "write TFSaz or Microsoft's Azure CLI"},
}

type namingMode int

const (
	namingCode  namingMode = iota // code, config and Markdown code blocks
	namingProse                   // Markdown outside code
	namingSpan                    // a Markdown code span
)

type namingProblem struct {
	line   int
	reason string
}

// namingProblems checks one file. Markdown prose must not show a lowercase
// tfsaz outside code format; code, code spans and code blocks may, where it
// is a literal.
func namingProblems(file, text string) []namingProblem {
	lines := strings.Split(text, "\n")
	if slices.ContainsFunc(lines[:min(5, len(lines))], namingFileMarker.MatchString) {
		return nil
	}
	markdown := strings.HasSuffix(file, ".md")
	inFence := false
	var problems []namingProblem
	for i, line := range lines {
		fence := markdown && namingFence.MatchString(line)
		if fence {
			inFence = !inFence
		}
		if namingLineMarker.MatchString(line) || i > 0 && namingLineMarker.MatchString(lines[i-1]) {
			continue
		}
		var reasons []string
		switch {
		case !markdown:
			reasons = append(namingSegment(line, namingCode), namingPhraseProblems(line, false)...)
		case inFence || fence:
			reasons = namingSegment(line, namingCode)
		default:
			// Odd pieces are code spans.
			for j, piece := range strings.Split(line, "`") {
				mode := namingProse
				if j%2 == 1 {
					mode = namingSpan
				}
				reasons = append(reasons, namingSegment(piece, mode)...)
			}
			reasons = append(reasons, namingPhraseProblems(line, true)...)
		}
		for _, r := range slices.Compact(reasons) {
			problems = append(problems, namingProblem{i + 1, r})
		}
	}
	return problems
}

func namingSegment(s string, mode namingMode) []string {
	var reasons []string
	for _, m := range namingName.FindAllStringIndex(s, -1) {
		if r := namingToken(s, m[0], m[1], mode); r != "" {
			reasons = append(reasons, r)
		}
	}
	return reasons
}

// namingPhraseProblems checks the phrases. In Markdown it reads prose only:
// code spans are blanked, except `az`, which some phrases name.
func namingPhraseProblems(line string, markdown bool) []string {
	if markdown {
		line = namingCodeSpan.ReplaceAllStringFunc(line, func(span string) string {
			if span == "`az`" {
				return span
			}
			return "``"
		})
	}
	var reasons []string
	for _, p := range namingPhrases {
		if p.pattern.MatchString(line) {
			reasons = append(reasons, p.reason)
		}
	}
	return reasons
}

func namingToken(s string, start, end int, mode namingMode) string {
	word, before, after := s[start:end], byte(0), s[end:]
	if start > 0 {
		before = s[start-1]
	}
	letter := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' }
	next := byte(0)
	if after != "" {
		next = after[0]
	}
	switch {
	case len(word) != 5:
		if letter(before) || letter(next) {
			return ""
		}
		return "misspelled name: write TFSaz, or tfsaz for a literal"
	case word == "TFSaz":
		if namingProductAsCommand.MatchString(after) {
			return "command written with the product name: write `tfsaz ...`"
		}
		return ""
	case word == "TFSAZ":
		if next == '_' {
			return ""
		}
	case word == "Tfsaz":
		// The PascalCase form only inside a longer identifier.
		if next >= 'A' && next <= 'Z' || next == '.' || before == '.' {
			return ""
		}
	case word == "tfsaz":
		// Part of a path, host, URL, file name or identifier.
		if letter(before) || strings.IndexByte("_-./\\~$@=#%:", before) >= 0 && before != 0 ||
			letter(next) || strings.IndexByte("_-/\\@#", next) >= 0 && next != 0 ||
			next == '.' && len(after) > 1 && letter(after[1]) {
			return ""
		}
		if mode == namingProse {
			return "lowercase product name in prose: write TFSaz, or `tfsaz` in code format for the literal"
		}
		// A code span that starts with tfsaz shows a literal or a command line.
		if mode == namingSpan && start == 0 || next == ':' || namingCommand.MatchString(after) ||
			strings.IndexByte("\"'`", before) >= 0 && before != 0 && next == before {
			return ""
		}
		return "lowercase product name: write TFSaz, or mark a literal with naming-allow"
	}
	return "misspelled name: write TFSaz, or tfsaz for a literal"
}

func TestNamingProblems(t *testing.T) {
	for _, tc := range []struct {
		name, file, text string
		want             []string
	}{
		{"product name in prose", "a.md", "TFSaz maps the alias.", nil},
		{"lowercase product in prose", "a.md", "Then tfsaz maps the alias.", []string{"lowercase product name in prose"}},
		{"bold label in prose", "a.md", "Select **tfsaz Links**.", []string{"lowercase product name in prose"}},
		{"command in a code span", "a.md", "Run `tfsaz init`.", nil},
		{"literal in a code span", "a.md", "The `tfsaz` keyring service.", nil},
		{"command in a code block", "a.md", "```sh\ntfsaz route setup\n```", nil},
		{"label in a code block", "a.md", "```powershell\n$h = Join-Path $env:LOCALAPPDATA 'tfsaz Links'\n```", []string{"lowercase product name:"}},
		{"path in prose", "a.md", "See ~/.tfsaz and tfsaz/config.json and tfsaz.exe.", nil},
		{"alias host in prose", "a.md", "Hosts tfsaz-0123 and git-remote-tfsaz.", nil},
		{"repository URL", "a.md", "[repo](https://github.com/toolspell/tfsaz)", nil},
		{"anchor", "a.md", "[x](b.md#what-tfsaz-changes)", nil},
		{"product as a command", "a.md", "Run TFSaz init first.", []string{"command written with the product name"}},
		{"product as a verb subject", "a.md", "TFSaz routes the call.", nil},
		{"capitalized misspelling", "a.go", `x := "Tfsaz Links"`, []string{"misspelled name"}},
		{"PascalCase identifier", "a.go", `id := "TfsazLinks." + n; ns := "Toolspell.Tfsaz.Links"`, nil},
		{"uppercase without underscore", "a.md", "TFSAZ is great.", []string{"misspelled name"}},
		{"environment variable", "a.md", "Set TFSAZ_CONFIG.", nil},
		{"mixed case", "a.md", "TfsAz and TFSAz.", []string{"misspelled name"}},
		{"split name", "a.md", "The TfsAz tool and tfs-az.", []string{"misspelled name"}},
		{"Go comment product", "a.go", "// the systems tfsaz supports", []string{"lowercase product name:"}},
		{"Go comment command", "a.go", "// run tfsaz route setup first", nil},
		{"Go string literal", "a.go", `exec.LookPath("tfsaz")`, nil},
		{"diagnostic prefix", "a.go", `fmt.Fprintln(os.Stderr, "tfsaz:", err)`, nil},
		{"version output", "a.go", `fmt.Fprintln(w, "tfsaz 0.1.0")`, nil},
		{"Git config value", "a.go", `"remote.origin.vcs=tfsaz"`, nil},
		{"Windows path", "a.go", "`\\tfsaz\\route-redirect-`", nil},
		{"marker on the line", "a.go", `const v = "tfsaz remote helper 1" // naming-allow: protocol string`, nil},
		{"marker above", "a.go", "// naming-allow: protocol string\nconst v = \"tfsaz remote helper 1\"", nil},
		{"marker without reason", "a.go", `const v = "tfsaz remote helper 1" // naming-allow:`, []string{"lowercase product name:", "write the `az` shim"}},
		{"file marker", "a.md", "<!-- naming-allow-file: lists forms -->\ntfsaz, Tfsaz, TFSAZ", nil},
		{"TFS alone", "a.md", "Connect to TFS.", []string{"write Azure DevOps Server"}},
		{"TFS in a path", "a.go", `"https://x.invalid/TFS/DefaultCollection"`, nil},
		{"VSTS in a field name", "a.go", `"Microsoft.VSTS.Common.Priority"`, nil},
		{"command concatenation", "a.go", `"tfsaz "+topic, "Usage: tfsaz %s"`, nil},
		{"future command in a code span", "a.md", "Add `tfsaz trust check`.", nil},
		{"TFS in a header", "a.go", `req.Header.Set("X-TFS-FedAuthRedirect", "Suppress")`, nil},
		{"on-prem", "a.md", "An on-prem server.", []string{`write "on-premises"`}},
		{"on-premises", "a.md", "An on-premises server.", nil},
		{"real URL", "a.go", "// prints the real URL", []string{"write native URL"}},
		{"phrase in a code span", "a.md", "The flag `--on-prem` and `TFS`.", nil},
		{"the CLI", "a.go", "// lets the CLI resolve it", []string{"write TFSaz or Microsoft's Azure CLI"}},
		{"stock az", "a.md", "Unlike stock `az`, it", []string{"write Microsoft's Azure CLI"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := namingProblems(tc.file, tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("problems = %v, want %q", got, tc.want)
			}
			for i, p := range got {
				if !strings.HasPrefix(p.reason, tc.want[i]) {
					t.Errorf("problem %d = %q, want prefix %q", i, p.reason, tc.want[i])
				}
			}
		})
	}
}
