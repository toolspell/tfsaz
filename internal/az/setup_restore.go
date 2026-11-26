package az

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// forceRestore writes the recorded original values over whatever the checkout
// holds now. It reports each value it replaces before it changes anything.
func (s *setupRegistry) forceRestore(ctx context.Context, c setupCheckout, r setupRecord) error {
	original, _ := setupStates(r)
	config := filepath.Join(c.gitDir, "config")
	return s.gitTransaction(c, func(lock *setupGitLock) error {
		if exists, err := setupFileExists(config); err != nil || !exists {
			return errors.Join(err, fmt.Errorf("expected a regular Git config at %s", config))
		}
		keys := []setupGitEdit{{setupOriginKey, original.url}, {setupSSHKey, ""}}
		if original.preserve {
			keys = []setupGitEdit{keys[0], {setupVCSKey, ""}, {setupBindingKey, ""}}
		}
		var report strings.Builder
		var edits []setupGitEdit
		for _, key := range keys {
			values, err := c.configValues(ctx, config, key.key)
			if err != nil {
				return err
			}
			if slices.Equal(values, nilIfEmptySetupValue(key.value)) {
				continue
			}
			edits = append(edits, key)
			writeForceRestoreEntry(&report, key.key, values, key.value)
		}
		removeSSH := false
		if !original.preserve {
			current, err := forceRestoreSSHState(r)
			if err != nil {
				return err
			}
			if current != "" {
				removeSSH = true
				fmt.Fprintf(&report, "  %s\n    current:  %s\n    recorded: (deleted)\n", r.SSHConfig, current)
			}
		}
		if report.Len() > 0 {
			if _, err := fmt.Fprintf(s.out, "Overwriting in %s:\n%s", c.repo, report.String()); err != nil {
				return err
			}
		}
		if err := s.update(r, setupPhaseRestoring); err != nil {
			return err
		}
		if len(edits) > 0 {
			if err := lock.rewrite(ctx, edits); err != nil {
				return err
			}
		}
		if removeSSH {
			if err := os.Remove(r.SSHConfig); err != nil {
				return err
			}
			if err := syncSetupDir(c.gitDir); err != nil {
				return err
			}
		}
		return s.update(r, "")
	})
}

func writeForceRestoreEntry(w *strings.Builder, key string, current []string, recorded string) {
	fmt.Fprintf(w, "  %s\n", key)
	if len(current) == 0 {
		current = []string{"(unset)"}
	}
	for _, value := range current {
		fmt.Fprintf(w, "    current:  %s\n", value)
	}
	if recorded == "" {
		recorded = "(unset)"
	}
	fmt.Fprintf(w, "    recorded: %s\n", recorded)
}

// The SSH configuration path is reserved for this checkout and registry, so
// force restore deletes it even when its content no longer matches.
func forceRestoreSSHState(r setupRecord) (string, error) {
	info, err := os.Lstat(r.SSHConfig)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", r.SSHConfig)
	}
	expected, err := renderSetupSSH(r)
	if err != nil {
		return "", err
	}
	data, err := readSetupFile(r.SSHConfig)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(data, expected) {
		return "edited, differs from the generated file", nil
	}
	return "generated file", nil
}

// setupLookupPath makes a path comparable with recorded checkout paths, which
// have their symbolic links resolved, even when the path itself is gone.
func setupLookupPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	missing := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, missing)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
		missing = filepath.Join(filepath.Base(dir), missing)
	}
}

func matchingSetupRecords(records []setupRecord, path string) []setupRecord {
	path = setupLookupPath(path)
	var matches []setupRecord
	for _, r := range records {
		if sameSetupPath(r.Repo, path) || sameSetupPath(r.GitDir, path) {
			matches = append(matches, r)
		}
	}
	return matches
}

// checkSetupGone refuses while anything of the clone remains that repo
// restore could still undo.
func checkSetupGone(ctx context.Context, r setupRecord) error {
	if c, err := resolveSetupCheckout(ctx, r); err == nil {
		return fmt.Errorf("checkout %s still exists; run repo restore %s instead", c.repo, setupCommandPath(c.repo))
	}
	if _, err := os.Lstat(r.GitDir); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return fmt.Errorf("Git directory %s of checkout %s still exists; run repo restore in a checkout that uses it", r.GitDir, r.Repo)
	}
	return nil
}

func (a app) forgetSetup(ctx context.Context, registry, path string) (err error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.cwd, path)
	}
	path = setupLookupPath(path)
	release, err := acquireSetupLock(registry + ".lock")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	records, err := readSetupRegistry(registry)
	if err != nil {
		return err
	}
	matches := matchingSetupRecords(records, path)
	if len(matches) == 0 {
		return fmt.Errorf("no setup recorded for %s; repo list shows the recorded paths", path)
	}
	for _, r := range matches {
		if err := checkSetupGone(ctx, r); err != nil {
			return err
		}
	}
	s := setupRegistry{path: registry, records: records, store: a.store, out: a.stdout}
	for _, r := range matches {
		if err := s.update(r, ""); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(a.stdout, "forgot %s\nOriginal origin: %s\nRecorded origin: %s\n", r.Repo, r.OriginalURL, r.RemoteURL); err != nil {
			return err
		}
		if r.SSHConfig != "" {
			if _, err := fmt.Fprintf(a.stdout, "Generated SSH configuration: %s\n", filepath.Base(r.SSHConfig)); err != nil {
				return err
			}
		}
	}
	return nil
}

// missingSetupCheckoutError explains a restore whose checkout is gone and
// names the command that can still act on its record.
func missingSetupCheckoutError(ctx context.Context, registry, path string, cause error) error {
	records, err := readSetupRegistry(registry)
	if err != nil {
		return cause
	}
	matches := matchingSetupRecords(records, path)
	if len(matches) == 0 {
		return cause
	}
	if err := checkSetupGone(ctx, matches[0]); err != nil {
		return fmt.Errorf("%w; %w", cause, err)
	}
	return fmt.Errorf("checkout %s no longer exists, so there is nothing to restore; run repo forget %s to remove its setup record", matches[0].Repo, setupCommandPath(matches[0].Repo))
}

// setupCommandPath quotes a path for a command the user copies from an error,
// in the quoting of the platform's usual shell.
func setupCommandPath(path string) string {
	return setupCommandPathFor(runtime.GOOS, path)
}

func setupCommandPathFor(goos, path string) string {
	safe := "/:._-+"
	if goos == "windows" {
		safe += `\`
	}
	if path != "" && !strings.ContainsFunc(path, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune(safe, r)
	}) {
		return path
	}
	// Windows paths cannot contain double quotes. Double quotes work in cmd and
	// PowerShell, but PowerShell expands $ and backtick inside them.
	if goos == "windows" && !strings.ContainsAny(path, "$`") {
		return `"` + path + `"`
	}
	if goos == "windows" {
		return "'" + strings.ReplaceAll(path, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}
