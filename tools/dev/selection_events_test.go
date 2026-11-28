package main

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func staticPkgDirs() ([]string, error) { return testPkgDirs, nil }

func TestChangeSelectionEvents(t *testing.T) {
	t.Run("pull request layer base HEAD^1 counts only the last commit", func(t *testing.T) {
		f := newGitFixture(t)
		f.commit(map[string]string{"internal/az/x.go": "package az\n"})
		f.commit(map[string]string{"docs/a.md": "a\n"})
		got, why := changeSelection("HEAD^1", staticPkgDirs)
		selectionCase{files: []string{"docs/a.md"}}.check(t, got)
		if !strings.HasPrefix(why, "1 changed files since ") {
			t.Errorf("reason %q", why)
		}
		// The same two commits against the first one are not skipped.
		got, _ = changeSelection(f.base, staticPkgDirs)
		selectionCase{files: []string{"internal/az/x.go", "docs/a.md"}, packages: all, shards: 3, goGates: true}.check(t, got)
	})

	t.Run("merge group base several commits back counts all of them", func(t *testing.T) {
		f := newGitFixture(t)
		first := f.commit(map[string]string{"tools/live/help.go": "package main\n"})
		f.commit(map[string]string{"docs/a.md": "a\n"})
		f.commit(map[string]string{"live-tests/router/Dockerfile": "FROM scratch\n"})
		got, why := changeSelection(f.base, staticPkgDirs)
		selectionCase{files: []string{"tools/live/help.go", "docs/a.md", "live-tests/router/Dockerfile"}, packages: append(slices.Clone(toolsPkgs), "./tools/live"), shards: 1, goGates: true}.check(t, got)
		if !strings.HasPrefix(why, "3 changed files since ") {
			t.Errorf("reason %q", why)
		}
		got, why = changeSelection(first, staticPkgDirs)
		selectionCase{files: []string{"docs/a.md", "live-tests/router/Dockerfile"}, packages: livePkgs, shards: 1}.check(t, got)
		if !strings.HasPrefix(why, "2 changed files since ") {
			t.Errorf("reason %q", why)
		}
	})

	t.Run("push before SHA with a deleted file", func(t *testing.T) {
		f := newGitFixture(t)
		before := f.commit(map[string]string{"tools/live/old.go": "package main\n", "docs/old.md": "old\n"})
		f.commit(map[string]string{"tools/live/old.go": ""})
		got, why := changeSelection(before, staticPkgDirs)
		selectionCase{files: []string{"tools/live/old.go"}, packages: toolsPkgs, shards: 1, goGates: true}.check(t, got)
		if !strings.HasPrefix(why, "1 changed files since ") {
			t.Errorf("reason %q", why)
		}
	})

	t.Run("push deleting only a doc is skipped", func(t *testing.T) {
		f := newGitFixture(t)
		before := f.commit(map[string]string{"docs/old.md": "old\n"})
		f.commit(map[string]string{"docs/old.md": ""})
		got, _ := changeSelection(before, staticPkgDirs)
		selectionCase{files: []string{"docs/old.md"}}.check(t, got)
	})
}

func TestChangeSelectionFailsOpen(t *testing.T) {
	const missing = "0123456789abcdef0123456789abcdef01234567"
	pkgDirsErr := errors.New("go list: boom")
	tests := []struct {
		name    string
		prepare func(f *gitFixture) string
		pkgDirs func() ([]string, error)
		reason  string
		called  bool
	}{
		{"empty base", func(*gitFixture) string { return "" }, staticPkgDirs, "no base commit", false},
		{"all-zero SHA of a first push", func(*gitFixture) string { return strings.Repeat("0", 40) }, staticPkgDirs, "no base commit", false},
		{"SHA not in the clone", func(*gitFixture) string { return missing }, staticPkgDirs, "base " + missing + " is not in this clone", false},
		{"ref that does not resolve", func(*gitFixture) string { return "no-such-branch" }, staticPkgDirs, "base no-such-branch is not in this clone", false},
		{"no changed files", func(f *gitFixture) string { return f.commit(nil) }, staticPkgDirs, "no changed files", false},
		{"base equals HEAD", func(f *gitFixture) string { return f.git("rev-parse", "HEAD") }, staticPkgDirs, "no changed files", false},
		{"package list fails", func(f *gitFixture) string {
			f.commit(map[string]string{"docs/a.md": "a\n"})
			return f.base
		}, func() ([]string, error) { return nil, pkgDirsErr }, pkgDirsErr.Error(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newGitFixture(t)
			base := tc.prepare(f)
			called := false
			got, why := changeSelection(base, func() ([]string, error) {
				called = true
				return tc.pkgDirs()
			})
			if !reflect.DeepEqual(got, everything()) {
				t.Errorf("got %+v, want everything", got)
			}
			if why != tc.reason {
				t.Errorf("reason %q, want %q", why, tc.reason)
			}
			if called != tc.called {
				t.Errorf("pkgDirs called %v, want %v", called, tc.called)
			}
		})
	}
}

func TestMatrix(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  []matrixEntry
	}{
		{"no tests", []string{"docs/a.md"}, []matrixEntry{}},
		{"one shard", []string{"tools/live/help.go"}, []matrixEntry{
			{"macos-latest", "1/1"}, {"ubuntu-latest", "1/1"}, {"windows-latest", "1/1"},
		}},
		{"three shards", []string{"internal/az/cli.go"}, []matrixEntry{
			{"macos-latest", "1/1"}, {"ubuntu-latest", "1/1"},
			{"windows-latest", "1/3"}, {"windows-latest", "2/3"}, {"windows-latest", "3/3"},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectFor(tc.files, testPkgDirs).matrix()
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			if tc.name == "no tests" {
				if out, _ := json.Marshal(got); string(out) != "[]" {
					t.Errorf("JSON %s, want []", out)
				}
			}
		})
	}
}
