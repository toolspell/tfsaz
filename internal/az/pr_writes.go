package az

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func runPRWrite(ctx context.Context, c *Client, scope Scope, command string, opts map[string]string) (any, error) {
	switch command {
	case "repos pr create":
		return createPullRequest(ctx, c, scope, opts)
	case "repos pr update":
		return updatePullRequest(ctx, c, scope, opts)
	case "repos pr reviewer add", "repos pr reviewer remove":
		return changePullRequestReviewers(ctx, c, command == "repos pr reviewer add", opts)
	default:
		return nil, fmt.Errorf("unsupported pull request write command %q", command)
	}
}

func createPullRequest(ctx context.Context, c *Client, scope Scope, opts map[string]string) (any, error) {
	project, repository := requestedRepository(scope, opts)
	if project == "" || repository == "" {
		return nil, fmt.Errorf("repos pr create requires a project and repository")
	}
	source, target := opts["source-branch"], opts["target-branch"]
	if source == "" || target == "" {
		return nil, fmt.Errorf("repos pr create requires --source-branch and --target-branch")
	}
	sourceRef, targetRef := pullRequestWriteRef(source), pullRequestWriteRef(target)
	if sourceRef == targetRef {
		return nil, fmt.Errorf("the source branch and target branch must be different")
	}

	body, completion, err := pullRequestWriteFields(opts)
	if err != nil {
		return nil, err
	}
	body["sourceRefName"], body["targetRefName"] = sourceRef, targetRef
	if _, ok := body["title"]; !ok {
		body["title"] = "Merge " + source + " to " + target
	}

	followUp := make(map[string]any)
	if len(completion) > 0 {
		followUp["completionOptions"] = completion
	}
	// Azure DevOps needs a subsequent update to enable auto-completion.
	// Verify the identity before creating the pull request.
	if opts["auto-complete"] == "true" {
		if err := setPullRequestAutoComplete(ctx, c, followUp, true); err != nil {
			return nil, err
		}
	}

	path := []string{project, "_apis", "git", "repositories", repository, "pullrequests"}
	query := url.Values{"api-version": {gitReadVersion}}
	var raw json.RawMessage
	if err := writeJSON(ctx, c, http.MethodPost, path, query, "application/json", body, &raw); err != nil {
		return nil, fmt.Errorf("create pull request: %w", err)
	}
	row, err := decodePullRequestRow(raw)
	if err != nil {
		return nil, fmt.Errorf("pull request was created, but its response could not be read: %w", err)
	}
	id := pullRequestNumber(row)
	if id == "" {
		return nil, fmt.Errorf("pull request was created, but the response is missing its ID")
	}
	if len(followUp) == 0 {
		return finishPullRequestWrite(row, id, scope)
	}
	path, err = pullRequestWritePath(row, id)
	if err != nil {
		return nil, fmt.Errorf("pull request %s was created, but could not set completion options: %w", id, err)
	}
	row, err = patchPullRequest(ctx, c, path, followUp)
	if err != nil {
		return nil, fmt.Errorf("pull request %s was created, but could not set completion options: %w", id, err)
	}
	return finishPullRequestWrite(row, id, scope)
}

func updatePullRequest(ctx context.Context, c *Client, scope Scope, opts map[string]string) (any, error) {
	body, completion, err := pullRequestWriteFields(opts)
	if err != nil {
		return nil, err
	}
	status := opts["status"]
	switch status {
	case "":
	case "active", "abandoned", "completed":
		body["status"] = status
	default:
		return nil, fmt.Errorf("--status expects active, abandoned, or completed")
	}
	id, raw, err := getPullRequestByID(ctx, c, opts["id"])
	if err != nil {
		return nil, err
	}
	row, err := decodePullRequestRow(raw)
	if err != nil {
		return nil, err
	}
	if autoComplete, ok := opts["auto-complete"]; ok {
		if err := setPullRequestAutoComplete(ctx, c, body, autoComplete == "true"); err != nil {
			return nil, err
		}
	}

	if len(completion) > 0 || status == "completed" {
		existing, _ := row["completionOptions"].(map[string]any)
		for field, value := range existing {
			if _, replaced := completion[field]; !replaced {
				completion[field] = value
			}
		}
		if len(completion) > 0 {
			body["completionOptions"] = completion
		}
	}
	if status == "completed" {
		lastSource, _ := row["lastMergeSourceCommit"].(map[string]any)
		commitID, _ := lastSource["commitId"].(string)
		if commitID == "" {
			return nil, fmt.Errorf("pull request %s has no last merge source commit to complete", id)
		}
		body["lastMergeSourceCommit"] = map[string]string{"commitId": commitID}
	}
	path, err := pullRequestWritePath(row, id)
	if err != nil {
		return nil, err
	}
	row, err = patchPullRequest(ctx, c, path, body)
	if err != nil {
		return nil, fmt.Errorf("update pull request %s: %w", id, err)
	}
	return finishPullRequestWrite(row, id, scope)
}

func pullRequestWriteFields(opts map[string]string) (body, completion map[string]any, err error) {
	body, completion = make(map[string]any), make(map[string]any)
	for _, field := range []string{"title", "description"} {
		if value, ok := opts[field]; ok {
			body[field] = value
		}
	}
	for _, option := range []string{"draft", "auto-complete", "squash", "delete-source-branch", "transition-work-items"} {
		raw, supplied := opts[option]
		if !supplied {
			continue
		}
		if raw != "true" && raw != "false" {
			return nil, nil, fmt.Errorf("--%s expects true or false", option)
		}
		value := raw == "true"
		switch option {
		case "draft":
			body["isDraft"] = value
		case "squash":
			completion["squashMerge"] = value
			// An existing explicit strategy takes precedence over squashMerge.
			strategy := "noFastForward"
			if value {
				strategy = "squash"
			}
			completion["mergeStrategy"] = strategy
		case "delete-source-branch":
			completion["deleteSourceBranch"] = value
		case "transition-work-items":
			completion["transitionWorkItems"] = value
		}
	}
	if message, ok := opts["merge-commit-message"]; ok {
		completion["mergeCommitMessage"] = message
	}
	return body, completion, nil
}

func setPullRequestAutoComplete(ctx context.Context, c *Client, body map[string]any, enabled bool) error {
	const emptyIdentityID = "00000000-0000-0000-0000-000000000000"
	id := emptyIdentityID
	if enabled {
		user, err := c.authenticatedUser(ctx)
		if err != nil {
			return fmt.Errorf("resolve auto-completion identity: %w", err)
		}
		id = user.ID
	}
	body["autoCompleteSetBy"] = map[string]string{"id": id}
	return nil
}

func pullRequestWriteRef(branch string) string {
	// Create passes the literal branch in JSON. List arguments may be
	// percent-encoded to accommodate Azure CLI's query handling.
	for _, prefix := range []string{"refs/heads/", "refs/pull/", "refs/tags/"} {
		if strings.HasPrefix(branch, prefix) {
			return branch
		}
	}
	return "refs/heads/" + branch
}

func pullRequestWritePath(row map[string]any, id string) ([]string, error) {
	if pullRequestNumber(row) != id {
		return nil, fmt.Errorf("Azure DevOps response does not identify pull request %s", id)
	}
	repository, _ := row["repository"].(map[string]any)
	project, _ := repository["project"].(map[string]any)
	repositoryID, _ := repository["id"].(string)
	projectID, _ := project["id"].(string)
	if !guidPattern.MatchString(repositoryID) || !guidPattern.MatchString(projectID) {
		return nil, fmt.Errorf("pull request %s response is missing its project or repository GUID", id)
	}
	return []string{projectID, "_apis", "git", "repositories", repositoryID, "pullrequests", id}, nil
}

func patchPullRequest(ctx context.Context, c *Client, path []string, body map[string]any) (map[string]any, error) {
	var raw json.RawMessage
	query := url.Values{"api-version": {gitReadVersion}}
	if err := writeJSON(ctx, c, http.MethodPatch, path, query, "application/json", body, &raw); err != nil {
		return nil, err
	}
	return decodePullRequestRow(raw)
}

func finishPullRequestWrite(row map[string]any, id string, scope Scope) (map[string]any, error) {
	if pullRequestNumber(row) != id {
		return nil, fmt.Errorf("Azure DevOps response does not identify pull request %s", id)
	}
	if err := setPullRequestWebLinks(row, id, scope, true); err != nil {
		return nil, err
	}
	return row, nil
}

func changePullRequestReviewers(ctx context.Context, c *Client, add bool, opts map[string]string) ([]json.RawMessage, error) {
	if strings.TrimSpace(opts["reviewers"]) == "" {
		return nil, fmt.Errorf("--reviewers requires one or more identities")
	}
	required := opts["required"]
	if required != "" && required != "true" && required != "false" {
		return nil, fmt.Errorf("--required expects true or false")
	}
	id, raw, err := getPullRequestByID(ctx, c, opts["id"])
	if err != nil {
		return nil, err
	}
	row, err := decodePullRequestRow(raw)
	if err != nil {
		return nil, err
	}
	path, err := pullRequestWritePath(row, id)
	if err != nil {
		return nil, err
	}
	path = append(path, "reviewers")

	// Resolve the entire batch before sending any mutation. An invalid or
	// ambiguous identity must leave the reviewers as they were.
	type reviewer struct {
		ID         string `json:"id"`
		IsRequired bool   `json:"isRequired,omitempty"`
	}
	var viewer Identity
	var reviewers []reviewer
	seen := make(map[string]bool)
	for _, login := range strings.Split(opts["reviewers"], "\x00") {
		reviewerID, err := pullRequestFilterID(ctx, c, &viewer, login)
		if err != nil {
			return nil, fmt.Errorf("resolve pull request reviewer: %w", err)
		}
		key := strings.ToLower(reviewerID)
		if !seen[key] {
			reviewers = append(reviewers, reviewer{ID: reviewerID, IsRequired: required == "true"})
			seen[key] = true
		}
	}
	query := url.Values{"api-version": {gitReadVersion}}
	if add {
		var raw json.RawMessage
		if err := writeJSON(ctx, c, http.MethodPost, path, query, "application/json", reviewers, &raw); err != nil {
			return nil, fmt.Errorf("add reviewers to pull request %s: %w", id, err)
		}
		var rows []json.RawMessage
		if json.Unmarshal(raw, &rows) != nil {
			var page struct {
				Value []json.RawMessage `json:"value"`
			}
			if err := json.Unmarshal(raw, &page); err == nil {
				rows = page.Value
			}
		}
		if rows == nil {
			return nil, fmt.Errorf("Azure DevOps accepted the reviewer addition for pull request %s, but did not return a reviewer list", id)
		}
		return rows, nil
	}

	for index, reviewer := range reviewers {
		target := append(path, reviewer.ID)
		if err := writeJSON(ctx, c, http.MethodDelete, target, query, "application/json", nil, nil); err != nil {
			return nil, fmt.Errorf("remove reviewers from pull request %s after %d successful removals: %w", id, index, err)
		}
	}
	rows, err := getRepoPage(ctx, c, path, query)
	if err != nil {
		return nil, fmt.Errorf("reviewers were removed from pull request %s, but could not read the remaining reviewers: %w", id, err)
	}
	return rows, nil
}
