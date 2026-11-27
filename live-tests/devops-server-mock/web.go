package main

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// gitPath serves /{project}/_git/{repo}/...: Git smart HTTP with the PAT,
// and the repository, PR and commit pages without authentication.
func (s *server) gitPath(q *req) {
	if len(q.parts) < 2 {
		notFound(q.w)
		return
	}
	s.mu.Lock()
	rp := q.c.repo(q.project, q.parts[1])
	s.mu.Unlock()
	if rp == nil {
		http.NotFound(q.w, q.r)
		return
	}
	rest := q.parts[2:]
	tail := "/" + strings.Join(rest, "/")
	switch {
	case tail == "/info/refs" || tail == "/git-upload-pack" || tail == "/git-receive-pack":
		if s.authorized(q.w, q.r) {
			s.serveGitHTTP(q.w, q.r, rp, tail)
		}
	case len(rest) == 0:
		s.page(q.w, rp.Name, "Repository "+rp.Name+" in project "+rp.Project.Name+".")
	case len(rest) == 2 && strings.EqualFold(rest[0], "pullrequest"):
		s.page(q.w, "Pull request "+rest[1], "Pull request "+rest[1]+" in repository "+rp.Name+".")
	case len(rest) == 2 && strings.EqualFold(rest[0], "commit"):
		s.page(q.w, "Commit "+rest[1], "Commit "+rest[1]+" in repository "+rp.Name+".")
	default:
		http.NotFound(q.w, q.r)
	}
}

func (s *server) webPage(q *req) {
	p := lower(q.parts)
	switch {
	case q.project != nil && match(p, "_workitems", "edit", "*"):
		s.page(q.w, "Work item "+q.parts[2], "Work item "+q.parts[2]+" in project "+q.project.Name+".")
	case q.project != nil && match(p, "_build", "results"):
		id := q.r.URL.Query().Get("buildId")
		s.page(q.w, "Build "+id, "Build "+id+" in project "+q.project.Name+".")
	default:
		http.NotFound(q.w, q.r)
	}
}

// page writes a static page that shows the full URL the browser landed on,
// fragment included, in the element with ID "location".
func (s *server) page(w http.ResponseWriter, title, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>%[1]s - Azure DevOps Server mock</title></head>
<body>
<h1>%[1]s</h1>
<p id="description">%[2]s</p>
<p>Location: <code id="location"></code></p>
<script>document.getElementById("location").textContent = location.href;</script>
</body></html>
`, html.EscapeString(title), html.EscapeString(text))
}
