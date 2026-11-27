package main

import (
	"archive/zip"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const emptyGUID = "00000000-0000-0000-0000-000000000000"

func (s *server) gitAPI(q *req) {
	p := lower(q.parts)
	m := q.r.Method
	switch {
	case match(p, "_apis", "git", "repositories"):
		s.repositories(q)
	case match(p, "_apis", "git", "pullrequests") && m == http.MethodGet:
		s.listPRs(q, nil)
	case match(p, "_apis", "git", "pullrequests", "*") && m == http.MethodGet:
		if pr := s.findPR(q, q.parts[3], nil); pr != nil {
			writeJSON(q.w, http.StatusOK, s.prJSON(q.l, pr, false))
		}
	case len(p) >= 4 && p[2] == "repositories":
		rp := q.c.repo(q.project, q.parts[3])
		if rp == nil {
			serviceError(q.w, http.StatusNotFound, "TF401019: The Git repository with name or identifier "+q.parts[3]+" does not exist or you do not have permissions for the operation you are attempting.", "GitRepositoryNotFoundException")
			return
		}
		s.repoAPI(q, rp, p[4:])
	default:
		notFound(q.w)
	}
}

func (s *server) repositories(q *req) {
	switch q.r.Method {
	case http.MethodGet:
		var rows []any
		for _, rp := range q.c.Repos {
			if q.project == nil || rp.Project == q.project {
				rows = append(rows, s.repoJSON(q.l, rp, false))
			}
		}
		writeList(q.w, rows)
	case http.MethodPost:
		var body struct{ Name string }
		if !decodeBody(q, &body) {
			return
		}
		if q.project == nil || strings.TrimSpace(body.Name) == "" {
			serviceError(q.w, http.StatusBadRequest, "A project and a repository name are required.", "ArgumentNullException")
			return
		}
		if q.c.repo(q.project, body.Name) != nil {
			serviceError(q.w, http.StatusConflict, "TF400948: A Git repository with the name "+body.Name+" already exists.", "GitRepositoryNameAlreadyExistsException")
			return
		}
		rp := &repo{ID: guid("repo", q.c.Name, q.project.Name, body.Name, time.Now().String()), Name: body.Name, Project: q.project}
		rp.Dir = s.gitRoot + "/" + rp.ID + ".git"
		if err := createBareRepo(rp.Dir); err != nil {
			serviceError(q.w, http.StatusInternalServerError, err.Error(), "MockException")
			return
		}
		q.c.Repos = append(q.c.Repos, rp)
		writeJSON(q.w, http.StatusCreated, s.repoJSON(q.l, rp, true))
	default:
		notFound(q.w)
	}
}

func (s *server) repoJSON(l links, rp *repo, withLinks bool) map[string]any {
	self := l.api(rp.Project.ID, "_apis", "git", "repositories", rp.ID)
	out := map[string]any{
		"id": rp.ID, "name": rp.Name, "url": self, "project": l.project(rp.Project),
		"size": 0, "remoteUrl": l.gitWeb(rp), "sshUrl": l.gitSSH(rp), "webUrl": l.gitWeb(rp),
		"isDisabled": false, "isInMaintenance": false,
	}
	if ref := defaultBranch(rp.Dir); ref != "" {
		out["defaultBranch"] = ref
	}
	if withLinks {
		out["_links"] = map[string]any{
			"self": map[string]any{"href": self}, "web": map[string]any{"href": l.gitWeb(rp)},
			"ssh": map[string]any{"href": l.gitSSH(rp)}, "project": map[string]any{"href": "vstfs:///Classification/TeamProject/" + rp.Project.ID},
		}
	}
	return out
}

func (s *server) repoAPI(q *req, rp *repo, rest []string) {
	m := q.r.Method
	switch {
	case len(rest) == 0 && m == http.MethodGet:
		writeJSON(q.w, http.StatusOK, s.repoJSON(q.l, rp, true))
	case match(rest, "pullrequests") && m == http.MethodGet:
		s.listPRs(q, rp)
	case match(rest, "pullrequests") && m == http.MethodPost:
		s.createPR(q, rp)
	case match(rest, "items") && m == http.MethodGet:
		s.item(q, rp)
	case match(rest, "blobs") && m == http.MethodPost:
		s.blobs(q, rp)
	case len(rest) >= 2 && rest[0] == "pullrequests":
		pr := s.findPR(q, q.parts[len(q.parts)-len(rest)+1], rp)
		if pr == nil {
			return
		}
		s.prAPI(q, pr, rest[2:])
	default:
		notFound(q.w)
	}
}

func (s *server) findPR(q *req, raw string, rp *repo) *pullRequest {
	id, _ := strconv.Atoi(raw)
	pr := q.c.pr(id)
	if pr == nil || (rp != nil && pr.Repo != rp) || (q.project != nil && pr.Repo.Project != q.project) {
		serviceError(q.w, http.StatusNotFound, "TF401180: The requested pull request was not found.", "GitPullRequestNotFoundException")
		return nil
	}
	return pr
}

func (s *server) prAPI(q *req, pr *pullRequest, rest []string) {
	m := q.r.Method
	switch {
	case len(rest) == 0 && m == http.MethodGet:
		writeJSON(q.w, http.StatusOK, s.prJSON(q.l, pr, false))
	case len(rest) == 0 && m == http.MethodPatch:
		s.updatePR(q, pr)
	case match(rest, "threads") && m == http.MethodGet:
		var rows []any
		for _, t := range pr.Threads {
			var comments []any
			for _, c := range t.Comments {
				comments = append(comments, map[string]any{
					"id": c.ID, "parentCommentId": 0, "author": q.l.identityRef(c.Author), "content": c.Content,
					"publishedDate": timestamp(c.At), "lastUpdatedDate": timestamp(c.At), "lastContentUpdatedDate": timestamp(c.At),
					"commentType": "text", "usersLiked": []any{},
				})
			}
			rows = append(rows, map[string]any{
				"id": t.ID, "publishedDate": timestamp(t.Comments[0].At), "lastUpdatedDate": timestamp(t.Comments[0].At),
				"comments": comments, "status": t.Status, "threadContext": nil, "properties": map[string]any{}, "identities": nil, "isDeleted": false,
			})
		}
		writeList(q.w, rows)
	case match(rest, "labels") && m == http.MethodGet:
		var rows []any
		for _, name := range pr.Labels {
			id := guid("label", name)
			rows = append(rows, map[string]any{"id": id, "name": name, "active": true, "url": q.l.api("_apis", "git", "labels", id)})
		}
		writeList(q.w, rows)
	case match(rest, "iterations") && m == http.MethodGet:
		writeList(q.w, []any{s.iterationJSON(q.l, pr)})
	case match(rest, "iterations", "*", "changes") && m == http.MethodGet:
		s.iterationChanges(q, pr, rest[1])
	case match(rest, "reviewers") && m == http.MethodGet:
		writeList(q.w, s.reviewersJSON(q.l, pr, pr.Reviewers))
	case match(rest, "reviewers") && m == http.MethodPost:
		s.addReviewers(q, pr)
	case match(rest, "reviewers", "*") && (m == http.MethodDelete || m == http.MethodPut):
		s.reviewer(q, pr, q.parts[len(q.parts)-1])
	default:
		notFound(q.w)
	}
}

// commits returns the source, target and common commits of a PR. A closed
// PR keeps the commits it had when it closed.
func (pr *pullRequest) commits() (source, target, common string) {
	if pr.Status == "active" {
		pr.LastSource, pr.LastTarget = head(pr.Repo.Dir, pr.Source), head(pr.Repo.Dir, pr.Target)
	}
	source, target = pr.LastSource, pr.LastTarget
	if source != "" && target != "" {
		common = mergeBase(pr.Repo.Dir, source, target)
	}
	return source, target, common
}

func (s *server) prJSON(l links, pr *pullRequest, list bool) map[string]any {
	source, target, _ := pr.commits()
	repoRef := s.repoJSON(l, pr.Repo, false)
	delete(repoRef, "defaultBranch")
	self := l.api(pr.Repo.Project.ID, "_apis", "git", "repositories", pr.Repo.ID, "pullRequests", strconv.Itoa(pr.ID))
	out := map[string]any{
		"repository": repoRef, "pullRequestId": pr.ID, "codeReviewId": pr.ID, "status": pr.Status,
		"createdBy": l.identityRef(pr.CreatedBy), "creationDate": timestamp(pr.Created),
		"title": pr.Title, "description": pr.Description,
		"sourceRefName": pr.Source, "targetRefName": pr.Target,
		"mergeStatus": "succeeded", "isDraft": pr.Draft, "mergeId": guid("merge", pr.Repo.ID, strconv.Itoa(pr.ID)),
		"reviewers": s.reviewersJSON(l, pr, pr.Reviewers), "url": self,
		"supportsIterations": true, "artifactId": prArtifactID(pr),
	}
	if source != "" {
		out["lastMergeSourceCommit"] = map[string]any{"commitId": source, "url": l.api(pr.Repo.Project.ID, "_apis", "git", "repositories", pr.Repo.ID, "commits", source)}
	}
	if target != "" {
		out["lastMergeTargetCommit"] = map[string]any{"commitId": target, "url": l.api(pr.Repo.Project.ID, "_apis", "git", "repositories", pr.Repo.ID, "commits", target)}
	}
	if pr.MergeCommit != "" {
		out["lastMergeCommit"] = map[string]any{"commitId": pr.MergeCommit, "url": l.api(pr.Repo.Project.ID, "_apis", "git", "repositories", pr.Repo.ID, "commits", pr.MergeCommit)}
	}
	if len(pr.Completion) > 0 {
		out["completionOptions"] = pr.Completion
	}
	if pr.AutoComplete != nil {
		out["autoCompleteSetBy"] = l.identityRef(pr.AutoComplete)
	}
	if !pr.Closed.IsZero() {
		out["closedDate"] = timestamp(pr.Closed)
		out["closedBy"] = l.identityRef(pr.ClosedBy)
	}
	if list {
		// List rows carry a slim repository and no artifact ID.
		out["repository"] = map[string]any{"id": pr.Repo.ID, "name": pr.Repo.Name, "url": repoRef["url"], "project": map[string]any{
			"id": pr.Repo.Project.ID, "name": pr.Repo.Project.Name, "state": "unchanged", "visibility": "unchanged", "lastUpdateTime": "0001-01-01T00:00:00",
		}}
		delete(out, "artifactId")
	}
	if len(pr.Labels) > 0 {
		var labels []any
		for _, name := range pr.Labels {
			labels = append(labels, map[string]any{"id": guid("label", name), "name": name, "active": true})
		}
		out["labels"] = labels
	}
	return out
}

func (s *server) reviewersJSON(l links, pr *pullRequest, reviewers []*reviewer) []any {
	rows := []any{}
	for _, rv := range reviewers {
		row := l.identityRef(rv.Who)
		delete(row, "descriptor")
		row["reviewerUrl"] = l.api(pr.Repo.Project.ID, "_apis", "git", "repositories", pr.Repo.ID, "pullRequests", strconv.Itoa(pr.ID), "reviewers", rv.Who.ID)
		row["vote"], row["hasDeclined"], row["isFlagged"] = rv.Vote, false, false
		if rv.Required {
			row["isRequired"] = true
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *server) listPRs(q *req, rp *repo) {
	query := q.r.URL.Query()
	status := strings.ToLower(query.Get("searchCriteria.status"))
	if status == "" {
		status = "active"
	}
	var rows []any
	for _, pr := range slices.Backward(q.c.PRs) {
		switch {
		case rp != nil && pr.Repo != rp,
			q.project != nil && pr.Repo.Project != q.project,
			status != "all" && pr.Status != status,
			!matchesParam(query.Get("searchCriteria.creatorId"), pr.CreatedBy.ID),
			!matchesParam(query.Get("searchCriteria.repositoryId"), pr.Repo.ID),
			!matchesParam(query.Get("searchCriteria.sourceRefName"), pr.Source),
			!matchesParam(query.Get("searchCriteria.targetRefName"), pr.Target),
			!hasReviewer(pr, query.Get("searchCriteria.reviewerId")):
			continue
		}
		rows = append(rows, s.prJSON(q.l, pr, true))
	}
	writeList(q.w, page(rows, q.r))
}

func matchesParam(want, got string) bool { return want == "" || strings.EqualFold(want, got) }

func hasReviewer(pr *pullRequest, id string) bool {
	if id == "" {
		return true
	}
	for _, rv := range pr.Reviewers {
		if strings.EqualFold(rv.Who.ID, id) {
			return true
		}
	}
	return false
}

type prWrite struct {
	SourceRefName, TargetRefName string
	Title, Description           *string
	IsDraft                      *bool
	Status                       string
	Reviewers                    []struct {
		ID         string `json:"id"`
		IsRequired bool   `json:"isRequired"`
	}
	CompletionOptions     map[string]any
	AutoCompleteSetBy     *struct{ ID string }
	LastMergeSourceCommit *struct{ CommitID string }
}

func (s *server) createPR(q *req, rp *repo) {
	var body prWrite
	if !decodeBody(q, &body) {
		return
	}
	if head(rp.Dir, body.SourceRefName) == "" || head(rp.Dir, body.TargetRefName) == "" {
		serviceError(q.w, http.StatusBadRequest, "TF401398: The pull request cannot be activated because the source and/or the target branch no longer exists, or the requested refs are not branches", "GitPullRequestCannotBeActivated")
		return
	}
	for _, pr := range q.c.PRs {
		if pr.Repo == rp && pr.Status == "active" && pr.Source == body.SourceRefName && pr.Target == body.TargetRefName {
			serviceError(q.w, http.StatusConflict, fmt.Sprintf("TF401179: An active pull request for the source and target branch already exists. Id: %d", pr.ID), "GitPullRequestExistsException")
			return
		}
	}
	pr := &pullRequest{Repo: rp, CreatedBy: s.viewer, Source: body.SourceRefName, Target: body.TargetRefName,
		Checks: []check{{"Mock CI", "build", "queued"}}}
	if body.Title != nil {
		pr.Title = *body.Title
	}
	if body.Description != nil {
		pr.Description = *body.Description
	}
	if body.IsDraft != nil {
		pr.Draft = *body.IsDraft
	}
	for _, r := range body.Reviewers {
		if who := s.findIdentity(r.ID); who != nil {
			pr.Reviewers = append(pr.Reviewers, &reviewer{Who: who, Required: r.IsRequired})
		}
	}
	q.c.addPR(pr)
	pr.Created = time.Now().UTC()
	writeJSON(q.w, http.StatusCreated, s.prJSON(q.l, pr, false))
}

func (s *server) updatePR(q *req, pr *pullRequest) {
	var body prWrite
	if !decodeBody(q, &body) {
		return
	}
	// Edits apply to a copy that replaces the PR only if the whole PATCH
	// succeeds.
	staged := *pr
	staged.Completion = maps.Clone(pr.Completion)
	if body.Title != nil {
		staged.Title = *body.Title
	}
	if body.Description != nil {
		staged.Description = *body.Description
	}
	if body.IsDraft != nil {
		staged.Draft = *body.IsDraft
	}
	if body.CompletionOptions != nil {
		if staged.Completion == nil {
			staged.Completion = map[string]any{}
		}
		maps.Copy(staged.Completion, body.CompletionOptions)
	}
	if body.AutoCompleteSetBy != nil {
		staged.AutoComplete = nil
		if body.AutoCompleteSetBy.ID != emptyGUID {
			staged.AutoComplete = s.findIdentity(body.AutoCompleteSetBy.ID)
		}
	}
	switch body.Status {
	case "":
	case "abandoned":
		staged.commits()
		staged.Status, staged.Closed, staged.ClosedBy = "abandoned", time.Now().UTC(), s.viewer
	case "active":
		staged.Status, staged.Closed, staged.ClosedBy = "active", time.Time{}, nil
	case "completed":
		if err := s.completePR(&staged, body, gitIdentityEnv); err != nil {
			serviceError(q.w, http.StatusConflict, err.Error(), "GitPullRequestUpdateRejectedException")
			return
		}
	default:
		serviceError(q.w, http.StatusBadRequest, "Unsupported status "+body.Status, "InvalidArgumentValueException")
		return
	}
	*pr = staged
	writeJSON(q.w, http.StatusOK, s.prJSON(q.l, pr, false))
}

// completePR merges with the given Git environment; the seed passes fixed
// dates so the merge commit ID is the same on every start.
func (s *server) completePR(pr *pullRequest, body prWrite, env []string) error {
	if pr.Status != "active" {
		return fmt.Errorf("TF401181: The pull request cannot be completed because it is %s", pr.Status)
	}
	source, target, _ := pr.commits()
	if body.LastMergeSourceCommit != nil && body.LastMergeSourceCommit.CommitID != "" && body.LastMergeSourceCommit.CommitID != source {
		return fmt.Errorf("TF401192: The source branch has been updated since the pull request was read")
	}
	strategy, _ := pr.Completion["mergeStrategy"].(string)
	squash, _ := pr.Completion["squashMerge"].(bool)
	squash = strategy == "squash" || (strategy == "" && squash)
	message, _ := pr.Completion["mergeCommitMessage"].(string)
	if message == "" {
		message = fmt.Sprintf("Merged PR %d: %s", pr.ID, pr.Title)
	}
	commit, err := merge(pr.Repo.Dir, source, target, message, squash, env)
	if err != nil {
		return err
	}
	// The target update and the source deletion succeed or fail together,
	// and each fails if its branch moved since it was read.
	refs := []string{"update " + pr.Target + " " + commit + " " + target}
	if del, _ := pr.Completion["deleteSourceBranch"].(bool); del {
		refs = append(refs, "delete "+pr.Source+" "+source)
	}
	if err := refTransaction(pr.Repo.Dir, refs...); err != nil {
		return err
	}
	pr.MergeCommit, pr.Status, pr.Closed, pr.ClosedBy, pr.AutoComplete = commit, "completed", time.Now().UTC(), s.viewer, nil
	return nil
}

func (s *server) addReviewers(q *req, pr *pullRequest) {
	var body []struct {
		ID         string `json:"id"`
		IsRequired bool   `json:"isRequired"`
	}
	if !decodeBody(q, &body) {
		return
	}
	people := make([]*identity, len(body))
	for i, b := range body {
		if people[i] = s.findIdentity(b.ID); people[i] == nil {
			serviceError(q.w, http.StatusBadRequest, "TF401199: The identity "+b.ID+" is not valid.", "InvalidArgumentValueException")
			return
		}
	}
	var added []*reviewer
	for i, b := range body {
		added = append(added, s.setReviewer(pr, people[i], b.IsRequired))
	}
	writeList(q.w, s.reviewersJSON(q.l, pr, added))
}

func (s *server) setReviewer(pr *pullRequest, who *identity, required bool) *reviewer {
	for _, rv := range pr.Reviewers {
		if rv.Who == who {
			rv.Required = required
			return rv
		}
	}
	rv := &reviewer{Who: who, Required: required}
	pr.Reviewers = append(pr.Reviewers, rv)
	return rv
}

func (s *server) reviewer(q *req, pr *pullRequest, id string) {
	who := s.findIdentity(id)
	if who == nil {
		serviceError(q.w, http.StatusNotFound, "TF401199: The identity "+id+" is not valid.", "InvalidArgumentValueException")
		return
	}
	if q.r.Method == http.MethodDelete {
		pr.Reviewers = slices.DeleteFunc(pr.Reviewers, func(rv *reviewer) bool { return rv.Who == who })
		q.w.WriteHeader(http.StatusNoContent)
		return
	}
	var body struct {
		Vote       int  `json:"vote"`
		IsRequired bool `json:"isRequired"`
	}
	if !decodeBody(q, &body) {
		return
	}
	rv := s.setReviewer(pr, who, body.IsRequired)
	rv.Vote = body.Vote
	writeJSON(q.w, http.StatusOK, s.reviewersJSON(q.l, pr, []*reviewer{rv})[0])
}

func (s *server) iterationJSON(l links, pr *pullRequest) map[string]any {
	source, target, common := pr.commits()
	return map[string]any{
		"id": 1, "description": pr.Title, "author": l.identityRef(pr.CreatedBy),
		"createdDate": timestamp(pr.Created), "updatedDate": timestamp(pr.Created),
		"sourceRefCommit": map[string]any{"commitId": source}, "targetRefCommit": map[string]any{"commitId": target},
		"commonRefCommit": map[string]any{"commitId": common}, "hasMoreCommits": false, "reason": "create",
	}
}

func (s *server) iterationChanges(q *req, pr *pullRequest, iteration string) {
	if iteration != "1" {
		serviceError(q.w, http.StatusNotFound, "TF401178: The requested pull request iteration was not found.", "GitPullRequestIterationNotFoundException")
		return
	}
	source, _, common := pr.commits()
	changes, err := diff(pr.Repo.Dir, common, source)
	if err != nil {
		serviceError(q.w, http.StatusInternalServerError, err.Error(), "MockException")
		return
	}
	var rows []any
	for i, c := range changes {
		item := map[string]any{"objectId": strings.ToUpper(c.Object), "path": c.Path, "gitObjectType": "blob"}
		if c.Original != "" {
			item["originalObjectId"] = strings.ToUpper(c.Original)
		}
		rows = append(rows, map[string]any{"changeTrackingId": i + 1, "changeId": i + 1, "item": item, "changeType": c.Type})
	}
	query := q.r.URL.Query()
	skip, skipErr := strconv.Atoi(query.Get("$skip"))
	top, topErr := strconv.Atoi(query.Get("$top"))
	if (query.Has("$skip") && (skipErr != nil || skip < 0)) || (query.Has("$top") && (topErr != nil || top < 1)) {
		serviceError(q.w, http.StatusBadRequest, "$skip must be 0 or more and $top 1 or more.", "InvalidArgumentValueException")
		return
	}
	paged := page(rows, q.r)
	out := map[string]any{"changeEntries": orEmpty(paged)}
	if query.Has("$top") {
		if next := skip + len(paged); next < len(rows) {
			out["nextSkip"], out["nextTop"] = next, len(paged)
		} else {
			out["nextSkip"], out["nextTop"] = 0, 0
		}
	}
	writeJSON(q.w, http.StatusOK, out)
}

func orEmpty(rows []any) []any {
	if rows == nil {
		return []any{}
	}
	return rows
}

func (s *server) item(q *req, rp *repo) {
	query := q.r.URL.Query()
	// The server's own item URLs use version; clients send versionDescriptor.version.
	path, commit := query.Get("path"), query.Get("versionDescriptor.version")
	if commit == "" {
		commit = query.Get("version")
	}
	object := objectAt(rp.Dir, commit, path)
	if object == "" {
		serviceError(q.w, http.StatusNotFound, "TF401174: The item '"+path+"' could not be found in the repository '"+rp.Name+"' at the version specified by '"+commit+"'.", "GitItemNotFoundException")
		return
	}
	content, err := blob(rp.Dir, object)
	if err != nil {
		serviceError(q.w, http.StatusInternalServerError, err.Error(), "MockException")
		return
	}
	name := path[strings.LastIndex(path, "/")+1:]
	metadata := map[string]any{"encoding": 1252, "contentType": "application/octet-stream", "fileName": name, "extension": strings.TrimPrefix(filepath.Ext(name), ".")}
	if !utf8.Valid(content) || slices.Contains(content, 0) {
		metadata["isBinary"] = true
	} else if !isASCII(content) {
		metadata["encoding"] = 65001
	}
	out := map[string]any{
		"objectId": object, "gitObjectType": "blob", "commitId": commit, "path": path,
		"contentMetadata": metadata,
		"url":             q.l.api(rp.Project.ID, "_apis", "git", "repositories", rp.ID, "items") + "?" + url.Values{"path": {path}, "versionType": {"Commit"}, "version": {commit}, "versionOptions": {"None"}}.Encode(),
	}
	if query.Get("includeContent") == "true" {
		out["content"] = string(content)
	}
	writeJSON(q.w, http.StatusOK, out)
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func (s *server) blobs(q *req, rp *repo) {
	var ids []string
	if !decodeBody(q, &ids) {
		return
	}
	q.w.Header().Set("Content-Type", "application/zip")
	zw := zip.NewWriter(q.w)
	for _, id := range ids {
		object := resolve(rp.Dir, strings.ToLower(id), "blob")
		if object == "" {
			continue
		}
		content, err := blob(rp.Dir, object)
		if err != nil {
			continue
		}
		f, err := zw.Create(id)
		if err == nil {
			_, err = f.Write(content)
		}
		if err != nil {
			return
		}
	}
	_ = zw.Close()
}

func (s *server) policyEvaluations(q *req) {
	artifact := q.r.URL.Query().Get("artifactId")
	var pr *pullRequest
	if rest, ok := strings.CutPrefix(artifact, "vstfs:///CodeReview/CodeReviewId/"); ok {
		projectID, id, _ := strings.Cut(rest, "/")
		n, _ := strconv.Atoi(id)
		if candidate := q.c.pr(n); candidate != nil && strings.EqualFold(candidate.Repo.Project.ID, projectID) {
			pr = candidate
		}
	}
	if pr == nil {
		serviceError(q.w, http.StatusNotFound, "Artifact id '"+artifact+"' does not exist or you do not have permission to view it.", "ArtifactNotFoundException")
		return
	}
	var rows []any
	for i, c := range pr.Checks {
		rows = append(rows, s.checkJSON(q.l, pr, artifact, i+1, c))
	}
	writeList(q.w, page(rows, q.r))
}

var checkTypes = map[string][2]string{
	"build":     {"0609b952-1397-4640-95ec-e00a01b2c241", "Build"},
	"status":    {"cbdc66da-9728-4af8-aada-9a5a32e4a226", "Status"},
	"reviewers": {"fa4e907d-c16b-4a4c-9dfa-4906e5d171dd", "Minimum number of reviewers"},
}

func (s *server) checkJSON(l links, pr *pullRequest, artifact string, id int, c check) map[string]any {
	typ := checkTypes[c.Kind]
	settings := map[string]any{}
	context := map[string]any{}
	switch c.Kind {
	case "build":
		settings = map[string]any{"displayName": c.Name, "buildDefinitionId": id, "validDuration": 720}
		context = map[string]any{"buildDefinitionName": c.Name, "buildDefinitionId": id, "buildId": pr.ID*100 + id, "buildIsNotCurrent": false}
	case "status":
		settings = map[string]any{"statusGenre": "mock-ci", "statusName": c.Name, "defaultDisplayName": c.Name}
	case "reviewers":
		settings = map[string]any{"minimumApproverCount": 2, "creatorVoteCounts": false}
	}
	row := map[string]any{
		"configuration": map[string]any{
			"id": id, "url": l.api(pr.Repo.Project.ID, "_apis", "policy", "configurations", strconv.Itoa(id)),
			"isBlocking": true, "isEnabled": true, "isDeleted": false,
			"type": map[string]any{"id": typ[0], "displayName": typ[1]}, "settings": settings,
		},
		"artifactId": artifact, "evaluationId": guid("evaluation", pr.Repo.ID, strconv.Itoa(pr.ID), c.Name),
		"startedDate": timestamp(pr.Created.Add(time.Minute)), "status": c.Status,
	}
	if len(context) > 0 {
		row["context"] = context
	}
	if c.Status == "approved" || c.Status == "rejected" {
		row["completedDate"] = timestamp(pr.Created.Add(10 * time.Minute))
	}
	return row
}
