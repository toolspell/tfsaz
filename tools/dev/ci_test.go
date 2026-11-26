package main

import "testing"

func TestTestsNeeded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"docs only", []string{"README.md", "docs/user/install.md", "docs/verification/x.png"}, false},
		{"skills and templates", []string{".agents/skills/reference-t3code/SKILL.md", ".claude", ".github/ISSUE_TEMPLATE/bug.yml", ".github/pull_request_template.md"}, false},
		{"repo config", []string{".github/labels.json", ".github/scripts/pr-size.cjs", ".agents/context-budget.json", ".github/dependabot.yml", ".gitleaks.toml", ".golangci.yml", ".githooks/pre-commit"}, false},
		{"review bots", []string{".coderabbit.config.ts", ".macroscope/ignore.md", ".macroscope/check-run-agents/test-signal.md"}, false},
		{"tool pin", []string{"tools/golangci-lint/go.mod", "tools/act/go.sum"}, false},
		{"Go code", []string{"README.md", "internal/az/cli.go"}, true},
		{"markdown fixture", []string{"internal/az/testdata/readme.md"}, true},
		{"root module files", []string{"go.sum"}, true},
		{"dev tool code", []string{"tools/dev/ci.go"}, true},
		{"workflow", []string{".github/workflows/labels.yml"}, true},
		{"unknown file", []string{".gitignore"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testsNeeded(tc.files); got != tc.want {
				t.Errorf("testsNeeded(%q) = %v, want %v", tc.files, got, tc.want)
			}
		})
	}
}
