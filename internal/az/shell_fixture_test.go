package az

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testExecutable(path string) string {
	if runtime.GOOS == "windows" {
		return path + ".exe"
	}
	return path
}

func copyTestExecutable(t *testing.T, source, destination string) {
	t.Helper()
	binary, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, binary, 0700); err != nil {
		t.Fatal(err)
	}
}

func writeShellFixture(t *testing.T, path, script string) string {
	t.Helper()
	executable := testExecutable(path)
	scriptPath := executable
	if runtime.GOOS == "windows" {
		shell := filepath.Join(testWindowsGitTools(t), "sh.exe")
		if _, err := os.Stat(shell); err != nil {
			t.Skipf("Git for Windows shell unavailable: %v", err)
		}
		t.Setenv("TFSAZ_TEST_SHELL", shell)
		build := exec.CommandContext(t.Context(), "go", "build", "-o", executable, "testdata/shell-fixture.go")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build shell fixture: %v\n%s", err, output)
		}
		scriptPath += ".sh"
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return executable
}

func testWindowsGitTools(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.CommandContext(t.Context(), "git", "--exec-path").Output()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(strings.TrimSpace(string(gitPath)), "..", "..", "..", "usr", "bin")
}

func setupTestSSH(t *testing.T, dir, script string) {
	t.Helper()
	writeShellFixture(t, filepath.Join(dir, "ssh"), script)
	if runtime.GOOS == "windows" {
		// Git for Windows puts its exec path and bundled programs before PATH
		// when invoking core.sshCommand through its shell.
		t.Setenv("GIT_EXEC_PATH", dir)
	}
}
