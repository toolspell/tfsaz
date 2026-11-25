package az

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

const preserveHelperVersion = "tfsaz remote helper 1"

// The common Git config is the only source of routing information the helper
// needs. The journal lets the CLI and browser resolve the organization alias.
type preserveBinding struct {
	ProfileID     string `json:"profileId"`
	CollectionURL string `json:"collectionUrl"`
	Project       string `json:"project"`
	Repository    string `json:"repository"`
	Target        string `json:"target"`
}

func setupBinding(r setupRecord) preserveBinding {
	return preserveBinding{
		ProfileID: r.ProfileID, CollectionURL: r.CollectionURL, Project: r.Project,
		Repository: r.Repository, Target: r.OriginalURL,
	}
}

func encodePreserveBinding(r setupRecord) string {
	data, _ := json.Marshal(setupBinding(r))
	return string(data)
}

func (b preserveBinding) destination() (*url.URL, string, error) {
	invalid := errors.New("invalid managed Git transport binding")
	collection, ok := baseURL(b.CollectionURL)
	if !ok || collection.Path == "" || !profileID.MatchString(b.ProfileID) ||
		!validSegment(b.Project) || !validSegment(b.Repository) {
		return nil, "", invalid
	}
	target, err := preserveSetupTarget(b.Target)
	if err != nil {
		return nil, "", invalid
	}
	parts, _ := pathSegments(target)
	if !strings.EqualFold(parts[len(parts)-1], b.Repository) ||
		(!strings.EqualFold(b.Project, b.Repository) && !strings.EqualFold(parts[len(parts)-3], b.Project)) {
		return nil, "", invalid
	}
	host := setupHost(b.ProfileID, b.CollectionURL, b.Project, b.Repository)
	return target, preserveSetupRemote(target.Scheme, host, b.Project, b.Repository), nil
}

func validatePreserveSetupRecord(r setupRecord) error {
	target, alias, err := setupBinding(r).destination()
	if err != nil {
		return err
	}
	if target.Scheme != r.Transport || alias != r.RemoteURL ||
		r.Host != setupHost(r.ProfileID, r.CollectionURL, r.Project, r.Repository) {
		return errors.New("invalid managed Git transport registration")
	}
	return nil
}

func checkPreserveHelper(ctx context.Context, c setupCheckout) error {
	// Starting Git and a cold helper can take several seconds on Windows.
	ctx, cancel := context.WithTimeout(ctx, setupSSHCheckTimeout)
	defer cancel()
	output, err := c.git(ctx, "remote-tfsaz", "--version")
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("Git transport helper version check timed out: %w", ctx.Err())
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || strings.TrimSpace(string(output)) != preserveHelperVersion {
		return errors.New("Git could not find git-remote-tfsaz; run tfsaz shim install --dir DIR and put DIR on Git's PATH")
	}
	return nil
}

func checkPreserveSetupAccess(ctx context.Context, c setupCheckout, r setupRecord, remote string) error {
	ctx, cancel := context.WithTimeout(ctx, setupSSHCheckTimeout+3*time.Second)
	defer cancel()
	cmd := c.gitCommand(ctx, "ls-remote", "--", remote, "HEAD")
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=Never", "SSH_ASKPASS_REQUIRE=never")
	if r.Transport == "ssh" {
		target, err := preserveSetupTarget(r.OriginalURL)
		if err != nil {
			return err
		}
		ssh, err := loadPreserveSSH(ctx, c.repo)
		if err != nil {
			return err
		}
		command, variant, err := ssh.checkCommand(ctx, target)
		if err != nil {
			return err
		}
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+command, "GIT_SSH_VARIANT="+variant)
		detachPreserveSSHProbe(cmd)
	}
	cmd.WaitDelay = time.Second
	var stderr setupSSHCheckOutput
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		return nil
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ctx.Err()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s repository check timed out", strings.ToUpper(r.Transport))
	}
	message := strings.ToLower(stderr.data.String())
	if remote == "origin" && strings.Contains(message, "remote-tfsaz") {
		if err := checkPreserveHelper(ctx, c); err != nil {
			return err
		}
	}
	if r.Transport == "ssh" {
		return setupSSHCheckError(message)
	}
	switch {
	case strings.Contains(message, "certificate") || strings.Contains(message, "ssl") || strings.Contains(message, "tls"):
		return errors.New("HTTPS repository check could not verify TLS; check the Git trust configuration")
	case strings.Contains(message, "authentication") || strings.Contains(message, "terminal prompts disabled") ||
		strings.Contains(message, "401") || strings.Contains(message, "403"):
		return errors.New("HTTPS repository authentication failed; check your Git credential helper and repository read access")
	default:
		return errors.New("HTTPS repository read failed; check network access, Git credentials and repository read permission")
	}
}

func preserveGitValue(ctx context.Context, cwd, key string) (string, error) {
	args := []string{"config", "--null", "--get", key}
	cmd := exec.CommandContext(ctx, "git", args...)
	if cwd != "" {
		cmd = (setupCheckout{repo: cwd}).gitCommand(ctx, args...)
	}
	out, err := cmd.Output()
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read Git configuration for %s: %w", key, err)
	}
	return strings.TrimSuffix(string(out), "\x00"), nil
}
