package az

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	witReadVersion          = "7.1"
	commentsPreview         = "7.1-preview.4"
	maxWorkItemID           = 1<<31 - 1
	maxWorkItemPage         = 200
	maxWitInputBytes        = 128 << 10
	maxWIQLChars            = 32 << 10
	workItemTypesProjection = "{value: value[].{name: name, isDisabled: isDisabled || `false`, states: (states || `[]`)[].{name: name, category: category}}}"
)

type workItemRead struct {
	id    string
	query url.Values
}

func parseWorkItemShow(opts map[string]string) (workItemRead, error) {
	id, err := invokeNumber("id", opts["id"], 1, maxWorkItemID)
	if err != nil {
		return workItemRead{}, errors.New("--id must be a positive work item number")
	}
	expand := strings.ToLower(opts["expand"])
	switch expand {
	case "":
		expand = "all" // Azure CLI defaults to all when --expand is omitted.
	case "none", "relations", "fields", "links", "all":
	default:
		return workItemRead{}, errors.New("--expand expects none, relations, fields, links, or all")
	}
	return workItemRead{id: id, query: url.Values{"api-version": {witReadVersion}, "$expand": {expand}}}, nil
}

// Server can return 401 for an otherwise valid PAT missing a Boards or team
// read permission. Verify the credential at the same collection before asking
// the caller to log in again.
func (c *Client) boardsReadError(ctx context.Context, err error) error {
	if !errors.Is(err, errRejectedPAT) {
		return err
	}
	if _, probeErr := c.authenticatedUser(ctx); probeErr != nil {
		return probeErr
	}
	return errors.New("Azure DevOps authenticated the PAT but denied this read. Check the PAT's read scopes and the account's project permissions")
}

func (read workItemRead) run(ctx context.Context, client *Client, scope Scope) (any, error) {
	var item map[string]json.RawMessage
	if err := client.Get(ctx, []string{"_apis", "wit", "workItems", read.id}, read.query, &item); err != nil {
		return nil, err
	}
	if workItemID(item) != read.id {
		return nil, fmt.Errorf("Azure DevOps response does not identify work item %s", read.id)
	}
	setWorkItemLink(item, scope.Organization, read.id)
	return item, nil
}

func parseWitInvoke(opts map[string]string, cwd string) (invokeRead, error) {
	var read invokeRead
	resource := strings.ToLower(opts["resource"])
	switch resource {
	case "workitemtypes", "wiql", "workitemsbatch", "comments", "updates", "tags":
	default:
		return read, errors.New("unsupported devops invoke work item resource")
	}
	version := opts["api-version"]
	if (resource == "comments" && version != "7.1-preview") || (resource != "comments" && version != witReadVersion) {
		return read, errors.New("work item reads require --api-version 7.1 or, for comments, 7.1-preview")
	}
	accept := opts["accept-media-type"]
	if (accept != "" && accept != "application/json") || opts["out-file"] != "" {
		return read, errors.New("work item reads only support JSON output")
	}
	method := strings.ToUpper(opts["http-method"])
	switch resource {
	case "wiql", "workitemsbatch":
		if method != http.MethodPost || opts["media-type"] != "application/json" ||
			!validInvokeText(opts["in-file"], maxInvokeItemPathBytes) {
			return read, errors.New("this work item resource requires POST, JSON --media-type and --in-file")
		}
	default:
		if (method != "" && method != http.MethodGet) || opts["media-type"] != "" || opts["in-file"] != "" {
			return read, errors.New("this work item resource supports JSON GET only")
		}
	}
	projection := opts["query"]
	if projection != "" && (resource != "workitemtypes" || projection != workItemTypesProjection) {
		return read, errors.New("unsupported work item --query projection")
	}
	route, err := invokeParameters(opts["route-parameters"], "route")
	if err != nil {
		return read, err
	}
	params, err := invokeParameters(opts["query-parameters"], "query")
	if err != nil {
		return read, err
	}
	project := route["project"]
	if !validInvokeText(project, 255) || !validSegment(project) {
		return read, errors.New("work item resource requires a valid project route parameter")
	}
	read.path = []string{project, "_apis", "wit"}
	switch resource {
	case "comments", "updates":
		idKey := "id"
		if resource == "comments" {
			idKey = "workItemId"
			version = commentsPreview
		}
		id, err := invokeNumber(idKey, route[idKey], 1, maxWorkItemID)
		if err != nil {
			return read, err
		}
		if len(route) != 2 {
			return read, errors.New("unexpected work item route parameter")
		}
		read.path = append(read.path, "workItems", id, resource)
	default:
		if len(route) != 1 {
			return read, errors.New("unexpected work item route parameter")
		}
		read.path = append(read.path, resource)
	}

	query, err := witQuery(resource, version, params)
	if err != nil {
		return read, err
	}
	read.query = query
	read.witResource = resource
	read.projectTypes = projection != ""
	if file := opts["in-file"]; file != "" {
		read.postBody, err = parseWitInput(resource, invokeLocalPath(cwd, file))
	}
	return read, err
}

func witQuery(resource, version string, params map[string]string) (url.Values, error) {
	query := url.Values{"api-version": {version}}
	for key, value := range params {
		var err error
		switch {
		case key == "$top" && resource == "wiql":
			value, err = invokeNumber(key, value, 1, 101) // T3 reads a 100-row page and one lookahead.
		case key == "$top" && (resource == "comments" || resource == "updates"):
			value, err = invokeNumber(key, value, 1, maxWorkItemPage)
		case key == "$skip" && resource == "updates":
			value, err = invokeNumber(key, value, 0, maxWorkItemID)
		case key == "timePrecision" && resource == "wiql":
			if value != "true" && value != "false" {
				err = errors.New("timePrecision expects true or false")
			}
		case key == "api-version" && resource == "comments":
			if value != commentsPreview {
				err = errors.New("unsupported work item comments preview version")
			}
		case key == "order" && resource == "comments":
			if value != "asc" && value != "desc" {
				err = errors.New("comment order expects asc or desc")
			}
		case key == "$expand" && resource == "comments":
			if value != "reactions" {
				err = errors.New("comment expansion supports reactions")
			}
		case key == "continuationToken" && resource == "comments":
			if !validInvokeText(value, 4096) || strings.HasPrefix(value, "@") {
				err = errors.New("invalid work item comments continuation token")
			}
		default:
			err = errors.New("unsupported work item query parameter")
		}
		if err != nil {
			return nil, err
		}
		query.Set(key, value)
	}
	return query, nil
}

var workItemField = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

func parseWitInput(resource, path string) ([]byte, error) {
	data, err := readInvokeInput(path, maxWitInputBytes)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) || !json.Valid(data) {
		return nil, errors.New("--in-file must contain valid UTF-8 JSON")
	}
	body, err := witInputObject(data)
	if err != nil {
		return nil, err
	}
	if resource == "wiql" {
		var query string
		if len(body) != 1 || json.Unmarshal(body["query"], &query) != nil ||
			utf8.RuneCountInString(query) > maxWIQLChars || strings.ContainsRune(query, 0) {
			return nil, errors.New("--in-file must contain a bounded WIQL query string")
		}
		words := strings.Fields(query)
		if len(words) == 0 || !strings.EqualFold(words[0], "select") {
			return nil, errors.New("WIQL query must start with SELECT")
		}
		return json.Marshal(map[string]string{"query": query})
	}

	var batch struct {
		IDs    []int32  `json:"ids"`
		Fields []string `json:"fields"`
	}
	if len(body) != 2 || json.Unmarshal(body["ids"], &batch.IDs) != nil ||
		len(batch.IDs) == 0 || len(batch.IDs) > maxWorkItemPage ||
		json.Unmarshal(body["fields"], &batch.Fields) != nil ||
		len(batch.Fields) == 0 || len(batch.Fields) > maxWorkItemPage {
		return nil, errors.New("--in-file must contain arrays of 1 to 200 work item IDs and field names")
	}
	for _, id := range batch.IDs {
		if id <= 0 {
			return nil, errors.New("work item batch IDs must be positive integers")
		}
	}
	for _, field := range batch.Fields {
		if !workItemField.MatchString(field) {
			return nil, errors.New("work item batch contains an invalid field name")
		}
	}
	return json.Marshal(batch)
}

// JSON syntax has already been validated. Preserve the exact key names and
// reject repeated keys before interpreting the body fields.
func witInputObject(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("--in-file must contain a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errors.New("--in-file contains an invalid JSON object")
		}
		if key != "query" && key != "ids" && key != "fields" {
			return nil, errors.New("--in-file contains an unexpected JSON field")
		}
		if _, exists := fields[key]; exists {
			return nil, errors.New("--in-file contains a repeated JSON field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("--in-file contains an invalid JSON field")
		}
		fields[key] = value
	}
	return fields, nil
}

func workItemID(item map[string]json.RawMessage) string {
	var id int32
	if json.Unmarshal(item["id"], &id) != nil || id <= 0 {
		return ""
	}
	return strconv.FormatInt(int64(id), 10)
}

func setWorkItemLink(item map[string]json.RawMessage, organization, id string) {
	item["url"], _ = json.Marshal(organization + "/_apis/wit/workItems/" + id)
}

func rewriteWorkItemBatchLinks(response map[string]json.RawMessage, organization string) error {
	raw := response["value"]
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return errors.New("Azure DevOps returned an invalid work item batch")
	}
	// Keep the server's rows and count. T3 skips entries it cannot decode.
	for i, raw := range rows {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		id := workItemID(item)
		if id == "" {
			continue
		}
		setWorkItemLink(item, organization, id)
		encoded, err := json.Marshal(item)
		if err != nil {
			return err
		}
		rows[i] = encoded
	}
	response["value"], _ = json.Marshal(rows)
	return nil
}

type workItemState struct {
	Name     string `json:"name"`
	Category string `json:"category"`
}

type workItemType struct {
	Name       string          `json:"name"`
	IsDisabled bool            `json:"isDisabled"`
	States     []workItemState `json:"states"`
}

func writeWorkItemTypes(stdout io.Writer, raw json.RawMessage) error {
	var types []workItemType
	if json.Unmarshal(raw, &types) != nil || types == nil {
		return errors.New("Azure DevOps returned an invalid work item type list")
	}
	for i := range types {
		if types[i].States == nil {
			types[i].States = []workItemState{}
		}
	}
	return json.NewEncoder(stdout).Encode(struct {
		Value []workItemType `json:"value"`
	}{Value: types})
}
