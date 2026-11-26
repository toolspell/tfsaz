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
		{"leading space", " .githooks", " .githooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			// A Git hook exports these, and they override the working directory.
			for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
				t.Setenv(name, "")
				_ = os.Unsetenv(name)
			}
			t.Chdir(t.TempDir())
			runGit(t, "init", "-q")
			if tc.hooksPath != "" {
				runGit(t, "config", "core.hooksPath", tc.hooksPath)
			}
			if !undoSetup() {
				t.Fatal("undoSetup() = false")
			}
			got, _ := exec.Command("git", "config", "--get", "core.hooksPath").Output()
			if got := strings.TrimSuffix(string(got), "\n"); got != tc.want {
				t.Errorf("core.hooksPath = %q, want %q", got, tc.want)
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
