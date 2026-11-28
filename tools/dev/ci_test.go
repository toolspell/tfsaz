package main

import (
	"slices"
	"testing"
)

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
		{"mock module", []string{"live-tests/devops-server-mock/go.sum"}, true},
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

func TestShardTests(t *testing.T) {
	out := "TestB\nExampleA\nBenchmarkX\nTestUTF8_é\nok  \tgithub.com/toolspell/tfsaz\t0.1s\nTestA\nFuzzC\nTestB\n?   \tgithub.com/toolspell/tfsaz/tools/x\t[no test files]\nok  \tgithub.com/toolspell/tfsaz/internal/az\t0.2s\n"
	names := listedTests(out)
	if want := []string{"ExampleA", "FuzzC", "TestA", "TestB", "TestUTF8_é"}; !slices.Equal(names, want) {
		t.Fatalf("listedTests = %q, want %q", names, want)
	}
	for total := 1; total <= len(names)+1; total++ {
		var all []string
		for index := 1; index <= total; index++ {
			all = append(all, shardTests(names, index, total)...)
		}
		slices.Sort(all)
		if !slices.Equal(all, names) {
			t.Errorf("%d shards run %q, want each of %q once", total, all, names)
		}
	}
	for _, tc := range []struct {
		shard        string
		index, total int
		ok           bool
	}{
		{"1/1", 1, 1, true}, {"3/3", 3, 3, true}, {"0/3", 0, 0, false}, {"4/3", 0, 0, false}, {"1/0", 0, 0, false}, {"2", 0, 0, false}, {"a/b", 0, 0, false},
	} {
		index, total, err := parseShard(tc.shard)
		if index != tc.index || total != tc.total || (err == nil) != tc.ok {
			t.Errorf("parseShard(%q) = %d, %d, %v", tc.shard, index, total, err)
		}
	}
}
