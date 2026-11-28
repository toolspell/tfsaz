package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestUndoSetup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, tc := range []struct {
		name, hooksPath, want string
	}{
		{"set by setup", ".githooks", ""},
		{"not set", "", ""},
		{"set by someone else", "custom-hooks", "custom-hooks"},
		{"leading space", " .githooks", " .githooks"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateGit(t)
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

// isolateGit keeps a test's Git commands away from the developer's config and
// checkout. A Git hook exports the repository variables, and they override
// the working directory.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
}
