package main

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Basic process types. Their states are what T3 reads for filters.
var workItemTypes = []struct{ name, color string }{{"Issue", "339947"}, {"Epic", "E06C00"}, {"Task", "A4880A"}}

var workItemStates = []map[string]any{
	{"name": "To Do", "color": "b2b2b2", "category": "Proposed"},
	{"name": "Doing", "color": "007acc", "category": "InProgress"},
	{"name": "Done", "color": "339933", "category": "Completed"},
}

var reactionTypes = []string{"like", "dislike", "heart", "hooray", "smile", "confused"}

func (s *server) witAPI(q *req) {
	p := lower(q.parts)
	m := q.r.Method
	switch {
	case match(p, "_apis", "wit", "workitemtypes") && m == http.MethodGet && q.project != nil:
		var rows []any
		for _, t := range workItemTypes {
			rows = append(rows, map[string]any{
				"name": t.name, "referenceName": "Microsoft.VSTS.WorkItemTypes." + t.name, "description": t.name + " work item.",
				"color": t.color, "icon": map[string]any{"id": "icon_clipboard"}, "isDisabled": false,
				"states": workItemStates, "url": q.l.api(q.project.ID, "_apis", "wit", "workItemTypes", t.name),
			})
		}
		writeList(q.w, rows)
	case match(p, "_apis", "wit", "tags") && m == http.MethodGet && q.project != nil:
		s.tags(q)
	case match(p, "_apis", "wit", "wiql") && m == http.MethodPost && q.project != nil:
		s.wiql(q)
	case match(p, "_apis", "wit", "workitemsbatch") && m == http.MethodPost:
		s.workItemsBatch(q)
	case len(p) >= 4 && p[2] == "workitems" && strings.HasPrefix(q.parts[3], "$") && len(p) == 4 && m == http.MethodPost:
		s.createWorkItem(q, q.parts[3][1:])
	case len(p) >= 4 && p[2] == "workitems":
		id, _ := strconv.Atoi(p[3])
		w := q.c.workItem(id)
		if w == nil || (q.project != nil && w.Project != q.project) {
			serviceError(q.w, http.StatusNotFound, fmt.Sprintf("TF401232: Work item %s does not exist, or you do not have permissions to read it.", p[3]), "WorkItemUnauthorizedAccessException")
			return
		}
		s.workItemAPI(q, w, p[4:])
	default:
		notFound(q.w)
	}
}

func (s *server) workItemAPI(q *req, w *workItem, rest []string) {
	m := q.r.Method
	switch {
	case len(rest) == 0 && m == http.MethodGet:
		expand := strings.ToLower(q.r.URL.Query().Get("$expand"))
		writeJSON(q.w, http.StatusOK, s.workItemJSON(q.l, w, nil, expand == "relations" || expand == "all"))
	case len(rest) == 0 && m == http.MethodPatch:
		s.patchWorkItem(q, w)
	case match(rest, "updates") && m == http.MethodGet:
		var rows []any
		for _, u := range w.Updates {
			by := u["by"].(*identity)
			ref := q.l.identityRef(by)
			ref["name"] = by.Display + " <" + by.unique() + ">"
			rows = append(rows, map[string]any{
				"id": u["id"], "workItemId": w.ID, "rev": u["rev"], "revisedBy": ref,
				"revisedDate": timestamp(u["at"].(time.Time)), "fields": s.render(q.l, u["fields"]),
				"url": q.l.api(w.Project.ID, "_apis", "wit", "workItems", strconv.Itoa(w.ID), "updates", strconv.Itoa(u["id"].(int))),
			})
		}
		writeList(q.w, page(rows, q.r))
	case match(rest, "comments") && m == http.MethodGet:
		s.listComments(q, w)
	case match(rest, "comments") && m == http.MethodPost:
		var body struct{ Text string }
		if !decodeBody(q, &body) {
			return
		}
		q.c.nextCmt++
		cm := &wiComment{ID: q.c.nextCmt, Text: body.Text, By: s.viewer, At: time.Now().UTC()}
		w.Comments = append(w.Comments, cm)
		w.Fields["System.CommentCount"] = len(w.Comments)
		writeJSON(q.w, http.StatusOK, s.commentJSON(q.l, w, cm, false))
	case len(rest) >= 2 && rest[0] == "comments":
		id, _ := strconv.Atoi(rest[1])
		i := slices.IndexFunc(w.Comments, func(c *wiComment) bool { return c.ID == id })
		if i < 0 {
			serviceError(q.w, http.StatusNotFound, fmt.Sprintf("TF401227: Comment %d does not exist.", id), "WorkItemCommentNotFoundException")
			return
		}
		s.commentAPI(q, w, i, rest[2:])
	default:
		notFound(q.w)
	}
}

func (s *server) commentAPI(q *req, w *workItem, i int, rest []string) {
	cm := w.Comments[i]
	m := q.r.Method
	switch {
	case len(rest) == 0 && m == http.MethodGet:
		writeJSON(q.w, http.StatusOK, s.commentJSON(q.l, w, cm, true))
	case len(rest) == 0 && m == http.MethodDelete:
		w.Comments = slices.Delete(w.Comments, i, i+1)
		w.Fields["System.CommentCount"] = len(w.Comments)
		q.w.WriteHeader(http.StatusNoContent)
	case len(rest) == 2 && rest[0] == "reactions" && (m == http.MethodPut || m == http.MethodDelete):
		typ := rest[1]
		if !slices.Contains(reactionTypes, typ) {
			serviceError(q.w, http.StatusBadRequest, "Unknown reaction type "+typ, "InvalidArgumentValueException")
			return
		}
		if cm.Reactions == nil {
			cm.Reactions = map[string]map[string]bool{}
		}
		if cm.Reactions[typ] == nil {
			cm.Reactions[typ] = map[string]bool{}
		}
		if m == http.MethodPut {
			cm.Reactions[typ][s.viewer.ID] = true
		} else {
			delete(cm.Reactions[typ], s.viewer.ID)
		}
		writeJSON(q.w, http.StatusOK, s.reactionJSON(cm, typ))
	default:
		notFound(q.w)
	}
}

func (s *server) reactionJSON(cm *wiComment, typ string) map[string]any {
	return map[string]any{"type": typ, "count": len(cm.Reactions[typ]), "isCurrentUserEngaged": cm.Reactions[typ][s.viewer.ID], "commentId": cm.ID}
}

func (s *server) commentJSON(l links, w *workItem, cm *wiComment, reactions bool) map[string]any {
	out := map[string]any{
		"workItemId": w.ID, "id": cm.ID, "version": 1, "text": cm.Text,
		"createdBy": l.identityRef(cm.By), "createdDate": timestamp(cm.At),
		"modifiedBy": l.identityRef(cm.By), "modifiedDate": timestamp(cm.At),
		"format": "markdown", "renderedText": "<p>" + html.EscapeString(cm.Text) + "</p>\n",
		"url": l.api(w.Project.ID, "_apis", "wit", "workItems", strconv.Itoa(w.ID), "comments", strconv.Itoa(cm.ID)),
	}
	if reactions {
		rows := []any{}
		for _, typ := range reactionTypes {
			if len(cm.Reactions[typ]) > 0 {
				rows = append(rows, s.reactionJSON(cm, typ))
			}
		}
		out["reactions"], out["mentions"] = rows, []any{}
	}
	return out
}

// listComments pages with an opaque continuation token, like the server.
func (s *server) listComments(q *req, w *workItem) {
	query := q.r.URL.Query()
	comments := slices.Clone(w.Comments)
	if strings.EqualFold(query.Get("order"), "desc") {
		slices.Reverse(comments)
	}
	start, _ := strconv.Atoi(strings.TrimPrefix(query.Get("continuationToken"), "mock-"))
	top, err := strconv.Atoi(query.Get("$top"))
	if err != nil || top <= 0 {
		top = 200
	}
	start = min(start, len(comments))
	end := min(start+top, len(comments))
	rows := []any{}
	for _, cm := range comments[start:end] {
		rows = append(rows, s.commentJSON(q.l, w, cm, strings.EqualFold(query.Get("$expand"), "reactions")))
	}
	out := map[string]any{"totalCount": len(comments), "count": len(rows), "comments": rows}
	if end < len(comments) {
		out["continuationToken"] = fmt.Sprintf("mock-%d", end)
		q.w.Header().Set("X-MS-ContinuationToken", fmt.Sprintf("mock-%d", end))
		out["nextPage"] = q.l.api(w.Project.ID, "_apis", "wit", "workItems", strconv.Itoa(w.ID), "comments") + "?continuationToken=" + fmt.Sprintf("mock-%d", end)
	}
	writeJSON(q.w, http.StatusOK, out)
}

func (s *server) tags(q *req) {
	seen := map[string]bool{}
	var rows []any
	for _, w := range q.c.WorkItems {
		if w.Project != q.project {
			continue
		}
		for _, tag := range splitTags(w.Fields["System.Tags"]) {
			if !seen[strings.ToLower(tag)] {
				seen[strings.ToLower(tag)] = true
				id := guid("tag", tag)
				rows = append(rows, map[string]any{"id": id, "name": tag, "url": q.l.api(q.project.ID, "_apis", "wit", "tags", id), "lastUpdated": timestamp(seedTime)})
			}
		}
	}
	writeList(q.w, rows)
}

func splitTags(v any) []string {
	raw, _ := v.(string)
	var tags []string
	for _, t := range strings.Split(raw, ";") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// render turns stored identities into identity references.
func (s *server) render(l links, v any) any {
	switch v := v.(type) {
	case *identity:
		return l.identityRef(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = s.render(l, x)
		}
		return out
	}
	return v
}

func (s *server) workItemJSON(l links, w *workItem, fields []string, relations bool) map[string]any {
	id := strconv.Itoa(w.ID)
	self := l.api(w.Project.ID, "_apis", "wit", "workItems", id)
	out := map[string]any{"id": w.ID, "rev": w.Rev, "url": self}
	values := map[string]any{}
	if fields == nil {
		for k, v := range w.Fields {
			values[k] = s.render(l, v)
		}
		out["_links"] = map[string]any{
			"self": map[string]any{"href": self}, "workItemUpdates": map[string]any{"href": self + "/updates"},
			"workItemComments": map[string]any{"href": self + "/comments"},
			"html":             map[string]any{"href": l.coll() + "/" + w.Project.ID + "/_workitems/edit/" + id},
		}
	} else {
		for _, k := range fields {
			if k == "System.Id" {
				values[k] = w.ID
			} else if v, ok := w.Fields[k]; ok {
				values[k] = s.render(l, v)
			}
		}
	}
	out["fields"] = values
	if relations && len(w.Relations) > 0 {
		out["relations"] = w.Relations
	}
	return out
}

func (s *server) workItemsBatch(q *req) {
	var body struct {
		IDs    []int    `json:"ids"`
		Fields []string `json:"fields"`
	}
	if !decodeBody(q, &body) {
		return
	}
	var rows []any
	for _, id := range body.IDs {
		if w := q.c.workItem(id); w != nil && (q.project == nil || w.Project == q.project) {
			rows = append(rows, s.workItemJSON(q.l, w, body.Fields, false))
		}
	}
	writeList(q.w, rows)
}

type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

func (s *server) createWorkItem(q *req, typ string) {
	if !slices.ContainsFunc(workItemTypes, func(t struct{ name, color string }) bool { return strings.EqualFold(t.name, typ) }) {
		serviceError(q.w, http.StatusNotFound, "TF401326: Invalid work item type "+typ+".", "WorkItemTypeNotFoundException")
		return
	}
	var ops []patchOp
	if !decodeBody(q, &ops) {
		return
	}
	now := time.Now().UTC()
	q.c.nextWI++
	w := &workItem{ID: q.c.nextWI, Project: q.project, Fields: map[string]any{
		"System.AreaPath": q.project.Name, "System.TeamProject": q.project.Name, "System.IterationPath": q.project.Name,
		"System.WorkItemType": typ, "System.State": "To Do", "System.Reason": "Added to backlog",
		"System.CreatedDate": timestamp(now), "System.CreatedBy": s.viewer, "System.CommentCount": 0,
		"Microsoft.VSTS.Common.StateChangeDate": timestamp(now),
		"Microsoft.VSTS.Common.Priority":        2,
	}}
	if err := s.applyPatch(w, ops, now); err != nil {
		q.c.nextWI--
		serviceError(q.w, http.StatusBadRequest, err.Error(), "WorkItemFieldInvalidException")
		return
	}
	q.c.WorkItems = append(q.c.WorkItems, w)
	writeJSON(q.w, http.StatusOK, s.workItemJSON(q.l, w, nil, true))
}

func (s *server) patchWorkItem(q *req, w *workItem) {
	var ops []patchOp
	if !decodeBody(q, &ops) {
		return
	}
	if err := s.applyPatch(w, ops, time.Now().UTC()); err != nil {
		serviceError(q.w, http.StatusBadRequest, err.Error(), "WorkItemFieldInvalidException")
		return
	}
	writeJSON(q.w, http.StatusOK, s.workItemJSON(q.l, w, nil, true))
}

// applyPatch applies JSON Patch to fields and relations and records the
// change as one update. Server 2022.2 accepts an empty tag string but keeps
// existing tags; the mock does the same.
func (s *server) applyPatch(w *workItem, ops []patchOp, now time.Time) error {
	next := make(map[string]any, len(w.Fields))
	for k, v := range w.Fields {
		next[k] = v
	}
	relations := slices.Clone(w.Relations)
	for _, op := range ops {
		if op.Path == "/relations/-" && op.Op == "add" {
			rel, ok := op.Value.(map[string]any)
			if !ok {
				return fmt.Errorf("a relation must be an object")
			}
			relations = append(relations, rel)
			continue
		}
		field, ok := strings.CutPrefix(op.Path, "/fields/")
		if !ok {
			return fmt.Errorf("unsupported patch path %s", op.Path)
		}
		switch op.Op {
		case "remove":
			delete(next, field)
		case "add", "replace":
			value, err := s.fieldValue(field, op.Value, next)
			if err != nil {
				return err
			}
			if value == nil {
				delete(next, field)
			} else {
				next[field] = value
			}
		default:
			return fmt.Errorf("unsupported patch operation %s", op.Op)
		}
	}
	if title, _ := next["System.Title"].(string); strings.TrimSpace(title) == "" {
		return fmt.Errorf("TF401320: Rule Error for field Title. Error code: Required, HasValues, LimitedToValues, AllowsOldValue, InvalidEmpty")
	}
	if state := next["System.State"]; state != w.Fields["System.State"] && w.Fields["System.State"] != nil {
		next["System.Reason"] = "Moved to state " + fmt.Sprint(state)
		next["Microsoft.VSTS.Common.StateChangeDate"] = timestamp(now)
		if state == "Done" {
			next["Microsoft.VSTS.Common.ClosedDate"] = timestamp(now)
		} else {
			delete(next, "Microsoft.VSTS.Common.ClosedDate")
		}
	}
	next["System.ChangedDate"], next["System.ChangedBy"] = timestamp(now), s.viewer
	changes := map[string]any{}
	keys := map[string]bool{}
	for k := range next {
		keys[k] = true
	}
	for k := range w.Fields {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		old, had := w.Fields[k]
		cur, has := next[k]
		if had && has && fmt.Sprint(old) == fmt.Sprint(cur) {
			continue
		}
		change := map[string]any{}
		if had {
			change["oldValue"] = old
		}
		if has {
			change["newValue"] = cur
		}
		changes[k] = change
	}
	w.Fields, w.Relations = next, relations
	w.Rev++
	w.Updates = append(w.Updates, map[string]any{"id": len(w.Updates) + 1, "rev": w.Rev, "by": s.viewer, "at": now, "fields": changes})
	return nil
}

func (s *server) fieldValue(field string, value any, next map[string]any) (any, error) {
	switch field {
	case "System.AssignedTo":
		name, _ := value.(string)
		if strings.TrimSpace(name) == "" {
			return nil, nil
		}
		who := s.findIdentity(name)
		if who == nil {
			return nil, fmt.Errorf("TF401320: Rule Error for field Assigned To. Error code: ValidIdentity, InvalidNotFoundInGroup. The identity %q is unknown", name)
		}
		return who, nil
	case "System.Tags":
		tags := splitTags(value)
		if len(tags) == 0 {
			return next[field], nil
		}
		return strings.Join(tags, "; "), nil
	case "System.State":
		state, _ := value.(string)
		if !slices.ContainsFunc(workItemStates, func(st map[string]any) bool { return st["name"] == state }) {
			return nil, fmt.Errorf("TF401320: Rule Error for field State. Error code: Required, HasValues, LimitedToValues, AllowsOldValue, InvalidListValue")
		}
		return state, nil
	}
	if text, ok := value.(string); ok && text == "" {
		return nil, nil
	}
	return value, nil
}

var (
	wiqlProject = regexp.MustCompile(`\[System\.TeamProject\]\s*=\s*'((?:[^']|'')*)'`)
	wiqlText    = regexp.MustCompile(`\[System\.(Title|Tags)\]\s+CONTAINS\s+'((?:[^']|'')*)'`)
	wiqlStates  = regexp.MustCompile(`\[System\.State\]\s+(NOT IN|IN)\s+\(([^)]*)\)`)
	wiqlPerson  = regexp.MustCompile(`\[System\.(AssignedTo|CreatedBy)\]\s*=\s*(@Me|'(?:[^']|'')*')`)
	wiqlCursor  = regexp.MustCompile(`\(\[System\.(ChangedDate|CreatedDate)\]\s*<\s*'([^']*)'\s+OR\s+\(\[System\.(?:ChangedDate|CreatedDate)\]\s*=\s*'[^']*'\s+AND\s+\[System\.Id\]\s*<\s*(\d+)\)\)`)
	wiqlOrder   = regexp.MustCompile(`ORDER BY\s+\[System\.(ChangedDate|CreatedDate)\]`)
	wiqlQuoted  = regexp.MustCompile(`'((?:[^']|'')*)'`)
)

func unquote(s string) string { return strings.ReplaceAll(strings.Trim(s, "'"), "''", "'") }

// wiql evaluates the clauses stock T3 builds; other clauses are ignored.
func (s *server) wiql(q *req) {
	var body struct{ Query string }
	if !decodeBody(q, &body) {
		return
	}
	text := body.Query
	if m := wiqlProject.FindStringSubmatch(text); m != nil && !strings.EqualFold(unquote(m[1]), q.project.Name) {
		writeJSON(q.w, http.StatusOK, s.wiqlResult(q, nil, "ChangedDate"))
		return
	}
	dateField := "ChangedDate"
	if m := wiqlOrder.FindStringSubmatch(text); m != nil {
		dateField = m[1]
	}
	var hits []*workItem
	for _, w := range q.c.WorkItems {
		if w.Project == q.project && wiqlMatches(s, text, w) {
			hits = append(hits, w)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := fmt.Sprint(hits[i].Fields["System."+dateField]), fmt.Sprint(hits[j].Fields["System."+dateField])
		if a != b {
			return a > b
		}
		return hits[i].ID > hits[j].ID
	})
	if top, err := strconv.Atoi(q.r.URL.Query().Get("$top")); err == nil && top >= 0 && top < len(hits) {
		hits = hits[:top]
	}
	writeJSON(q.w, http.StatusOK, s.wiqlResult(q, hits, dateField))
}

func wiqlMatches(s *server, text string, w *workItem) bool {
	for _, m := range wiqlText.FindAllStringSubmatch(text, -1) {
		needle := strings.ToLower(unquote(m[2]))
		if m[1] == "Title" && !strings.Contains(strings.ToLower(fmt.Sprint(w.Fields["System.Title"])), needle) {
			return false
		}
		if m[1] == "Tags" && !slices.ContainsFunc(splitTags(w.Fields["System.Tags"]), func(t string) bool { return strings.ToLower(t) == needle }) {
			return false
		}
	}
	for _, m := range wiqlStates.FindAllStringSubmatch(text, -1) {
		var states []string
		for _, v := range wiqlQuoted.FindAllString(m[2], -1) {
			states = append(states, strings.ToLower(unquote(v)))
		}
		in := slices.Contains(states, strings.ToLower(fmt.Sprint(w.Fields["System.State"])))
		if in != (m[1] == "IN") {
			return false
		}
	}
	for _, m := range wiqlPerson.FindAllStringSubmatch(text, -1) {
		who, _ := w.Fields["System."+m[1]].(*identity)
		want := s.viewer
		if m[2] != "@Me" {
			want = s.findIdentity(unquote(m[2]))
		}
		if who == nil || who != want {
			return false
		}
	}
	if m := wiqlCursor.FindStringSubmatch(text); m != nil {
		cursor, err := time.Parse(time.RFC3339Nano, m[2])
		date, err2 := time.Parse(time.RFC3339Nano, fmt.Sprint(w.Fields["System."+m[1]]))
		id, _ := strconv.Atoi(m[3])
		if err == nil && err2 == nil && (date.After(cursor) || (date.Equal(cursor) && w.ID >= id)) {
			return false
		}
	}
	return true
}

func (s *server) wiqlResult(q *req, hits []*workItem, dateField string) map[string]any {
	rows := []any{}
	for _, w := range hits {
		rows = append(rows, map[string]any{"id": w.ID, "url": q.l.api(w.Project.ID, "_apis", "wit", "workItems", strconv.Itoa(w.ID))})
	}
	return map[string]any{
		"queryType": "flat", "queryResultType": "workItem", "asOf": timestamp(time.Now()),
		"columns":     []any{map[string]any{"referenceName": "System.Id", "name": "ID", "url": q.l.api("_apis", "wit", "fields", "System.Id")}},
		"sortColumns": []any{map[string]any{"field": map[string]any{"referenceName": "System." + dateField, "name": strings.TrimSuffix(dateField, "Date") + " Date", "url": q.l.api("_apis", "wit", "fields", "System."+dateField)}, "descending": true}},
		"workItems":   rows,
	}
}
