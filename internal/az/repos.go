package az

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	gitReadVersion    = "7.1"
	policyReadVersion = "7.1-preview.1"
)

func runRepos(ctx context.Context, client *Client, scope Scope, command string, opts map[string]string) (any, error) {
	switch command {
	case "repos show":
		project, name := requestedRepository(scope, opts)
		if project == "" || name == "" {
			return nil, fmt.Errorf("repos show requires a project and repository")
		}
		var repository map[string]json.RawMessage
		path := []string{project, "_apis", "git", "repositories", name}
		if err := client.Get(ctx, path, url.Values{"api-version": {gitReadVersion}}, &repository); err != nil {
			return nil, err
		}
		normalizeRepositoryURLs(repository)
		return repository, nil
	case "repos pr list":
		return listPullRequests(ctx, client, scope, opts)
	case "repos pr policy list":
		query, err := pageQuery(policyReadVersion, opts)
		if err != nil {
			return nil, err
		}
		return listPullRequestPolicies(ctx, client, opts["id"], query)
	case "repos pr show":
		id, raw, err := getPullRequestByID(ctx, client, opts["id"])
		if err != nil {
			return nil, err
		}
		row, err := decodePullRequestRow(raw)
		if err != nil {
			return nil, err
		}
		if pullRequestNumber(row) != id {
			return nil, fmt.Errorf("Azure DevOps response does not identify pull request %s", id)
		}
		if err := setPullRequestWebLinks(row, id, scope, true); err != nil {
			return nil, err
		}
		return row, nil
	default:
		return nil, fmt.Errorf("unsupported command %q", command)
	}
}

func getPullRequestByID(ctx context.Context, client *Client, option string) (string, json.RawMessage, error) {
	number, err := strconv.ParseInt(option, 10, 32)
	if err != nil || number <= 0 {
		return "", nil, fmt.Errorf("--id must be a positive pull request number")
	}
	id := strconv.FormatInt(number, 10)
	var raw json.RawMessage
	path := []string{"_apis", "git", "pullrequests", id}
	query := url.Values{"api-version": {gitReadVersion}}
	if err := client.Get(ctx, path, query, &raw); err != nil {
		return "", nil, fmt.Errorf("pull request %s: %w", id, err)
	}
	return id, raw, nil
}

func listPullRequestPolicies(ctx context.Context, client *Client, option string, query url.Values) ([]json.RawMessage, error) {
	id, raw, err := getPullRequestByID(ctx, client, option)
	if err != nil {
		return nil, err
	}
	var pr struct {
		ID         int32 `json:"pullRequestId"`
		Repository struct {
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, fmt.Errorf("read project for pull request %s: %w", id, err)
	}
	if strconv.Itoa(int(pr.ID)) != id {
		return nil, fmt.Errorf("Azure DevOps response does not identify pull request %s", id)
	}
	project := pr.Repository.Project.ID
	if !guidPattern.MatchString(project) {
		return nil, fmt.Errorf("pull request %s response is missing its project GUID", id)
	}
	query.Set("artifactId", "vstfs:///CodeReview/CodeReviewId/"+project+"/"+id)
	return getRepoPage(ctx, client, []string{project, "_apis", "policy", "evaluations"}, query)
}

func requestedRepository(scope Scope, opts map[string]string) (string, string) {
	project, repository := scope.Project, scope.Repository
	if opts["detect"] == "false" {
		project, repository = "", ""
	}
	if explicit, ok := opts["project"]; ok {
		project = explicit
	}
	if explicit, ok := opts["repository"]; ok {
		repository = explicit
	}
	return project, repository
}

func listPullRequests(ctx context.Context, client *Client, scope Scope, opts map[string]string) ([]json.RawMessage, error) {
	project, repository := requestedRepository(scope, opts)
	if project == "" {
		return nil, fmt.Errorf("repos pr list requires a project")
	}
	query, err := pageQuery(gitReadVersion, opts)
	if err != nil {
		return nil, err
	}
	if status, ok := opts["status"]; ok {
		query.Set("searchCriteria.status", status)
	}
	for _, filter := range []struct{ option, key string }{
		{"source-branch", "searchCriteria.sourceRefName"},
		{"target-branch", "searchCriteria.targetRefName"},
	} {
		if branch, ok := opts[filter.option]; ok {
			ref, err := pullRequestRef(branch)
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", filter.option, err)
			}
			query.Set(filter.key, ref)
		}
	}
	_, includeLinks := opts["include-links"]
	query.Set("searchCriteria.includeLinks", strconv.FormatBool(includeLinks))

	var viewer Identity
	for _, filter := range []struct{ option, key string }{
		{"creator", "searchCriteria.creatorId"},
		{"reviewer", "searchCriteria.reviewerId"},
	} {
		if login, ok := opts[filter.option]; ok {
			id, err := pullRequestFilterID(ctx, client, &viewer, login)
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", filter.option, err)
			}
			query.Set(filter.key, id)
		}
	}

	path := []string{project, "_apis", "git"}
	if repository != "" {
		path = append(path, "repositories", repository)
	}
	rows, err := getRepoPage(ctx, client, append(path, "pullrequests"), query)
	if err != nil {
		return nil, err
	}
	for index, raw := range rows {
		row, err := decodePullRequestRow(raw)
		id := pullRequestNumber(row)
		if err != nil || id == "" || setPullRequestWebLinks(row, id, scope, includeLinks) != nil {
			// Both list decoders skip a null row, but T3 still counts its
			// position when requesting the next REST page.
			rows[index] = json.RawMessage("null")
			continue
		}
		rows[index], err = json.Marshal(row)
		if err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func pageQuery(version string, opts map[string]string) (url.Values, error) {
	query := url.Values{"api-version": {version}}
	for _, option := range []string{"top", "skip"} {
		if raw, ok := opts[option]; ok {
			number, err := strconv.ParseInt(raw, 10, 32)
			if err != nil || number < 0 {
				return nil, fmt.Errorf("--%s must be a nonnegative integer", option)
			}
			query.Set("$"+option, strconv.FormatInt(number, 10))
		}
	}
	return query, nil
}

func getRepoPage(ctx context.Context, client *Client, path []string, query url.Values) ([]json.RawMessage, error) {
	var page struct {
		Value []json.RawMessage `json:"value"`
	}
	if err := client.Get(ctx, path, query, &page); err != nil {
		return nil, err
	}
	if page.Value == nil {
		return nil, fmt.Errorf("Azure DevOps response is missing the result array")
	}
	return page.Value, nil
}

func pullRequestRef(branch string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("branch is required")
	}
	// T3 escapes branch arguments for az's SDK, which inserts them into its query URL.
	if decoded, err := url.PathUnescape(branch); err == nil {
		branch = decoded
	}
	for _, prefix := range []string{"refs/heads/", "refs/pull/", "refs/tags/"} {
		if strings.HasPrefix(branch, prefix) {
			return branch, nil
		}
	}
	return "refs/heads/" + branch, nil
}

func pullRequestFilterID(ctx context.Context, client *Client, viewer *Identity, login string) (string, error) {
	login = strings.TrimSpace(login)
	if login == "" {
		return "", fmt.Errorf("identity is required")
	}
	if guidPattern.MatchString(login) {
		return login, nil
	}
	if viewer.ID == "" {
		identity, err := client.Identity(ctx)
		if err != nil {
			return "", err
		}
		if identity.ID == "" {
			return "", fmt.Errorf("authenticated identity is missing its ID")
		}
		*viewer = identity
	}
	if strings.EqualFold(login, "me") || strings.EqualFold(login, viewer.Login) {
		return viewer.ID, nil
	}
	query := url.Values{"api-version": {identityAPIVersion}, "filterValue": {login}}
	for _, searchFilter := range []string{"General", "DirectoryAlias"} {
		query.Set("searchFilter", searchFilter)
		rows, err := getRepoPage(ctx, client, []string{"_apis", "identities"}, query)
		if err != nil {
			return "", err
		}
		switch len(rows) {
		case 0:
			continue
		case 1:
			var identity struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(rows[0], &identity); err != nil || !guidPattern.MatchString(identity.ID) {
				return "", fmt.Errorf("identity result for %q is missing its GUID", login)
			}
			return identity.ID, nil
		default:
			return "", fmt.Errorf("multiple identities match %q", login)
		}
	}
	return "", fmt.Errorf("could not resolve identity %q", login)
}

func decodePullRequestRow(raw json.RawMessage) (map[string]any, error) {
	var row map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&row); err != nil {
		return nil, fmt.Errorf("read pull request: %w", err)
	}
	return row, nil
}

func pullRequestNumber(row map[string]any) string {
	number, _ := row["pullRequestId"].(json.Number)
	id, err := number.Int64()
	if err != nil || id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func setPullRequestWebLinks(row map[string]any, id string, scope Scope, includeLinks bool) error {
	repository, _ := row["repository"].(map[string]any)
	project, _ := repository["project"].(map[string]any)
	repositoryName, _ := repository["name"].(string)
	projectName, _ := project["name"].(string)
	if repositoryName == "" || projectName == "" {
		return fmt.Errorf("pull request %s response is missing its project or repository name", id)
	}
	if !validSegment(projectName) || !validSegment(repositoryName) {
		return fmt.Errorf("pull request %s response contains an invalid project or repository name", id)
	}
	repoWebURL := scope.WebURL
	if repoWebURL == "" || !strings.EqualFold(scope.Project, projectName) || !strings.EqualFold(scope.Repository, repositoryName) {
		repoWebURL = strings.TrimRight(scope.Organization, "/") + "/" + url.PathEscape(projectName) + "/_git/" + url.PathEscape(repositoryName)
	}
	repository["webUrl"] = repoWebURL
	links, _ := row["_links"].(map[string]any)
	web, _ := links["web"].(map[string]any)
	if includeLinks || web != nil {
		if links == nil {
			links = make(map[string]any)
			row["_links"] = links
		}
		if web == nil {
			web = make(map[string]any)
			links["web"] = web
		}
		web["href"] = repoWebURL + "/pullrequest/" + id
	}
	return nil
}
