package main

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type identity struct {
	ID, Account, Display string
	sid                  int
}

type project struct {
	ID, Name, Description string
}

type repo struct {
	ID, Name string
	Project  *project
	Dir      string
}

type reviewer struct {
	Who      *identity
	Vote     int
	Required bool
}

type prComment struct {
	ID      int
	Author  *identity
	Content string
	At      time.Time
}

type prThread struct {
	ID       int
	Status   string
	Comments []prComment
}

type pullRequest struct {
	ID                     int
	Repo                   *repo
	Status                 string
	CreatedBy              *identity
	Created                time.Time
	Title, Description     string
	Source, Target         string
	Draft                  bool
	Reviewers              []*reviewer
	Threads                []prThread
	Labels                 []string
	AutoComplete           *identity
	Completion             map[string]any
	MergeCommit            string
	LastSource, LastTarget string
	Closed                 time.Time
	ClosedBy               *identity
	Checks                 []check
}

// check is one policy evaluation on a PR: a pipeline (build), a required
// status, or the minimum number of reviewers.
type check struct {
	Name, Kind, Status string
}

type wiComment struct {
	ID        int
	Text      string
	By        *identity
	At        time.Time
	Reactions map[string]map[string]bool
}

type workItem struct {
	ID        int
	Project   *project
	Rev       int
	Fields    map[string]any
	Relations []map[string]any
	Comments  []*wiComment
	Updates   []map[string]any
}

type collection struct {
	Name, ID  string
	Projects  []*project
	Repos     []*repo
	PRs       []*pullRequest
	WorkItems []*workItem
	nextPR    int
	nextWI    int
	nextCmt   int
}

type server struct {
	mu          sync.Mutex
	journal     requestJournal
	cfg         config
	gitRoot     string
	collections []*collection
	people      []*identity
	viewer      *identity
}

// guid derives a stable GUID from a name, so IDs survive restarts.
func guid(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

var seedTime = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

func newServer(cfg config) (*server, error) {
	data, err := filepath.Abs(cfg.data)
	if err != nil {
		return nil, err
	}
	s := &server{cfg: cfg, gitRoot: filepath.Join(data, "git")}
	for i, p := range [][2]string{
		{"tester", "Test User"}, {"rreviewer", "Rita Reviewer"}, {"oother", "Oscar Other"},
		{"aarcher", "Alice Archer"}, {"bbuilder", "Ben Builder"},
	} {
		s.people = append(s.people, &identity{ID: guid("identity", p[0]), Account: p[0], Display: p[1], sid: 1001 + i})
	}
	s.viewer = s.people[0]
	if err := s.seed(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *server) collectionNames() []string {
	var names []string
	for _, c := range s.collections {
		names = append(names, "/"+c.Name)
	}
	return names
}

func (s *server) person(account string) *identity {
	for _, p := range s.people {
		if p.Account == account {
			return p
		}
	}
	return nil
}

// findIdentity matches the spellings TFSaz and T3 send: GUID, account,
// DOMAIN\account, display name and "Display <DOMAIN\account>".
func (s *server) findIdentity(value string) *identity {
	value = strings.TrimSpace(value)
	if i := strings.LastIndex(value, "<"); i >= 0 && strings.HasSuffix(value, ">") {
		value = value[i+1 : len(value)-1]
	}
	for _, p := range s.people {
		for _, spelling := range []string{p.ID, p.Account, p.unique(), p.Display, p.Account + "@mock.invalid"} {
			if strings.EqualFold(value, spelling) {
				return p
			}
		}
	}
	return nil
}

func (p *identity) unique() string { return `MOCK\` + p.Account }

func (p *identity) windowsSID() string {
	return fmt.Sprintf("S-1-5-21-1000000000-2000000000-3000000000-%d", p.sid)
}

func (p *identity) subjectDescriptor() string {
	return "win." + base64.RawURLEncoding.EncodeToString([]byte(p.windowsSID()))
}

func (c *collection) project(name string) *project {
	for _, p := range c.Projects {
		if strings.EqualFold(p.Name, name) || strings.EqualFold(p.ID, name) {
			return p
		}
	}
	return nil
}

func (c *collection) repo(p *project, name string) *repo {
	for _, r := range c.Repos {
		if (p == nil || r.Project == p) && (strings.EqualFold(r.Name, name) || strings.EqualFold(r.ID, name)) {
			return r
		}
	}
	return nil
}

func (c *collection) pr(id int) *pullRequest {
	for _, pr := range c.PRs {
		if pr.ID == id {
			return pr
		}
	}
	return nil
}

func (c *collection) workItem(id int) *workItem {
	for _, w := range c.WorkItems {
		if w.ID == id {
			return w
		}
	}
	return nil
}

type links struct {
	origin, virtualDir, ssh string
	c                       *collection
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func splitCollectionPath(path string) (virtualDir string, segments []string) {
	segments = strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) > 1 && strings.EqualFold(segments[0], "tfs") {
		return "/" + segments[0], segments[1:]
	}
	return "", segments
}

func (s *server) links(r *http.Request, virtualDir string, c *collection) links {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return links{
		origin: requestScheme(r) + "://" + r.Host, virtualDir: virtualDir, c: c,
		ssh: net.JoinHostPort(strings.Trim(host, "[]"), s.cfg.sshPort),
	}
}

func (l links) coll() string { return l.origin + l.virtualDir + "/" + url.PathEscape(l.c.Name) }

func (l links) api(parts ...string) string {
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = url.PathEscape(p)
	}
	return l.coll() + "/" + strings.Join(escaped, "/")
}

// gitWeb matches the server: a literal space in the collection name, the
// project and repository escaped.
func (l links) gitWeb(r *repo) string {
	return l.origin + l.virtualDir + "/" + l.c.Name + "/" + url.PathEscape(r.Project.Name) + "/_git/" + url.PathEscape(r.Name)
}

func (l links) gitSSH(r *repo) string {
	return "ssh://" + l.ssh + l.virtualDir + "/" + url.PathEscape(l.c.Name) + "/" + url.PathEscape(r.Project.Name) + "/_git/" + url.PathEscape(r.Name)
}

func (l links) identityRef(p *identity) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"displayName": p.Display,
		"url":         l.api("_apis", "Identities", p.ID),
		"_links":      map[string]any{"avatar": map[string]any{"href": l.api("_apis", "GraphProfile", "MemberAvatars", p.subjectDescriptor())}},
		"id":          p.ID,
		"uniqueName":  p.unique(),
		"imageUrl":    l.api("_apis", "GraphProfile", "MemberAvatars", p.subjectDescriptor()),
		"descriptor":  p.subjectDescriptor(),
	}
}

func prop(v string) map[string]any { return map[string]any{"$type": "System.String", "$value": v} }

func (l links) identity(p *identity, full bool) map[string]any {
	out := map[string]any{
		"id":                  p.ID,
		"descriptor":          "System.Security.Principal.WindowsIdentity;" + p.windowsSID(),
		"subjectDescriptor":   p.subjectDescriptor(),
		"providerDisplayName": p.Display,
		"isActive":            true,
		"properties":          map[string]any{"Account": prop(p.Account)},
		"resourceVersion":     2,
		"metaTypeId":          255,
	}
	if full {
		out["members"], out["memberOf"], out["memberIds"] = []any{}, []any{}, []any{}
		out["masterId"] = "ffffffff-ffff-ffff-ffff-ffffffffffff"
		out["properties"] = map[string]any{
			"SchemaClassName": prop("User"), "Description": prop(""), "Domain": prop("MOCK"),
			"Account": prop(p.Account), "DN": prop("WinNT://MOCK/" + p.Account), "Mail": prop(p.Account + "@mock.invalid"),
			"SpecialType": prop("Generic"), "DirectoryAlias": prop(p.Account),
		}
	}
	return out
}

func (l links) project(p *project) map[string]any {
	return map[string]any{
		"id": p.ID, "name": p.Name, "description": p.Description,
		"url":   l.api("_apis", "projects", p.ID),
		"state": "wellFormed", "revision": 10, "visibility": "private",
		"lastUpdateTime": seedTime.Format(time.RFC3339Nano),
	}
}

func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
