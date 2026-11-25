package az

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

type Scope struct {
	Organization string
	Project      string
	Repository   string
	WebURL       string
}

func (cfg config) selectBase(raw string) (*Profile, Scope) {
	u, ok := baseURL(raw)
	if !ok {
		return nil, Scope{}
	}
	var selected *Profile
	requested := u.String()
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		for _, candidate := range p.selectors() {
			if !strings.EqualFold(candidate, requested) &&
				!(u.Host == "dev.azure.com" && strings.EqualFold(canonicalAzureOrganization(candidate), requested)) {
				continue
			}
			if selected != nil && selected != p {
				return nil, Scope{}
			}
			selected = p
			break
		}
	}
	if selected == nil {
		return nil, Scope{}
	}
	scope := Scope{Organization: selected.Organization}
	if azureOrganization(u) {
		scope.Organization = requested
	}
	return selected, scope
}

// T3 canonicalizes legacy repository keys to dev.azure.com. Keep that
// equivalent in CLI organization selection without adding a raw REST alias.
func canonicalAzureOrganization(legacy string) string {
	host, ok := strings.CutPrefix(legacy, "https://")
	if !ok || !legacyHost.MatchString(host) {
		return ""
	}
	return "https://dev.azure.com/" + strings.TrimSuffix(host, ".visualstudio.com")
}

func remoteURL(raw string) (*url.URL, bool) {
	if !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(raw, ":")
		if !ok || strings.Contains(host, "/") {
			return nil, false
		}
		raw = "ssh://" + host + "/" + path
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return nil, false
	}
	u.User = nil
	u.Host = strings.ToLower(u.Host)
	return u, true
}

// azureRemote extracts the repository spelling T3 uses for web links. Keep
// this separate from profile matching so an unconfigured Azure origin cannot
// accidentally select a different collection.
func azureRemote(u *url.URL, parts []string) (Scope, bool) {
	var s Scope
	if u.Scheme == "ssh" {
		if len(parts) != 4 || parts[0] != "v3" {
			return Scope{}, false
		}
		switch u.Hostname() {
		case "ssh.dev.azure.com":
			s.Organization = "https://dev.azure.com/" + url.PathEscape(parts[1])
		case "vs-ssh.visualstudio.com":
			host := strings.ToLower(parts[1]) + ".visualstudio.com"
			if !legacyHost.MatchString(host) {
				return Scope{}, false
			}
			s.Organization = "https://" + host
		default:
			return Scope{}, false
		}
		s.Project, s.Repository = parts[2], parts[3]
		s.WebURL = s.Organization + "/" + url.PathEscape(s.Project) + "/_git/" + url.PathEscape(s.Repository)
		return s, true
	}
	if u.Scheme != "https" || len(parts) < 2 || parts[len(parts)-2] != "_git" {
		return Scope{}, false
	}
	switch {
	case u.Host == "dev.azure.com" && len(parts) >= 3:
		s.Organization = "https://dev.azure.com/" + url.PathEscape(parts[0])
		parts = parts[1:]
	case legacyHost.MatchString(u.Host):
		s.Organization = "https://" + u.Host
		if strings.EqualFold(parts[0], "DefaultCollection") {
			parts = parts[1:]
		}
	default:
		return Scope{}, false
	}
	if len(parts) != 2 && len(parts) != 3 {
		return Scope{}, false
	}
	s.Repository = parts[len(parts)-1]
	s.Project = s.Repository
	if len(parts) == 3 {
		s.Project = parts[0]
	}
	s.WebURL = strings.TrimSuffix(u.String(), "/")
	return s, true
}

func (cfg config) remoteScope(raw string) (*Profile, Scope) {
	u, ok := remoteURL(raw)
	if !ok {
		return nil, Scope{}
	}
	parts, ok := pathSegments(u)
	if !ok {
		return nil, Scope{}
	}
	if s, ok := azureRemote(u, parts); ok {
		p, _ := cfg.selectBase(s.Organization)
		return p, s
	}
	// A real server remote identifies a collection only at its configured
	// authority and full path. The resulting web link uses its organization alias.
	if u.Scheme != "https" || len(parts) < 3 || parts[len(parts)-2] != "_git" {
		return nil, Scope{}
	}
	repo := parts[len(parts)-1]
	prefix := parts[:len(parts)-2]
	base := url.URL{Scheme: "https", Host: u.Host, Path: "/" + strings.Join(prefix, "/")}
	p, s := cfg.selectBase(base.String())
	project := repo // A default repository may omit its project segment.
	if len(prefix) > 1 {
		base.Path = "/" + strings.Join(prefix[:len(prefix)-1], "/")
		parent, parentScope := cfg.selectBase(base.String())
		if parent != nil {
			// The same spelling can denote a nested collection or a project
			// inside its parent. Require explicit scope in this case.
			if p != nil {
				return nil, Scope{}
			}
			p, s, project = parent, parentScope, prefix[len(prefix)-1]
		}
	}
	if p == nil {
		return nil, Scope{}
	}
	s.Project, s.Repository = project, repo
	s.WebURL = s.Organization + "/" + url.PathEscape(project) + "/_git/" + url.PathEscape(repo)
	return p, s
}

func (cfg config) checkout(ctx context.Context, cwd string) (*Profile, Scope) {
	cmd := exec.CommandContext(ctx, "git", "remote", "-v")
	cmd.Dir = cwd
	// Match T3's cwd even if the parent was launched from a git hook.
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		switch strings.ToUpper(key) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES":
			continue
		}
		cmd.Env = append(cmd.Env, e)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, Scope{}
	}
	var firstProfile *Profile
	var firstScope Scope
	for _, line := range strings.Split(string(out), "\n") {
		name, remote, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "\t")
		if !ok || !strings.HasSuffix(remote, " (fetch)") {
			continue
		}
		if name != "origin" && firstScope.Organization != "" {
			continue
		}
		p, s := cfg.remoteScope(strings.TrimSuffix(remote, " (fetch)"))
		if name == "origin" {
			return p, s
		}
		if s.Organization != "" {
			firstProfile, firstScope = p, s
		}
	}
	return firstProfile, firstScope
}

func (cfg config) scope(ctx context.Context, cwd string, opts map[string]string) (*Profile, Scope, error) {
	explicit := opts["organization"]
	if explicit != "" {
		p, s := cfg.selectBase(explicit)
		if p == nil {
			return nil, Scope{}, errors.New("organization does not match a configured collection")
		}
		// PR-ID commands only carry an organization. Retain the checkout's web
		// spelling when it belongs to this organization (e.g. DefaultCollection).
		cp, checkout := cfg.checkout(ctx, cwd)
		if cp == p && strings.EqualFold(s.Organization, checkout.Organization) {
			s = checkout
		}
		return p, s, nil
	}
	if opts["detect"] == "false" {
		return nil, Scope{}, errors.New("an explicit organization is required when detection is disabled")
	}
	p, s := cfg.checkout(ctx, cwd)
	if p == nil {
		return nil, Scope{}, errors.New("checkout does not identify a configured collection; specify --organization")
	}
	return p, s, nil
}
