package main

import (
	"bytes"
	"errors"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestSelectRepos(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		want       []string
		wantLatest bool
		wantErr    string
	}{
		{args: nil, want: names(repos)},
		{args: []string{"-latest"}, want: names(repos), wantLatest: true},
		{args: []string{"-latest", "--repo", "knack"}, want: []string{"knack"}, wantLatest: true},
		{args: []string{"--repo", "t3code"}, want: []string{"t3code"}},
		{args: []string{"--repo=azure-cli"}, want: []string{"azure-cli"}},
		{args: []string{"--repo", "nope"}, wantErr: `unknown repo "nope"`},
		{args: []string{"t3code"}, wantErr: `unexpected argument "t3code"`},
		{args: []string{"--app", "t3code"}, wantErr: "not defined"},
	} {
		got, latest, err := selectRepos(repos, tc.args, io.Discard)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("selectRepos(%q) error = %v, want %q", tc.args, err, tc.wantErr)
			}
			continue
		}
		if err != nil || !slices.Equal(names(got), tc.want) || latest != tc.wantLatest {
			t.Errorf("selectRepos(%q) = %q, %v, %v, want %q, %v", tc.args, names(got), latest, err, tc.want, tc.wantLatest)
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
		matched := 0
		for _, ch := range repo.channels {
			if ch.pattern.MatchString(repo.tag) {
				matched++
			}
		}
		if matched != 1 {
			t.Errorf("%s: tag %q matches %d release channels, want exactly 1", repo.name, repo.tag, matched)
		}
	}
}

func TestParseTags(t *testing.T) {
	out := "aaa\trefs/tags/v1.0.0\n" +
		"bbb\trefs/tags/v1.1.0\n" +
		"ccc\trefs/tags/v1.1.0^{}\n" +
		"ddd\trefs/heads/main\n\n"
	got := parseTags(out)
	want := map[string]string{"v1.0.0": "aaa", "v1.1.0": "ccc"}
	if !maps.Equal(got, want) {
		t.Errorf("parseTags = %v, want %v", got, want)
	}
}

func TestNewerTag(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"azure-cli-2.100.0", "azure-cli-2.93.0", true},
		{"azure-cli-2.93.0", "azure-cli-2.93.0", false},
		{"v0.1.13-nightly.20261126.5301", "v0.1.13-nightly.20261125.5258", true},
		{"v0.1.13-nightly.20261125.5245", "v0.1.13-nightly.20261125.5258", false},
		{"20261201.1", "20261117.2", true},
		{"v1.2.1", "v1.2", true},
	} {
		if got := newerTag(tc.a, tc.b); got != tc.want {
			t.Errorf("newerTag(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestReportLatest(t *testing.T) {
	repo := referenceRepo{
		name: "app",
		url:  "https://example.invalid/app.git",
		tag:  "v0.2.0-nightly.20261125.7",
		channels: []channel{
			{"stable", tagPattern(`v\d+\.\d+\.\d+`)},
			{"nightly", tagPattern(`v\d+\.\d+\.\d+-nightly\.\d+\.\d+`)},
			{"beta", tagPattern(`v\d+\.\d+\.\d+-beta`)},
		},
	}
	tags := map[string]string{
		"v0.1.9":                    "1111111111111111111111111111111111111111",
		"v0.1.10":                   "2222222222222222222222222222222222222222",
		"v0.2.0-nightly.20261125.7": "3333333333333333333333333333333333333333",
		"v0.2.0-nightly.20261126.9": "4444444444444444444444444444444444444444",
		"v0.1.10x":                  "5555555555555555555555555555555555555555",
	}
	var out bytes.Buffer
	err := reportLatest([]referenceRepo{repo}, func(url string) (map[string]string, error) {
		if url != repo.url {
			t.Errorf("listed %q, want %q", url, repo.url)
		}
		return tags, nil
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"app   stable   -                          v0.1.10                    2222222222  not pinned",
		"app   nightly  v0.2.0-nightly.20261125.7  v0.2.0-nightly.20261126.9  4444444444  newer release",
		"app   beta     -                          -                          -           no matching tag",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}

	err = reportLatest([]referenceRepo{repo}, func(string) (map[string]string, error) {
		return nil, errors.New("offline")
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "app: offline") {
		t.Errorf("reportLatest error = %v, want the repo name and cause", err)
	}
}
