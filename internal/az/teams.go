package az

import (
	"context"
	"errors"
	"net/url"
)

const coreReadVersion = "7.1"

type teamRead struct {
	project string
	team    string
	query   url.Values
}

func parseTeamRead(command string, opts map[string]string) (teamRead, error) {
	project := opts["project"]
	if (project != "" || opts["detect"] == "false") && !validSegment(project) {
		return teamRead{}, errors.New("team lookup requires a valid project")
	}
	team := opts["team"]
	if command == "devops team list-member" && !validSegment(team) {
		return teamRead{}, errors.New("list-member requires a valid --team")
	}
	query, err := pageQuery(coreReadVersion, opts)
	return teamRead{project: project, team: team, query: query}, err
}

// The team commands in the Azure CLI return the value array from the Core API.
func (read teamRead) run(ctx context.Context, client *Client, scope Scope) (any, error) {
	project := read.project
	if project == "" {
		project = scope.Project
	}
	if project == "" {
		return nil, usageError{errors.New("team lookup requires a project")}
	}
	path := []string{"_apis", "projects", project, "teams"}
	if read.team != "" {
		path = append(path, read.team, "members")
	}
	return getRepoPage(ctx, client, path, read.query)
}
