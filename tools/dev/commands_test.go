package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestUndoSetup(t *testing.T) {
	for _, tc := range []struct {
		name, hooksPath, want string
	}{
		{"set by setup", ".githooks", ""},
		{"not set", "", ""},
		{"set by someone else", "custom-hooks", "custom-hooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Chdir(t.TempDir())
			runGit(t, "init", "-q")
			if tc.hooksPath != "" {
				runGit(t, "config", "core.hooksPath", tc.hooksPath)
			}
			if !undoSetup() {
				t.Fatal("undoSetup() = false")
			}
			got, _ := exec.Command("git", "config", "--get", "core.hooksPath").Output()
			if strings.TrimSpace(string(got)) != tc.want {
				t.Errorf("core.hooksPath = %q, want %q", strings.TrimSpace(string(got)), tc.want)
			}
		})
	}
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}
