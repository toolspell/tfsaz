package main

import (
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestSelectRepos(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		want    []string
		wantErr string
	}{
		{args: nil, want: names(repos)},
		{args: []string{"--repo", "t3code"}, want: []string{"t3code"}},
		{args: []string{"--repo=azure-cli"}, want: []string{"azure-cli"}},
		{args: []string{"--repo", "nope"}, wantErr: `unknown repo "nope"`},
		{args: []string{"t3code"}, wantErr: `unexpected argument "t3code"`},
		{args: []string{"--app", "t3code"}, wantErr: "not defined"},
	} {
		got, err := selectRepos(repos, tc.args, io.Discard)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("selectRepos(%q) error = %v, want %q", tc.args, err, tc.wantErr)
			}
			continue
		}
		if err != nil || !slices.Equal(names(got), tc.want) {
			t.Errorf("selectRepos(%q) = %q, %v, want %q", tc.args, names(got), err, tc.want)
		}
	}
}

func TestRepoPins(t *testing.T) {
	commit := regexp.MustCompile(`^[0-9a-f]{40}$`)
	seen := map[string]bool{}
	for _, repo := range repos {
		if seen[repo.name] {
			t.Errorf("duplicate name %q", repo.name)
		}
		seen[repo.name] = true
		if !commit.MatchString(repo.commit) {
			t.Errorf("%s: commit %q is not a full SHA", repo.name, repo.commit)
		}
		if repo.tag == "" || !strings.HasPrefix(repo.url, "https://") {
			t.Errorf("%s: needs a tag and an HTTPS URL", repo.name)
		}
		if len(repo.paths) == 0 || strings.HasPrefix(repo.paths[0], "!") {
			t.Errorf("%s: sparse paths must start with an include", repo.name)
		}
	}
}
