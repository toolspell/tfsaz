package az

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
)

func (a app) repoCommand(ctx context.Context, client *Client, scope Scope, command string, opts map[string]string) (any, error) {
	switch command {
	case "repos create":
		return createRepository(ctx, client, scope, opts)
	case "repos pr checkout":
		return nil, a.checkoutPullRequest(ctx, client, scope, opts)
	default:
		return nil, fmt.Errorf("unsupported command %q", command)
	}
}

func createRepository(ctx context.Context, client *Client, scope Scope, opts map[string]string) (map[string]json.RawMessage, error) {
	project, _ := requestedRepository(scope, opts)
	name := opts["name"]
	if project == "" || strings.TrimSpace(name) == "" {
		return nil, errors.New("repos create requires a project and --name")
	}
	var repository map[string]json.RawMessage
	path := []string{project, "_apis", "git", "repositories"}
	query := url.Values{"api-version": {gitReadVersion}}
	if err := writeJSON(ctx, client, http.MethodPost, path, query, "application/json", map[string]string{"name": name}, &repository); err != nil {
		return nil, err
	}
	normalizeRepositoryURLs(repository)
	return repository, nil
}

func normalizeRepositoryURLs(repository map[string]json.RawMessage) {
	// Server may return literal spaces in the collection portion of a
	// clone URL. Preserve the advertised transport and existing escapes.
	for _, key := range []string{"remoteUrl", "webUrl", "sshUrl"} {
		var raw string
		if json.Unmarshal(repository[key], &raw) != nil || !strings.Contains(raw, " ") {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.IsAbs() {
			repository[key], _ = json.Marshal(u.String())
		}
	}
}

func pullRequestRemote(opts map[string]string) string {
	if name := opts["remote-name"]; name != "" {
		return name
	}
	return "origin"
}

func (cfg config) repoCheckoutScope(ctx context.Context, cwd string, opts map[string]string) (*Profile, Scope, error) {
	explicit := opts["organization"]
	if explicit == "" && opts["detect"] == "false" {
		return nil, Scope{}, errors.New("an explicit organization is required when detection is disabled")
	}
	git := setupCheckout{repo: cwd}
	remote := pullRequestRemote(opts)
	out, err := git.git(ctx, "remote", "get-url", "--", remote)
	if err != nil {
		return nil, Scope{}, checkoutGitError(ctx, err, "read the selected Git remote")
	}
	profile, scope := cfg.remoteScope(strings.TrimSuffix(strings.TrimSuffix(string(out), "\n"), "\r"))
	if profile == nil {
		return nil, Scope{}, errors.New("the selected Git remote does not identify a configured collection")
	}
	if explicit != "" {
		requested, _ := cfg.selectBase(explicit)
		if requested == nil {
			return nil, Scope{}, errors.New("organization does not match a configured collection")
		}
		if requested != profile {
			return nil, Scope{}, errors.New("the selected Git remote belongs to a different collection than --organization")
		}
	}
	return profile, scope, nil
}

func (a app) checkoutPullRequest(ctx context.Context, client *Client, scope Scope, opts map[string]string) error {
	id, raw, err := getPullRequestByID(ctx, client, opts["id"])
	if err != nil {
		return err
	}
	var pr struct {
		ID         int32           `json:"pullRequestId"`
		SourceRef  string          `json:"sourceRefName"`
		ForkSource json.RawMessage `json:"forkSource"`
		Repository struct {
			Name    string `json:"name"`
			Project struct {
				Name string `json:"name"`
			} `json:"project"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return fmt.Errorf("read pull request %s: %w", id, err)
	}
	if strconv.Itoa(int(pr.ID)) != id {
		return fmt.Errorf("Azure DevOps response does not identify pull request %s", id)
	}
	if len(pr.ForkSource) != 0 && string(pr.ForkSource) != "null" {
		return errors.New("checking out an Azure DevOps pull request from a fork is unsupported")
	}
	if scope.Repository == "" || scope.Project == "" ||
		!strings.EqualFold(scope.Repository, pr.Repository.Name) || !strings.EqualFold(scope.Project, pr.Repository.Project.Name) {
		return errors.New("the pull request belongs to a different repository than the selected Git remote")
	}
	git := setupCheckout{repo: a.cwd}
	branch, ok := strings.CutPrefix(pr.SourceRef, "refs/heads/")
	if !ok {
		return fmt.Errorf("pull request %s response contains an invalid source branch", id)
	}
	checked, err := git.git(ctx, "check-ref-format", "--branch", branch)
	if ctx.Err() != nil || errors.Is(err, exec.ErrNotFound) {
		return checkoutGitError(ctx, err, "validate the pull request source branch")
	}
	if err != nil || strings.TrimRight(string(checked), "\r\n") != branch {
		return fmt.Errorf("pull request %s response contains an invalid source branch", id)
	}
	return switchPullRequestBranch(ctx, git, pullRequestRemote(opts), branch, pr.SourceRef)
}

func switchPullRequestBranch(ctx context.Context, git setupCheckout, remote, branch, sourceRef string) error {
	tracking := "refs/remotes/" + remote + "/" + branch
	// An explicit destination also works in a single-branch clone. Ignore
	// configured ref mappings so fetch cannot update a user's local branches.
	if _, err := git.git(ctx, "fetch", "--quiet", "--no-tags", "--no-prune", "--no-prune-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--refmap=", "--", remote, "+"+sourceRef+":"+tracking); err != nil {
		return checkoutGitError(ctx, err, "fetch the pull request source branch")
	}
	if _, err := git.git(ctx, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return checkoutGitError(ctx, err, "check for the local pull request branch")
		}
		return createPullRequestBranch(ctx, git, branch, tracking)
	}
	if _, err := git.git(ctx, "switch", "--quiet", "--no-guess", "--no-recurse-submodules", "--", branch); err != nil {
		return checkoutGitError(ctx, err, "switch to the local pull request branch")
	}
	return nil
}

func createPullRequestBranch(ctx context.Context, git setupCheckout, branch, tracking string) error {
	args := []string{"--quiet", "--no-recurse-submodules", "-c", branch, "--", tracking}
	_, err := git.git(ctx, append([]string{"switch", "--track"}, args...)...)
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(string(exit.Stderr), "cannot set up tracking information") {
		return checkoutGitError(ctx, err, "create the local pull request branch")
	}
	// A narrow clone may have fetched the ref explicitly without a configured
	// mapping for it. Keep that clone's Git configuration intact.
	if _, err := git.git(ctx, append([]string{"switch", "--no-track"}, args...)...); err != nil {
		return checkoutGitError(ctx, err, "create the local pull request branch")
	}
	return nil
}

func checkoutGitError(ctx context.Context, err error, action string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("could not %s: %w", action, ctx.Err())
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("Git executable not found; install Git and ensure git is on PATH")
	}
	// Git may include credential-bearing remote URLs in its error output.
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch {
		case strings.Contains(string(exit.Stderr), "would be overwritten by checkout"):
			return fmt.Errorf("could not %s: local changes would be overwritten", action)
		case strings.Contains(string(exit.Stderr), "already used by worktree"), strings.Contains(string(exit.Stderr), "already checked out"):
			return fmt.Errorf("could not %s: the branch is checked out in another worktree", action)
		}
	}
	return fmt.Errorf("could not %s; check the Git checkout and remote configuration", action)
}
