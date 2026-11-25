package az

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type workItemPatchField struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

func (a app) boardsWrite(ctx context.Context, c *Client, scope Scope, command string, opts map[string]string) (any, error) {
	var method, id string
	var path []string
	switch command {
	case "boards work-item create":
		project := opts["project"]
		if project == "" && opts["detect"] != "false" {
			project = scope.Project
		}
		if !validInvokeText(project, 255) || !validSegment(project) {
			return nil, usageError{errors.New("work item creation requires a valid project")}
		}
		typ := opts["type"]
		if !validInvokeText(typ, 255) || !validSegment(typ) {
			return nil, usageError{errors.New("work item creation requires a valid --type")}
		}
		if strings.TrimSpace(opts["title"]) == "" {
			return nil, usageError{errors.New("work item creation requires --title")}
		}
		method = http.MethodPost
		path = []string{project, "_apis", "wit", "workItems", "$" + typ}
	case "boards work-item update":
		var err error
		id, err = invokeNumber("id", opts["id"], 1, maxWorkItemID)
		if err != nil {
			return nil, usageError{errors.New("--id must be a positive work item number")}
		}
		// The extension addresses updates by collection and ID. T3 checks the
		// item's project with a separate show call before sending an update.
		method = http.MethodPatch
		path = []string{"_apis", "wit", "workItems", id}
	default:
		return nil, usageError{errors.New("unsupported work item write command")}
	}

	patch := make([]workItemPatchField, 0)
	for _, field := range []struct{ option, name string }{
		{"title", "System.Title"},
		{"description", "System.Description"},
		{"state", "System.State"},
	} {
		if value, supplied := opts[field.option]; supplied {
			patch = append(patch, workItemPatchField{Op: "add", Path: "/fields/" + field.name, Value: value})
		}
	}
	if fields := opts["fields"]; fields != "" {
		for pair := range strings.SplitSeq(fields, "\x00") {
			field, value, ok := strings.Cut(pair, "=")
			if !ok || !workItemField.MatchString(field) {
				return nil, usageError{errors.New("--fields requires field=value pairs with valid field names")}
			}
			// Server 2022.2 accepts adding an empty tag string but keeps existing tags.
			if strings.EqualFold(field, "System.Tags") && value == "" {
				if method == http.MethodPatch {
					patch = append(patch, workItemPatchField{Op: "remove", Path: "/fields/System.Tags"})
				}
				continue
			}
			patch = append(patch, workItemPatchField{Op: "add", Path: "/fields/" + field, Value: value})
		}
	}

	var item map[string]json.RawMessage
	query := url.Values{"api-version": {witReadVersion}}
	if err := writeJSON(ctx, c, method, path, query, "application/json-patch+json", patch, &item); err != nil {
		return nil, err
	}
	responseID := workItemID(item)
	if id != "" && responseID != id {
		return nil, fmt.Errorf("Azure DevOps response does not identify work item %s; check the item before retrying", id)
	}
	if responseID == "" {
		return nil, errors.New("Azure DevOps response is missing the created work item ID; check the project before retrying")
	}
	setWorkItemLink(item, scope.Organization, responseID)
	return item, nil
}
