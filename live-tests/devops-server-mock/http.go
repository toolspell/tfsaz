package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

// catalog.json is the resource-location catalog recorded from an Azure
// DevOps Server 2022.2 collection (OPTIONS _apis?allHostTypes=true).
//
//go:embed catalog.json
var catalog []byte

// supportedVersions are the api-version values TFSaz and stock T3 send.
// Catalog-resolved calls can carry the bare 7.1-preview.
var supportedVersions = map[string]bool{
	"5.0": true, "5.0-preview.1": true, "7.1": true, "7.1-preview": true, "7.1-preview.1": true, "7.1-preview.4": true,
}

// apiVersion reads the version from the query or, as Azure CLI's invoke
// sends it, from the api-version parameter of the Accept header.
func apiVersion(r *http.Request) string {
	if v := r.URL.Query().Get("api-version"); v != "" {
		return v
	}
	for _, accept := range strings.Split(r.Header.Get("Accept"), ",") {
		if _, params, err := mime.ParseMediaType(strings.TrimSpace(accept)); err == nil && params["api-version"] != "" {
			return params["api-version"]
		}
	}
	return ""
}

// req is one request split at the collection, with an optional project.
type req struct {
	w       http.ResponseWriter
	r       *http.Request
	c       *collection
	project *project
	parts   []string // after the collection and project
	l       links
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.journaled(w, r, s.serveCollection)
}

func (s *server) journaled(w http.ResponseWriter, r *http.Request, serve func(http.ResponseWriter, *http.Request)) {
	entry := newRequestEntry(r, s.cfg.pat)
	response := &journalResponse{ResponseWriter: w}
	defer func() {
		entry.Status = response.status
		if entry.Status == 0 {
			entry.Status = http.StatusOK
		}
		s.journal.record(entry)
	}()
	serve(response, r)
}

func (s *server) serveCollection(w http.ResponseWriter, r *http.Request) {
	virtualDir, segments := splitCollectionPath(r.URL.Path)
	if segments[0] == "" {
		s.page(w, "Azure DevOps Server mock", "Collections: "+strings.Join(s.collectionNames(), ", "))
		return
	}
	var c *collection
	for _, candidate := range s.collections {
		if strings.EqualFold(candidate.Name, segments[0]) {
			c = candidate
		}
	}
	if c == nil {
		notFound(w)
		return
	}
	q := &req{w: w, r: r, c: c, parts: segments[1:], l: s.links(r, virtualDir, c)}
	if len(q.parts) >= 2 && !strings.EqualFold(q.parts[0], "_apis") && !strings.EqualFold(q.parts[0], "_git") {
		q.project = c.project(q.parts[0])
		if q.project == nil {
			if strings.EqualFold(q.parts[1], "_apis") {
				serviceError(w, http.StatusNotFound, "TF200016: The following project does not exist: "+q.parts[0]+".", "ProjectDoesNotExistWithNameException")
			} else {
				notFound(w)
			}
			return
		}
		q.parts = q.parts[1:]
	}
	if len(q.parts) == 0 {
		s.page(w, c.Name, "Collection home page.")
		return
	}
	switch strings.ToLower(q.parts[0]) {
	case "_apis":
		if !s.authorized(q) {
			return
		}
		if r.Method != http.MethodOptions {
			if v := apiVersion(r); !supportedVersions[v] {
				serviceError(w, http.StatusBadRequest, fmt.Sprintf("The requested REST API version of %s is not supported by this mock.", v), "VssVersionOutOfRangeException")
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.api(q)
	case "_git":
		s.gitPath(q)
	default:
		s.webPage(q)
	}
}

// authorized accepts Basic authentication with any user name and the PAT,
// and answers like the server otherwise.
func (s *server) authorized(q *req) bool {
	if _, password, ok := q.r.BasicAuth(); ok && password == s.cfg.pat {
		return true
	}
	w := q.w
	w.Header().Add("WWW-Authenticate", "Bearer")
	w.Header().Add("WWW-Authenticate", `Basic realm="`+q.l.coll()+`/"`)
	w.Header().Set("X-TFS-ServiceError", "TF400813%3A%20Resource%20not%20available%20for%20anonymous%20access.%20Client%20authentication%20required.")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprint(w, "<html><body>TF400813: Resource not available for anonymous access. Client authentication required.</body></html>")
	return false
}

func (s *server) api(q *req) {
	p := lower(q.parts)
	m := q.r.Method
	switch {
	case len(p) == 1 && m == http.MethodOptions:
		q.w.Header().Set("Content-Type", "application/json; charset=utf-8")
		q.w.Header().Set("Content-Location", q.l.origin+q.r.URL.RequestURI())
		_, _ = q.w.Write(catalog)
	case match(p, "_apis", "connectiondata") && m == http.MethodGet:
		s.connectionData(q)
	case match(p, "_apis", "identities") && m == http.MethodGet:
		s.identities(q)
	case len(p) >= 2 && p[1] == "projects" && q.project == nil:
		s.projects(q)
	case len(p) >= 2 && p[1] == "git":
		s.gitAPI(q)
	case match(p, "_apis", "policy", "evaluations") && m == http.MethodGet:
		s.policyEvaluations(q)
	case len(p) >= 2 && p[1] == "wit":
		s.witAPI(q)
	default:
		notFound(q.w)
	}
}

func (s *server) connectionData(q *req) {
	me := q.l.identity(s.viewer, false)
	q.w.Header().Set("Content-Location", q.l.origin+q.r.URL.RequestURI())
	writeJSON(q.w, http.StatusOK, map[string]any{
		"authenticatedUser": me, "authorizedUser": me,
		"instanceId": q.c.ID, "deploymentId": guid("deployment"), "deploymentType": "onPremises",
		"locationServiceData":             map[string]any{"clientCacheTimeToLive": 86400, "defaultAccessMappingMoniker": "PublicAccessMapping", "lastChangeId": 1, "lastChangeId64": 1},
		"webApplicationRelativeDirectory": strings.TrimPrefix(q.l.virtualDir+"/"+q.c.Name, "/") + "/",
	})
}

func (s *server) identities(q *req) {
	query := q.r.URL.Query()
	var found []any
	if ids := query.Get("identityIds"); ids != "" {
		for _, id := range strings.Split(ids, ",") {
			if p := s.findIdentity(id); p != nil && strings.EqualFold(p.ID, strings.TrimSpace(id)) {
				found = append(found, q.l.identity(p, true))
			}
		}
	} else if value := query.Get("filterValue"); value != "" {
		if p := s.findIdentity(value); p != nil {
			found = append(found, q.l.identity(p, true))
		}
	} else {
		serviceError(q.w, http.StatusBadRequest, "A filter or identity IDs are required.", "ArgumentException")
		return
	}
	writeList(q.w, found)
}

func (s *server) projects(q *req) {
	p := q.parts
	if q.r.Method != http.MethodGet {
		notFound(q.w)
		return
	}
	if len(p) == 2 {
		var rows []any
		for _, pr := range q.c.Projects {
			rows = append(rows, q.l.project(pr))
		}
		writeList(q.w, rows)
		return
	}
	pr := q.c.project(p[2])
	if pr == nil {
		serviceError(q.w, http.StatusNotFound, "TF200016: The following project does not exist: "+p[2]+".", "ProjectDoesNotExistWithNameException")
		return
	}
	team := map[string]any{
		"id": guid("team", q.c.Name, pr.Name), "name": pr.Name + " Team",
		"description": "The default project team.", "projectName": pr.Name, "projectId": pr.ID,
	}
	team["url"] = q.l.api("_apis", "projects", pr.ID, "teams", team["id"].(string))
	team["identityUrl"] = q.l.api("_apis", "Identities", team["id"].(string))
	switch {
	case len(p) == 3:
		writeJSON(q.w, http.StatusOK, q.l.project(pr))
	case len(p) == 4 && strings.EqualFold(p[3], "teams"):
		writeList(q.w, []any{team})
	case len(p) == 6 && strings.EqualFold(p[3], "teams") && strings.EqualFold(p[5], "members"):
		if !strings.EqualFold(p[4], team["id"].(string)) && !strings.EqualFold(p[4], team["name"].(string)) {
			serviceError(q.w, http.StatusNotFound, "The team with id "+p[4]+" does not exist.", "TeamNotFoundException")
			return
		}
		var members []any
		for _, person := range s.people {
			members = append(members, map[string]any{"identity": q.l.identityRef(person)})
		}
		writeList(q.w, page(members, q.r))
	default:
		notFound(q.w)
	}
}

func lower(parts []string) []string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.ToLower(p)
	}
	return out
}

func match(parts []string, want ...string) bool {
	if len(parts) != len(want) {
		return false
	}
	for i := range want {
		if want[i] != "*" && parts[i] != want[i] {
			return false
		}
	}
	return true
}

// page applies $skip and $top to rows.
func page[T any](rows []T, r *http.Request) []T {
	query := r.URL.Query()
	if skip, err := strconv.Atoi(query.Get("$skip")); err == nil && skip > 0 {
		if skip >= len(rows) {
			return nil
		}
		rows = rows[skip:]
	}
	if top, err := strconv.Atoi(query.Get("$top")); err == nil && top >= 0 && top < len(rows) {
		rows = rows[:top]
	}
	return rows
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8; api-version=7.1")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Print(err)
	}
}

func writeList[T any](w http.ResponseWriter, rows []T) {
	if rows == nil {
		rows = []T{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(rows), "value": rows})
}

// serviceError writes the server's JSON error shape.
func serviceError(w http.ResponseWriter, status int, message, typeKey string) {
	writeJSON(w, status, map[string]any{
		"$id": "1", "innerException": nil, "message": message,
		"typeName": "Microsoft.TeamFoundation.Mock." + typeKey + ", Microsoft.TeamFoundation.Mock",
		"typeKey":  typeKey, "errorCode": 0, "eventId": 3000,
	})
}

func notFound(w http.ResponseWriter) {
	serviceError(w, http.StatusNotFound, "The mock does not serve this route.", "RouteNotFoundException")
}

func decodeBody(q *req, v any) bool {
	if err := json.NewDecoder(q.r.Body).Decode(v); err != nil {
		serviceError(q.w, http.StatusBadRequest, "The request body is not valid JSON: "+err.Error(), "InvalidArgumentValueException")
		return false
	}
	return true
}
