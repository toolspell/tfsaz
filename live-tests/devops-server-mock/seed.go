package main

import (
	"fmt"
	"path/filepath"
	"time"
)

// branchSeed is one branch created from main with the given file contents.
type branchSeed struct {
	name  string
	files map[string]string
}

// Votes as the server reports them.
const (
	approved        = 10
	withSuggestions = 5
	noVote          = 0
	waiting         = -5
	rejected        = -10
)

func file(content string) map[string]string { return map[string]string{"src/change.txt": content} }

func (s *server) seed() error {
	tester, rita, oscar := s.person("tester"), s.person("rreviewer"), s.person("oother")
	alice, ben := s.person("aarcher"), s.person("bbuilder")
	rv := func(who *identity, vote int, required bool) *reviewer {
		return &reviewer{Who: who, Vote: vote, Required: required}
	}

	dc := &collection{Name: "DefaultCollection", ID: guid("collection", "DefaultCollection")}
	mock := dc.addProject("Mock Project", "Mock project with a space in its name.")
	platform := dc.addProject("Platform", "Infrastructure for the mock services.")
	api, err := s.addRepo(dc, mock, "api",
		branchSeed{"feature/readme", map[string]string{"README.md": "# api\n\nMock repository.\nA feature line.\n", "docs/notes #1.md": "Notes with a special path.\n"}},
		branchSeed{"feature/other", file("Other change.\n")},
		branchSeed{"feature/failing-tests", map[string]string{"src/login.txt": "Login change that breaks a test.\n"}},
	)
	if err != nil {
		return err
	}
	web, err := s.addRepo(dc, mock, "web app",
		branchSeed{"feature/web", map[string]string{"docs/guide.md": "Guide\n\nUpdated by the web feature.\n"}},
		branchSeed{"feature/old-design", file("Old design.\n")},
	)
	if err != nil {
		return err
	}
	infra, err := s.addRepo(dc, platform, "infra",
		branchSeed{"feature/network", file("Network module.\n")},
		branchSeed{"feature/queue", map[string]string{"src/queue.txt": "Queue module.\n"}},
	)
	if err != nil {
		return err
	}
	pr1 := dc.addPR(&pullRequest{Repo: api, CreatedBy: tester, Title: "Update the readme", Description: "Adds a feature line.\n\nSecond paragraph.",
		Source: "refs/heads/feature/readme", Target: "refs/heads/main", Labels: []string{"documentation"},
		Reviewers: []*reviewer{rv(rita, approved, false), rv(alice, waiting, false), rv(ben, noVote, true)},
		Checks:    []check{{"API CI", "build", "approved"}, {"code-coverage", "status", "approved"}, {"Minimum reviewers", "reviewers", "queued"}},
		Threads:   []prThread{{ID: 1, Status: "active", Comments: []prComment{{ID: 1, Author: rita, Content: "Please update the example.", At: seedTime.Add(time.Hour)}}}}})
	dc.addPR(&pullRequest{Repo: api, CreatedBy: oscar, Title: "Add other file", Source: "refs/heads/feature/other", Target: "refs/heads/main", Draft: true,
		Reviewers: []*reviewer{rv(tester, noVote, true), rv(rita, withSuggestions, false)},
		Checks:    []check{{"API CI", "build", "running"}}})
	pr3 := dc.addPR(&pullRequest{Repo: api, CreatedBy: alice, Title: "Fix the login flow", Source: "refs/heads/feature/failing-tests", Target: "refs/heads/main",
		Labels: []string{"bug", "needs-work"}, Reviewers: []*reviewer{rv(tester, rejected, false), rv(oscar, approved, false)},
		Checks: []check{{"API CI", "build", "rejected"}, {"code-coverage", "status", "rejected"}}})
	dc.addPR(&pullRequest{Repo: web, CreatedBy: tester, Title: "Update the guide", Source: "refs/heads/feature/web", Target: "refs/heads/main",
		Reviewers: []*reviewer{rv(alice, approved, true), rv(ben, approved, false)},
		Checks:    []check{{"Web CI", "build", "approved"}, {"Minimum reviewers", "reviewers", "approved"}}})
	old := dc.addPR(&pullRequest{Repo: web, CreatedBy: ben, Title: "Try the old design", Source: "refs/heads/feature/old-design", Target: "refs/heads/main",
		Reviewers: []*reviewer{rv(tester, rejected, false)}, Checks: []check{{"Web CI", "build", "approved"}}})
	s.closePR(old, "abandoned")
	merged := dc.addPR(&pullRequest{Repo: infra, CreatedBy: ben, Title: "Add the network module", Source: "refs/heads/feature/network", Target: "refs/heads/main",
		Reviewers: []*reviewer{rv(tester, approved, false), rv(alice, approved, false)},
		Checks:    []check{{"Infra CI", "build", "approved"}}, Completion: map[string]any{"mergeStrategy": "squash"}})
	if err := s.completePR(merged, prWrite{}, seedEnv); err != nil {
		return err
	}
	merged.Closed, merged.ClosedBy = merged.Created.Add(2*time.Hour), merged.CreatedBy
	pr7 := dc.addPR(&pullRequest{Repo: infra, CreatedBy: tester, Title: "Add the queue module", Source: "refs/heads/feature/queue", Target: "refs/heads/main",
		Reviewers: []*reviewer{rv(ben, noVote, false), rv(rita, noVote, false)},
		Checks:    []check{{"Infra CI", "build", "queued"}, {"Minimum reviewers", "reviewers", "queued"}}})
	dc.addWorkItem(s, mock, "Issue", "Mock issue", "Doing", tester, oscar, "mock; ui", "<p>Issue body.</p>", pr1,
		[]*wiComment{{Text: "First comment.", By: rita}, {Text: "Second comment.", By: tester}})
	dc.addWorkItem(s, mock, "Task", "Mock task", "To Do", oscar, tester, "backend", "", nil, nil)
	dc.addWorkItem(s, mock, "Epic", "Mock epic", "Done", nil, oscar, "", "", nil, nil)
	dc.addWorkItem(s, mock, "Issue", "Login fails after timeout", "To Do", alice, tester, "bug; ui", "<p>Steps to reproduce.</p>", pr3,
		[]*wiComment{{Text: "Reproduced on the mock.", By: alice}})
	dc.addWorkItem(s, platform, "Task", "Set up the queue", "Doing", tester, ben, "infra", "", pr7, nil)

	sc := &collection{Name: "Second Collection", ID: guid("collection", "Second Collection")}
	demo := sc.addProject("Demo Project", "Project in a collection with a space in its name.")
	api2, err := s.addRepo(sc, demo, "api",
		branchSeed{"feature/readme", map[string]string{"README.md": "# api\n\nMock repository.\nSecond collection change.\n"}},
	)
	if err != nil {
		return err
	}
	docs, err := s.addRepo(sc, demo, "docs site",
		branchSeed{"feature/pages", file("New pages.\n")},
	)
	if err != nil {
		return err
	}
	pr21 := sc.addPR(&pullRequest{Repo: api2, CreatedBy: tester, Title: "Second collection change", Source: "refs/heads/feature/readme", Target: "refs/heads/main",
		Reviewers: []*reviewer{rv(rita, approved, false), rv(oscar, noVote, false)},
		Checks:    []check{{"Demo CI", "build", "approved"}}})
	sc.addPR(&pullRequest{Repo: docs, CreatedBy: alice, Title: "Add new pages", Source: "refs/heads/feature/pages", Target: "refs/heads/main",
		Reviewers: []*reviewer{rv(tester, noVote, false), rv(ben, waiting, false)},
		Checks:    []check{{"Docs build", "build", "running"}, {"Minimum reviewers", "reviewers", "queued"}}})
	sc.addWorkItem(s, demo, "Issue", "Second collection issue", "To Do", tester, tester, "mock", "", pr21, nil)
	sc.addWorkItem(s, demo, "Task", "Write the docs", "Doing", alice, alice, "docs", "", nil, nil)

	s.collections = []*collection{dc, sc}
	return nil
}

// closePR records the PR's commits and closes it without merging.
func (s *server) closePR(pr *pullRequest, status string) {
	pr.commits()
	pr.Status, pr.Closed, pr.ClosedBy = status, pr.Created.Add(time.Hour), pr.CreatedBy
}

func (c *collection) addProject(name, description string) *project {
	p := &project{ID: guid("project", c.Name, name), Name: name, Description: description}
	c.Projects = append(c.Projects, p)
	return p
}

func (s *server) addRepo(c *collection, p *project, name string, branches ...branchSeed) (*repo, error) {
	r := &repo{ID: guid("repo", c.Name, p.Name, name), Name: name, Project: p}
	r.Dir = filepath.Join(s.gitRoot, r.ID+".git")
	if err := createBareRepo(r.Dir); err != nil {
		return nil, err
	}
	if err := seedRepoContent(r.Dir, name, branches); err != nil {
		return nil, fmt.Errorf("seed %s: %w", name, err)
	}
	c.Repos = append(c.Repos, r)
	return r, nil
}

func (c *collection) addPR(pr *pullRequest) *pullRequest {
	c.nextPR++
	pr.ID = c.nextPR
	pr.Status = "active"
	pr.Created = seedTime.Add(time.Duration(pr.ID) * time.Hour)
	c.PRs = append(c.PRs, pr)
	return pr
}

func (c *collection) addWorkItem(s *server, p *project, typ, title, state string, assigned, creator *identity, tags, description string, linked *pullRequest, comments []*wiComment) {
	c.nextWI++
	at := seedTime.Add(time.Duration(c.nextWI) * 24 * time.Hour)
	w := &workItem{ID: c.nextWI, Project: p, Rev: 1, Fields: map[string]any{
		"System.AreaPath": p.Name, "System.TeamProject": p.Name, "System.IterationPath": p.Name,
		"System.WorkItemType": typ, "System.State": state, "System.Reason": "Seeded",
		"System.CreatedDate": timestamp(at), "System.CreatedBy": creator,
		"System.ChangedDate": timestamp(at), "System.ChangedBy": creator,
		"System.Title": title, "Microsoft.VSTS.Common.Priority": 2, "Microsoft.VSTS.Common.StateChangeDate": timestamp(at), "System.CommentCount": len(comments),
	}}
	if assigned != nil {
		w.Fields["System.AssignedTo"] = assigned
	}
	if tags != "" {
		w.Fields["System.Tags"] = tags
	}
	if description != "" {
		w.Fields["System.Description"] = description
	}
	if state == "Done" {
		w.Fields["Microsoft.VSTS.Common.ClosedDate"] = timestamp(at)
	}
	if linked != nil {
		w.Relations = append(w.Relations, prRelation(linked))
	}
	for i, cm := range comments {
		c.nextCmt++
		cm.ID, cm.At = c.nextCmt, at.Add(time.Duration(i+1)*time.Hour)
		w.Comments = append(w.Comments, cm)
	}
	w.Updates = []map[string]any{{"id": 1, "rev": 1, "by": creator, "at": at, "fields": initialUpdateFields(w.Fields)}}
	c.WorkItems = append(c.WorkItems, w)
}

func prRelation(pr *pullRequest) map[string]any {
	return map[string]any{
		"rel":        "ArtifactLink",
		"url":        prArtifactID(pr),
		"attributes": map[string]any{"authorizedDate": timestamp(pr.Created), "id": pr.ID, "name": "Pull Request"},
	}
}

func prArtifactID(pr *pullRequest) string {
	return fmt.Sprintf("vstfs:///Git/PullRequestId/%s%%2F%s%%2F%d", pr.Repo.Project.ID, pr.Repo.ID, pr.ID)
}

func initialUpdateFields(fields map[string]any) map[string]any {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		out[k] = map[string]any{"newValue": v}
	}
	return out
}
