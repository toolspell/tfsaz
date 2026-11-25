package az

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/gofrs/flock"
)

const (
	setupPhasePreparing = "preparing"
	setupPhaseActive    = "active"
	setupPhaseRestoring = "restoring"

	setupOriginKey  = "remote.origin.url"
	setupSSHKey     = "core.sshCommand"
	setupVCSKey     = "remote.origin.vcs"
	setupBindingKey = "remote.origin.tfsazBinding"
)

func setupRegistryPath() (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	return setupRegistryForConfig(path), nil
}

func setupRegistryForConfig(path string) string {
	path = filepath.Clean(path)
	if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(dir, filepath.Base(path))
	}
	return path + ".repo-setups.json"
}

func readSetupRecords() ([]setupRecord, error) {
	path, err := setupRegistryPath()
	if err != nil {
		return nil, err
	}
	return readSetupRegistry(path)
}

func setupSSHPath(registry, gitDir string) string {
	key := registry
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return filepath.Join(gitDir, fmt.Sprintf("tfsaz-%x.ssh.conf", sha256.Sum256([]byte(key))))
}

func readSetupRegistry(path string) ([]setupRecord, error) {
	data, err := readSetupFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []setupRecord
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) {
		return nil, errors.New("invalid repository setup registry: expected an array")
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("invalid repository setup registry: %w", err)
	}
	for i, r := range records {
		if !filepath.IsAbs(r.Repo) || !filepath.IsAbs(r.GitDir) ||
			r.OriginalURL == "" || r.RemoteURL == "" || r.OriginalURL == r.RemoteURL {
			return nil, errors.New("invalid repository setup record")
		}
		if r.Transport == "" {
			if r.SSHConfig != setupSSHPath(path, r.GitDir) || r.SSHCommand == "" {
				return nil, errors.New("invalid repository SSH setup record")
			}
		} else if r.SSHConfig != "" || r.SSHCommand != "" || r.SSHURL != "" ||
			validatePreserveSetupRecord(r) != nil {
			return nil, errors.New("invalid repository transport setup record")
		}
		switch r.Phase {
		case setupPhasePreparing, setupPhaseActive, setupPhaseRestoring:
		default:
			return nil, errors.New("invalid repository setup phase")
		}
		if setupRecordIndex(records[:i], r.GitDir) >= 0 {
			return nil, errors.New("duplicate repository setup record")
		}
	}
	return records, nil
}

func setupRecordIndex(records []setupRecord, gitDir string) int {
	return slices.IndexFunc(records, func(r setupRecord) bool {
		return sameSetupPath(r.GitDir, gitDir)
	})
}

func sameSetupPath(a, b string) bool {
	return a == b || (runtime.GOOS == "windows" && strings.EqualFold(a, b))
}

func readSetupFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}

func setupFileExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	return true, nil
}

func acquireSetupLock(path string) (func() error, error) {
	if _, err := setupFileExists(path); err != nil {
		return nil, err
	}
	lock := flock.New(path)
	ok, err := lock.TryLock()
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	if !ok {
		return nil, errors.Join(fmt.Errorf("another repository setup transaction holds %s; retry", path), lock.Close())
	}
	return lock.Close, nil
}

func syncSetupDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Publish a complete file from the same directory. An SSH config is installed
// exclusively so setup cannot overwrite a file it did not create.
func publishSetupFile(path string, data []byte, replace bool) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".tfsaz-setup-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	if replace {
		err = os.Rename(file.Name(), path)
	} else {
		err = os.Link(file.Name(), path)
	}
	if err != nil {
		return err
	}
	return syncSetupDir(filepath.Dir(path))
}

type setupRegistry struct {
	path    string
	records []setupRecord
}

func (s *setupRegistry) update(r setupRecord, phase string) error {
	records := append([]setupRecord{}, s.records...)
	i := setupRecordIndex(records, r.GitDir)
	r.Phase = phase
	switch {
	case phase == "":
		records = slices.Delete(records, i, i+1)
	case i < 0:
		records = append(records, r)
	default:
		records[i] = r
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	// Keep the in-memory journal aligned with the published file even if the
	// directory sync fails after a successful rename.
	err = publishSetupFile(s.path, append(data, '\n'), true)
	if err == nil {
		s.records = records
	} else if current, readErr := readSetupRegistry(s.path); readErr == nil {
		s.records = current
	}
	return err
}

type setupCheckout struct {
	repo   string
	gitDir string
}

func (c setupCheckout) git(ctx context.Context, args ...string) ([]byte, error) {
	return c.gitCommand(ctx, args...).Output()
}

func (c setupCheckout) gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.repo
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		switch strings.ToUpper(key) {
		case "GIT_CONFIG", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	return cmd
}

func (c setupCheckout) gitPath(ctx context.Context, flag string) (string, error) {
	out, err := c.git(ctx, "rev-parse", "--path-format=absolute", flag)
	if err != nil {
		return "", err
	}
	path := strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "\r")
	return filepath.EvalSymlinks(path)
}

func findSetupCheckout(ctx context.Context, path string) (setupCheckout, error) {
	c := setupCheckout{repo: path}
	for _, target := range []struct {
		flag string
		dest *string
	}{{"--show-toplevel", &c.repo}, {"--git-common-dir", &c.gitDir}} {
		resolved, err := c.gitPath(ctx, target.flag)
		if errors.Is(err, exec.ErrNotFound) {
			return setupCheckout{}, errors.New("Git executable not found; install Git and ensure git is on PATH")
		}
		if err != nil {
			return setupCheckout{}, fmt.Errorf("find Git checkout at %q: %w", path, err)
		}
		*target.dest = resolved
	}
	return c, nil
}

func resolveSetupCheckout(ctx context.Context, r setupRecord) (setupCheckout, error) {
	c, err := findSetupCheckout(ctx, r.Repo)
	if err == nil {
		if sameSetupPath(c.gitDir, r.GitDir) {
			return c, nil
		}
		err = errors.New("checkout Git directory has changed")
	}

	common := setupCheckout{repo: r.GitDir, gitDir: r.GitDir}
	worktrees, listErr := common.listWorktrees(ctx, "--git-dir="+r.GitDir)
	if listErr != nil {
		return setupCheckout{}, errors.Join(err, listErr)
	}
	for _, worktree := range worktrees {
		if worktree.bare {
			continue
		}
		c, checkoutErr := findSetupCheckout(ctx, worktree.path)
		if checkoutErr == nil && sameSetupPath(c.gitDir, r.GitDir) {
			return c, nil
		}
	}
	return setupCheckout{}, err
}

func (c setupCheckout) configValues(ctx context.Context, file, key string) ([]string, error) {
	args := []string{"config"}
	if file != "" {
		args = append(args, "--file", file, "--no-includes")
	}
	out, err := c.git(ctx, append(args, "--null", "--get-all", key)...)
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Git configuration for %s: %w", key, err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), nil
}

type setupGitState struct {
	url      string
	ssh      string
	vcs      string
	binding  string
	preserve bool
}

func (c setupCheckout) localState(ctx context.Context) (setupGitState, error) {
	return c.loadLocalState(ctx, false)
}

func (c setupCheckout) loadLocalState(ctx context.Context, preserve bool) (setupGitState, error) {
	path := filepath.Join(c.gitDir, "config")
	if exists, err := setupFileExists(path); err != nil || !exists {
		return setupGitState{}, errors.Join(err, fmt.Errorf("expected a regular Git config at %s", path))
	}
	urls, err := c.configValues(ctx, path, setupOriginKey)
	if err != nil {
		return setupGitState{}, err
	}
	if len(urls) != 1 || urls[0] == "" {
		return setupGitState{}, errors.New("expected exactly one common-config origin URL")
	}
	state := setupGitState{url: urls[0], preserve: preserve}
	for _, entry := range []struct {
		key string
		out *string
	}{{setupVCSKey, &state.vcs}, {setupBindingKey, &state.binding}, {setupSSHKey, &state.ssh}} {
		if preserve && entry.key == setupSSHKey {
			continue
		}
		values, err := c.configValues(ctx, path, entry.key)
		if err != nil {
			return setupGitState{}, err
		}
		if len(values) > 1 || (len(values) == 1 && values[0] == "") {
			return setupGitState{}, fmt.Errorf("expected at most one nonempty common-config value for %s", entry.key)
		}
		if len(values) == 1 {
			*entry.out = values[0]
		}
	}
	return state, nil
}

func (c setupCheckout) verifyEffective(ctx context.Context, state setupGitState) error {
	if !state.preserve {
		for _, key := range []string{"GIT_SSH_COMMAND", "GIT_SSH", "GIT_SSH_VARIANT"} {
			if _, present := os.LookupEnv(key); present {
				return fmt.Errorf("%s customizes Git SSH", key)
			}
		}
	}
	for _, entry := range []struct {
		key  string
		want []string
	}{
		{setupOriginKey, []string{state.url}},
		{"remote.origin.pushurl", nil},
		{setupVCSKey, nilIfEmptySetupValue(state.vcs)},
		{setupBindingKey, nilIfEmptySetupValue(state.binding)},
		{"remote.origin.uploadpack", nil},
		{"remote.origin.receivepack", nil},
		{setupSSHKey, nilIfEmptySetupValue(state.ssh)},
		{"ssh.variant", nil},
	} {
		if state.preserve && (entry.key == setupSSHKey || entry.key == "ssh.variant") {
			continue
		}
		values, err := c.configValues(ctx, "", entry.key)
		if err != nil {
			return err
		}
		if entry.key == "ssh.variant" && len(values) == 1 && values[0] == "ssh" {
			continue
		}
		if !slices.Equal(values, entry.want) {
			if entry.key == setupSSHKey && state.ssh == "" {
				return errors.New("core.sshCommand is customized in Git configuration; tfsaz needs the checkout SSH command and OpenSSH on PATH (use git config --show-origin --get-all core.sshCommand to locate it)")
			}
			return fmt.Errorf("effective Git setting %s differs from the expected values", entry.key)
		}
	}

	for _, push := range []bool{false, true} {
		args := []string{"remote", "get-url", "--all"}
		if push {
			args = append(args, "--push")
		}
		out, err := c.git(ctx, append(args, "origin")...)
		if err != nil {
			return fmt.Errorf("resolve Git origin URL: %w", err)
		}
		if strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "\r") != state.url {
			return errors.New("Git rewrites the origin fetch or push URL")
		}
	}
	return nil
}

func nilIfEmptySetupValue(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

type setupWorktree struct {
	path             string
	bare             bool
	prunableOrLocked bool
}

func (c setupCheckout) listWorktrees(ctx context.Context, gitOptions ...string) ([]setupWorktree, error) {
	out, err := c.git(ctx, append(gitOptions, "worktree", "list", "--porcelain", "-z")...)
	if err != nil {
		return nil, fmt.Errorf("list linked Git worktrees: %w", err)
	}
	var worktrees []setupWorktree
	for record := range strings.SplitSeq(string(out), "\x00\x00") {
		line, attributes, _ := strings.Cut(record, "\x00")
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		worktree := setupWorktree{path: path}
		for field := range strings.SplitSeq(attributes, "\x00") {
			name, _, _ := strings.Cut(field, " ")
			switch name {
			case "bare":
				worktree.bare = true
			case "prunable", "locked":
				worktree.prunableOrLocked = true
			}
		}
		worktrees = append(worktrees, worktree)
	}
	return worktrees, nil
}

// Linked worktrees share the configuration being edited. Validate effective
// settings in each worktree because includes can depend on the worktree or branch.
func (c setupCheckout) verifyWorktrees(ctx context.Context, state setupGitState) error {
	worktrees, err := c.listWorktrees(ctx)
	if err != nil {
		return err
	}
	for _, entry := range worktrees {
		// A prunable record (or an offline locked worktree) has no active
		// local Git process whose effective configuration we can inspect.
		if entry.prunableOrLocked {
			if _, err := os.Lstat(filepath.Join(entry.path, ".git")); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		worktree := setupCheckout{repo: entry.path, gitDir: c.gitDir}
		gitDir, err := worktree.gitPath(ctx, "--git-common-dir")
		if err != nil {
			return fmt.Errorf("%s: find Git common directory: %w", entry.path, err)
		}
		// Git can still list a stale worktree whose path was reused.
		if !sameSetupPath(gitDir, c.gitDir) {
			continue
		}
		if err := worktree.verifyEffective(ctx, state); err != nil {
			return fmt.Errorf("%s: %w", entry.path, err)
		}
	}
	return nil
}

const setupGitLockPrefix = ".tfsaz-git-lock-"

type setupGitLock struct {
	c     setupCheckout
	proof string
}

// A hard link identifies our Git lock after a crash. The repository advisory
// lock is held throughout recovery and excludes managers using other registries.
func lockSetupGit(c setupCheckout) (*setupGitLock, error) {
	path := filepath.Join(c.gitDir, "config.lock")
	target, err := os.Lstat(path)
	if err == nil {
		if err := reclaimSetupGitLock(c.gitDir, path, target); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.CreateTemp(c.gitDir, setupGitLockPrefix)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		os.Remove(file.Name())
		return nil, err
	}
	if err := os.Link(file.Name(), path); err != nil {
		os.Remove(file.Name())
		return nil, fmt.Errorf("acquire Git config lock: %w", err)
	}
	lock := &setupGitLock{c: c, proof: file.Name()}
	if err := syncSetupDir(c.gitDir); err != nil {
		return nil, errors.Join(err, lock.close())
	}
	return lock, nil
}

func reclaimSetupGitLock(dir, path string, target os.FileInfo) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), setupGitLockPrefix) {
			continue
		}
		proof := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(proof)
		if err == nil && info.Mode().IsRegular() && os.SameFile(target, info) {
			if err := os.Remove(path); err != nil {
				return err
			}
			return os.Remove(proof)
		}
	}
	return fmt.Errorf("Git config lock %s is already present; Git or another process may own it", path)
}

func (l *setupGitLock) close() error {
	path := filepath.Join(l.c.gitDir, "config.lock")
	target, lockErr := os.Lstat(path)
	if lockErr == nil {
		info, err := os.Lstat(l.proof)
		switch {
		case err != nil:
			lockErr = err
		case os.SameFile(info, target):
			lockErr = os.Remove(path)
		}
	} else if errors.Is(lockErr, os.ErrNotExist) {
		lockErr = nil
	}
	// Preserve the proof when removal failed so a retry can identify the lock.
	if lockErr != nil {
		return lockErr
	}
	if err := os.Remove(l.proof); err != nil {
		return err
	}
	return syncSetupDir(l.c.gitDir)
}

func (l *setupGitLock) change(ctx context.Context, from, to setupGitState) error {
	c := l.c
	actual, err := c.loadLocalState(ctx, from.preserve)
	if err != nil {
		return err
	}
	if actual != from {
		return setupGitDriftError(from)
	}
	path := filepath.Join(c.gitDir, "config")
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	data, err := readSetupFile(path)
	if err != nil {
		return err
	}
	stage, err := os.CreateTemp(c.gitDir, ".tfsaz-git-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	_, writeErr := stage.Write(data)
	if err := errors.Join(writeErr, stage.Close()); err != nil {
		return err
	}

	for _, entry := range []struct {
		key      string
		from, to string
	}{{setupOriginKey, from.url, to.url}, {setupSSHKey, from.ssh, to.ssh},
		{setupVCSKey, from.vcs, to.vcs}, {setupBindingKey, from.binding, to.binding}} {
		if entry.from == entry.to {
			continue
		}
		args := []string{"config", "--file", stage.Name(), "--no-includes"}
		if entry.to == "" {
			args = append(args, "--unset-all", entry.key)
		} else {
			args = append(args, "--replace-all", entry.key, entry.to)
		}
		if _, err := c.git(ctx, args...); err != nil {
			return fmt.Errorf("stage Git configuration for %s: %w", entry.key, err)
		}
	}
	staged, err := os.ReadFile(stage.Name())
	if err != nil {
		return err
	}
	current, err := readSetupFile(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, data) {
		return errors.New("Git config changed during staging")
	}
	lockPath := path + ".lock"
	lockInfo, err := os.Lstat(lockPath)
	if err != nil {
		return err
	}
	if err := writeSetupGitLock(l.proof, lockInfo, staged, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Rename(lockPath, path); err != nil {
		return err
	}
	return syncSetupDir(c.gitDir)
}

func writeSetupGitLock(path string, lockInfo os.FileInfo, staged []byte, mode os.FileMode) (err error) {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	proof, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(lockInfo, proof) {
		return errors.New("Git config lock changed during staging")
	}
	if _, err := file.Write(staged); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	return file.Sync()
}

func setupStates(r setupRecord) (original, active setupGitState) {
	if r.Transport != "" {
		return setupGitState{url: r.OriginalURL, preserve: true},
			setupGitState{url: r.RemoteURL, vcs: "tfsaz", binding: encodePreserveBinding(r), preserve: true}
	}
	return setupGitState{url: r.OriginalURL}, setupGitState{url: r.RemoteURL, ssh: r.SSHCommand}
}

func setupGitDriftError(state setupGitState) error {
	if state.preserve {
		return errors.New("Git origin URL or transport binding has drifted")
	}
	return errors.New("Git origin URL or SSH command has drifted")
}

func checkSetupSSH(r setupRecord, expected []byte) (bool, error) {
	data, err := readSetupFile(r.SSHConfig)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(data, expected) {
		return false, errors.New("generated SSH configuration has drifted")
	}
	info, err := os.Lstat(r.SSHConfig)
	if err != nil {
		return false, err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		return false, errors.New("generated SSH configuration must have mode 0600")
	}
	return true, nil
}

func verifySetupGitState(ctx context.Context, c setupCheckout, r setupRecord) error {
	if r.Phase != setupPhaseActive {
		return fmt.Errorf("repository setup transaction is %s", r.Phase)
	}
	_, active := setupStates(r)
	actual, err := c.loadLocalState(ctx, active.preserve)
	if err != nil {
		return err
	}
	if active != actual {
		return setupGitDriftError(active)
	}
	if err := c.verifyWorktrees(ctx, active); err != nil {
		return err
	}
	if active.preserve {
		return checkPreserveHelper(ctx, c)
	}
	ssh, err := renderSetupSSH(r)
	if err != nil {
		return err
	}
	exists, err := checkSetupSSH(r, ssh)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("generated SSH configuration is missing")
	}
	return nil
}

func verifySetup(ctx context.Context, c setupCheckout, r setupRecord, cfg config) error {
	if err := verifySetupGitState(ctx, c, r); err != nil {
		return err
	}
	if r.Transport != "" {
		expected, err := makePreserveSetupRecord(cfg, setupCheckout{repo: r.Repo, gitDir: r.GitDir}, r.OriginalURL, r.ProfileID)
		if err != nil {
			return fmt.Errorf("repository setup routing has drifted: %w", err)
		}
		expected.Phase = r.Phase
		if expected != r {
			return errors.New("repository setup routing has drifted; run repo restore then repo setup")
		}
		return checkPreserveSetupAccess(ctx, c, r, "origin")
	}
	override := ""
	if strings.HasPrefix(strings.ToLower(r.OriginalURL), "https://") {
		override = r.SSHURL
	}
	expected, err := makeSetupRecord(ctx, cfg, r.Repo, r.GitDir, r.OriginalURL, override, r.ProfileID, r.SSHConfig)
	if err != nil {
		return fmt.Errorf("repository setup routing has drifted: %w", err)
	}
	expected.Phase = r.Phase
	if expected != r {
		return errors.New("repository setup routing or SSH helper path has changed; run repo restore then repo setup with the installed helper")
	}
	return checkSetupSSHAccess(ctx, c, r, "origin")
}

type setupTransition int

const (
	activateSetup setupTransition = iota
	restoreSetup
	recoverSetup
	rollbackSetup
	rollbackRestore
)

// settleSetup puts a journaled transaction at an endpoint. Only the two
// recorded Git states are accepted, so an intervening manual edit is preserved.
func (s *setupRegistry) settleSetup(ctx context.Context, lock *setupGitLock, r setupRecord, transition setupTransition) error {
	c := lock.c
	activate := transition == activateSetup || transition == rollbackRestore
	// Restore only edits the two owned local values. An external override
	// may itself be the reason the user needs to undo this setup.
	checkEffective := transition == activateSetup
	original, active := setupStates(r)
	from, err := c.loadLocalState(ctx, original.preserve)
	if err != nil {
		return err
	}
	if from != original && from != active {
		return setupGitDriftError(from)
	}
	if checkEffective {
		if err := c.verifyWorktrees(ctx, from); err != nil {
			return err
		}
	}
	var data []byte
	var exists bool
	if !original.preserve {
		data, err = renderSetupSSH(r)
		if err != nil {
			return err
		}
		exists, err = checkSetupSSH(r, data)
		if transition == activateSetup && from == original && (exists || err != nil) {
			return errors.Join(err, errors.New("SSH configuration path changed during setup"), s.update(r, ""))
		}
		if err != nil {
			return err
		}
	}
	to := original
	if transition == activateSetup && from != original {
		return errors.New("Git settings or the SSH configuration path changed during setup")
	}
	if transition == restoreSetup {
		if from != active {
			return errors.New("managed Git settings have drifted")
		}
		if err := s.update(r, setupPhaseRestoring); err != nil {
			return err
		}
	}
	if activate {
		to = active
		if !original.preserve && !exists {
			if err := publishSetupFile(r.SSHConfig, data, false); err != nil {
				if transition == activateSetup && errors.Is(err, os.ErrExist) {
					return errors.Join(err, s.update(r, ""))
				}
				return err
			}
		}
	}
	if from != to {
		if err := lock.change(ctx, from, to); err != nil {
			return err
		}
	}
	if checkEffective {
		if err := c.verifyWorktrees(ctx, to); err != nil {
			return err
		}
		if to.preserve {
			if err := checkPreserveSetupAccess(ctx, c, r, "origin"); err != nil {
				return err
			}
		}
	}
	if activate {
		return s.update(r, setupPhaseActive)
	}
	if exists {
		if _, err := checkSetupSSH(r, data); err != nil {
			return err
		}
		if err := os.Remove(r.SSHConfig); err != nil {
			return err
		}
		if err := syncSetupDir(c.gitDir); err != nil {
			return err
		}
	}
	return s.update(r, "")
}

func (s *setupRegistry) prepare(ctx context.Context, c setupCheckout, cfg config, sshURL, profile string) (setupRecord, error) {
	state, err := c.localState(ctx)
	if err != nil {
		return setupRecord{}, err
	}
	if sshURL != "" && !strings.HasPrefix(strings.ToLower(state.url), "https://") {
		return setupRecord{}, errors.New("--ssh-url requires an HTTPS origin")
	}
	if state.ssh != "" {
		return setupRecord{}, errors.New("Git already has a custom core.sshCommand")
	}
	if state.vcs != "" || state.binding != "" {
		return setupRecord{}, errors.New("Git origin already has a transport helper or repository binding")
	}
	if err := c.verifyWorktrees(ctx, state); err != nil {
		return setupRecord{}, err
	}
	sshPath := setupSSHPath(s.path, c.gitDir)
	if exists, err := setupFileExists(sshPath); err != nil || exists {
		return setupRecord{}, errors.Join(err, fmt.Errorf("SSH configuration path already exists or cannot be inspected: %s", sshPath))
	}
	r, err := makeSetupRecord(ctx, cfg, c.repo, c.gitDir, state.url, sshURL, profile, sshPath)
	if err != nil {
		return setupRecord{}, err
	}
	return r, nil
}

func preparePreserveSetup(ctx context.Context, c setupCheckout, cfg config, profile string) (setupRecord, error) {
	state, err := c.loadLocalState(ctx, true)
	if err != nil {
		return setupRecord{}, err
	}
	if state.vcs != "" || state.binding != "" {
		return setupRecord{}, errors.New("Git origin already has a transport helper or repository binding")
	}
	if err := c.verifyWorktrees(ctx, state); err != nil {
		return setupRecord{}, err
	}
	return makePreserveSetupRecord(cfg, c, state.url, profile)
}

func (s *setupRegistry) gitTransaction(c setupCheckout, run func(*setupGitLock) error) (err error) {
	lock, err := lockSetupGit(c)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.close()) }()
	return run(lock)
}

func (s *setupRegistry) mutate(ctx context.Context, c setupCheckout, op repoSetupArgs) error {
	index := setupRecordIndex(s.records, c.gitDir)
	preserve := op.mode != repoSetupManagedSSH
	var cfg config
	if op.action == "setup" {
		var err error
		cfg, err = loadConfig()
		if err != nil {
			return err
		}
	}
	if index >= 0 {
		r := s.records[index]
		if op.mode == repoSetupDefault {
			preserve = r.Transport != ""
		}
		if r.Phase != setupPhaseActive {
			if err := s.gitTransaction(c, func(lock *setupGitLock) error {
				return s.settleSetup(ctx, lock, r, recoverSetup)
			}); err != nil {
				return fmt.Errorf("recover %s repository setup: %w", r.Phase, err)
			}
			if op.action == "restore" {
				return nil
			}
		} else if op.action == "setup" {
			if preserve != (r.Transport != "") {
				return errors.New("repository already has a setup with a different transport mode; restore it first")
			}
			if op.profile != "" && op.profile != r.ProfileID {
				return errors.New("repository already has a setup with a different profile; restore it first")
			}
			if op.sshURL != "" {
				if !strings.HasPrefix(strings.ToLower(r.OriginalURL), "https://") {
					return errors.New("--ssh-url requires an HTTPS origin")
				}
				ssh, err := nativeSetupURL(op.sshURL)
				if err != nil || ssh.String() != r.SSHURL {
					return errors.New("repository already has a setup with a different SSH URL; restore it first")
				}
			}
			return verifySetup(ctx, c, r, cfg)
		} else {
			err := s.gitTransaction(c, func(lock *setupGitLock) error {
				return s.settleSetup(ctx, lock, r, restoreSetup)
			})
			return s.rollback(ctx, c, r, err, setupPhaseRestoring, rollbackRestore)
		}
	} else if op.action == "restore" {
		return errors.New("no setup recorded for this repository")
	}

	var r setupRecord
	var err error
	if preserve {
		r, err = preparePreserveSetup(ctx, c, cfg, op.profile)
	} else {
		r, err = s.prepare(ctx, c, cfg, op.sshURL, op.profile)
	}
	if err != nil {
		return err
	}
	// Network reads run under the manager lock, before taking Git's lock.
	if preserve {
		if err := checkPreserveHelper(ctx, c); err != nil {
			return err
		}
		if err := checkPreserveSetupAccess(ctx, c, r, r.OriginalURL); err != nil {
			return err
		}
	} else {
		if err := checkSetupSSHAccess(ctx, c, r, r.RemoteURL); err != nil {
			return err
		}
	}
	err = s.gitTransaction(c, func(lock *setupGitLock) error {
		current, err := c.loadLocalState(ctx, preserve)
		if err != nil {
			return err
		}
		original, _ := setupStates(r)
		if current != original {
			if preserve {
				return errors.New("Git configuration changed during the native transport check; retry setup")
			}
			return errors.New("Git configuration changed during the SSH check; retry setup")
		}
		if err := c.verifyWorktrees(ctx, current); err != nil {
			return err
		}
		if err := s.update(r, setupPhasePreparing); err != nil {
			return err
		}
		return s.settleSetup(ctx, lock, r, activateSetup)
	})
	return s.rollback(ctx, c, r, err, setupPhasePreparing, rollbackSetup)
}

func (s *setupRegistry) rollback(ctx context.Context, c setupCheckout, r setupRecord, cause error, pending string, transition setupTransition) error {
	if cause == nil {
		return nil
	}
	i := setupRecordIndex(s.records, c.gitDir)
	if i < 0 || s.records[i].Phase != pending {
		return cause
	}
	err := s.gitTransaction(c, func(lock *setupGitLock) error {
		return s.settleSetup(context.WithoutCancel(ctx), lock, r, transition)
	})
	if err != nil {
		return errors.Join(cause, fmt.Errorf("rollback pending; retry repository operation: %w", err))
	}
	return cause
}

type repoSetupMode int

const (
	repoSetupDefault repoSetupMode = iota
	repoSetupPreserve
	repoSetupManagedSSH
)

type repoSetupArgs struct {
	action  string
	path    string
	sshURL  string
	profile string
	mode    repoSetupMode
}

func parseRepoSetupArgs(args []string) (repoSetupArgs, error) {
	if len(args) == 0 {
		return repoSetupArgs{}, errors.New("expected repo setup, restore, show, list, or verify")
	}
	op := repoSetupArgs{action: args[0]}
	switch op.action {
	case "setup", "restore", "show", "list", "verify":
	default:
		return op, errors.New("expected repo setup, restore, show, list, or verify")
	}
	options := true
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if options && arg == "--" {
			options = false
			continue
		}
		if options && strings.HasPrefix(arg, "-") {
			key, value, joined := strings.Cut(arg, "=")
			if op.action == "setup" && (key == "--preserve-transport" || key == "--managed-ssh") {
				if joined || op.mode != repoSetupDefault {
					return op, errors.New("expected at most one of --preserve-transport or --managed-ssh, without a value")
				}
				op.mode = repoSetupPreserve
				if key == "--managed-ssh" {
					op.mode = repoSetupManagedSSH
				}
				continue
			}
			if op.action != "setup" || (key != "--ssh-url" && key != "--profile") {
				return op, fmt.Errorf("unsupported option for repo %s", op.action)
			}
			dest := &op.sshURL
			if key == "--profile" {
				dest = &op.profile
			}
			if *dest != "" {
				return op, fmt.Errorf("duplicate %s", key)
			}
			if !joined && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				value = args[i]
			}
			if value == "" {
				return op, fmt.Errorf("%s requires a value", key)
			}
			*dest = value
			continue
		}
		if op.path != "" || arg == "" {
			return op, errors.New("expected a single checkout path")
		}
		op.path = arg
	}
	if op.sshURL != "" {
		if op.mode == repoSetupPreserve {
			return op, errors.New("--preserve-transport and --ssh-url are mutually exclusive")
		}
		op.mode = repoSetupManagedSSH
	}
	return op, nil
}

func (a app) runRepo(ctx context.Context, args []string) (err error) {
	op, err := parseRepoSetupArgs(args)
	if err != nil {
		return usageError{err}
	}
	path, err := setupRegistryPath()
	if err != nil {
		return err
	}
	var checkout setupCheckout
	mutate := op.action == "setup" || op.action == "restore"
	if mutate || op.action == "show" || op.path != "" {
		repo := op.path
		if repo == "" {
			repo = a.cwd
		} else if !filepath.IsAbs(repo) {
			repo = filepath.Join(a.cwd, repo)
		}
		checkout, err = findSetupCheckout(ctx, repo)
		if err != nil {
			return err
		}
	}
	if mutate {
		release, err := acquireSetupLock(path + ".lock")
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, release()) }()
		releaseRepo, err := acquireSetupLock(filepath.Join(checkout.gitDir, "tfsaz-repo-setup.lock"))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, releaseRepo()) }()
	}
	records, err := readSetupRegistry(path)
	if err != nil {
		return err
	}
	if mutate {
		registry := setupRegistry{path: path, records: records}
		if err := registry.mutate(ctx, checkout, op); err != nil {
			return err
		}
		_, err := fmt.Fprintf(a.stdout, "%s complete for %s\n", op.action, checkout.repo)
		return err
	}
	if op.path != "" || op.action == "show" {
		i := setupRecordIndex(records, checkout.gitDir)
		if i < 0 {
			return errors.New("no setup recorded for this repository")
		}
		records = records[i : i+1]
	}
	if op.action == "show" {
		r := records[0]
		transport, target, mode := r.Transport, r.OriginalURL, "preserve"
		if transport == "" {
			transport, target, mode = "ssh", r.SSHURL, "legacy"
		}
		if _, err := fmt.Fprintf(a.stdout, "Checkout: %s\nRemote: origin\nPhase: %s\nProfile: %s\nRecorded origin: %s\nOriginal origin: %s\nRecorded Git target: %s\nTransport: %s (%s)\n",
			checkout.repo, r.Phase, r.ProfileID, r.RemoteURL, r.OriginalURL, target, transport, mode); err != nil {
			return err
		}
		if r.Phase == setupPhaseActive {
			if err := verifySetupGitState(ctx, checkout, r); err != nil {
				return fmt.Errorf("recorded origin mapping differs from the current Git setup: %w", err)
			}
		}
		return nil
	}
	if op.action == "list" {
		if len(records) == 0 {
			_, err := fmt.Fprintln(a.stdout, "No repository setups")
			return err
		}
		for _, r := range records {
			repo := r.Repo
			if op.path != "" {
				repo = checkout.repo
			} else if c, err := resolveSetupCheckout(ctx, r); err == nil {
				repo = c.repo
			}
			if _, err := fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\n", r.Phase, repo, r.ProfileID, r.Host); err != nil {
				return err
			}
		}
		return nil
	}
	if len(records) == 0 {
		_, err := fmt.Fprintln(a.stdout, "No repository setups")
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	var failures []error
	for _, r := range records {
		c := checkout
		if op.path == "" {
			c, err = resolveSetupCheckout(ctx, r)
		}
		if err == nil && !sameSetupPath(c.gitDir, r.GitDir) {
			err = errors.New("checkout Git directory has changed")
		}
		if err == nil {
			err = verifySetup(ctx, c, r, cfg)
		}
		if err != nil {
			repo := r.Repo
			if c.repo != "" {
				repo = c.repo
			}
			failures = append(failures, fmt.Errorf("%s: %w", repo, err))
		} else if _, err := fmt.Fprintf(a.stdout, "verified %s\n", c.repo); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}
