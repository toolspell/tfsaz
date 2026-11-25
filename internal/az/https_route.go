package az

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	httpsActionRedirect = "redirect"
	httpsActionGit      = "git"
)

var errNoHTTPSRoute = errors.New("Azure DevOps URL does not match a configured HTTPS alias")

type httpsRoute struct {
	ProfileID   string
	Destination string
	Action      string
}

type httpsRouteBinding struct {
	base                *url.URL
	profile             *Profile
	project, repository string
}

// httpsRouteBindings is shared by the mapper and local routing exporters.
// Stock T3 uses the modern Azure URL when constructing a link from a legacy
// repository key, so the modern spelling inherits the same binding.
func httpsRouteBindings(cfg config, records []setupRecord) []httpsRouteBinding {
	var bindings []httpsRouteBinding
	add := func(raw string, p *Profile, project, repository string) {
		base, ok := baseURL(raw)
		if !ok || !azureOrganization(base) {
			return
		}
		if _, ok := httpsRoutePath(base); !ok {
			return
		}
		bindings = append(bindings, httpsRouteBinding{base, p, project, repository})
		if legacyHost.MatchString(base.Host) {
			modern := &url.URL{Scheme: "https", Host: "dev.azure.com", Path: "/" + strings.TrimSuffix(base.Host, ".visualstudio.com")}
			bindings = append(bindings, httpsRouteBinding{modern, p, project, repository})
		}
	}
	for i := range cfg.Profiles {
		p := &cfg.Profiles[i]
		add(p.Organization, p, "", "")
		for _, alias := range p.Aliases {
			add(alias, p, "", "")
		}
		for _, record := range records {
			if record.Phase == setupPhaseActive && record.ProfileID == p.ID && record.CollectionURL == p.CollectionURL &&
				safeHTTPSRouteSegment(record.Project) && safeHTTPSRouteSegment(record.Repository) &&
				record.Host == setupHost(record.ProfileID, record.CollectionURL, record.Project, record.Repository) {
				add("https://"+record.Host, p, record.Project, record.Repository)
			}
		}
	}
	return bindings
}

func safeHTTPSRouteSegment(s string) bool {
	for {
		if !utf8.ValidString(s) || s == "" || s == "." || s == ".." || s != strings.TrimSpace(s) ||
			strings.HasSuffix(s, ".") || strings.ContainsAny(s, "/\\;?#") || strings.ContainsFunc(s, unicode.IsControl) {
			return false
		}
		decoded, err := url.PathUnescape(s)
		if err != nil || decoded == s {
			return true
		}
		s = decoded
	}
}

func httpsRoutePath(u *url.URL) ([]string, bool) {
	parts, ok := pathSegments(u)
	if !ok {
		return nil, false
	}
	for _, part := range parts {
		if !safeHTTPSRouteSegment(part) {
			return nil, false
		}
	}
	return parts, true
}

func mapHTTPSRoute(cfg config, records []setupRecord, raw string) (httpsRoute, error) {
	return mapHTTPSRouteMode(cfg, records, raw, false)
}

func mapHTTPSGitBase(cfg config, records []setupRecord, raw string) (httpsRoute, error) {
	return mapHTTPSRouteMode(cfg, records, raw, true)
}

func mapHTTPSRouteMode(cfg config, records []setupRecord, raw string, gitBase bool) (httpsRoute, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Opaque != "" {
		return httpsRoute{}, errors.New("use an HTTPS Azure DevOps URL without embedded credentials")
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	organization, _ := url.PathUnescape(first)
	var alias httpsRouteBinding
	bindings := httpsRouteBindings(cfg, records)
	for _, binding := range bindings {
		if !strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), binding.base.Host) ||
			(binding.base.Path != "" && !strings.EqualFold("/"+organization, binding.base.Path)) {
			continue
		}
		if alias.profile != nil && (alias.profile != binding.profile || !strings.EqualFold(alias.project, binding.project) ||
			!strings.EqualFold(alias.repository, binding.repository)) {
			return httpsRoute{}, errors.New("Azure DevOps URL matches ambiguous HTTPS aliases")
		}
		alias = binding
	}
	if alias.profile == nil {
		return httpsRoute{}, errNoHTTPSRoute
	}
	if !strings.EqualFold(u.Host, alias.base.Host) || u.User != nil || u.ForceQuery || strings.ContainsFunc(raw, unicode.IsControl) ||
		(gitBase && strings.ContainsAny(raw, "?#")) {
		return httpsRoute{}, errors.New("use the exact HTTPS alias without embedded credentials")
	}
	parts, ok := httpsRoutePath(u)
	if !ok {
		return httpsRoute{}, errors.New("invalid Azure DevOps URL path")
	}
	baseParts, _ := httpsRoutePath(alias.base)
	parts = parts[len(baseParts):]
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return httpsRoute{}, errors.New("invalid Azure DevOps URL query")
	}
	project, repository, action := classifyHTTPSRoute(parts, query, u.Fragment, gitBase)
	if legacyHost.MatchString(alias.base.Host) && len(parts) > 0 && strings.EqualFold(parts[0], "DefaultCollection") {
		p, r, a := classifyHTTPSRoute(parts[1:], query, u.Fragment, gitBase)
		if a != "" {
			parts = parts[1:]
			project, repository, action = p, r, a
		}
	}
	if action == "" || (alias.project != "" && !strings.EqualFold(alias.project, project)) ||
		(alias.repository != "" && repository != "" && !strings.EqualFold(alias.repository, repository)) {
		return httpsRoute{}, errors.New("URL does not identify a supported Azure DevOps page or Git endpoint for this HTTPS alias")
	}
	destination, ok := baseURL(alias.profile.CollectionURL)
	if !ok {
		return httpsRoute{}, errors.New("invalid configured HTTPS collection URL")
	}
	destinationParts, ok := httpsRoutePath(destination)
	if !ok {
		return httpsRoute{}, errors.New("invalid configured HTTPS collection path")
	}
	if cfg.checkRestBoundary(alias.profile, parts) != nil {
		return httpsRoute{}, errors.New("Azure DevOps URL destination overlaps configured collection paths")
	}
	targetParts := append(destinationParts, parts...)
	for _, binding := range bindings {
		if sameRESTServer(destination, binding.base) {
			bindingParts, _ := pathSegments(binding.base)
			if restPathHasPrefix(targetParts, bindingParts) {
				return httpsRoute{}, errors.New("configured collection points to an Azure DevOps HTTPS alias")
			}
		}
	}
	destination.Path += "/" + strings.Join(parts, "/")
	if strings.HasSuffix(u.Path, "/") {
		destination.Path += "/"
	}
	destination.RawQuery, destination.Fragment, destination.RawFragment = u.RawQuery, u.Fragment, u.RawFragment
	return httpsRoute{ProfileID: alias.profile.ID, Destination: destination.String(), Action: action}, nil
}

func classifyHTTPSRoute(parts []string, query url.Values, fragment string, gitBase bool) (project, repository, action string) {
	for _, part := range parts {
		if collectionAPIRoot(part) {
			return "", "", ""
		}
	}
	if len(parts) >= 2 && strings.EqualFold(parts[0], "_git") {
		return parts[1], parts[1], classifyHTTPSRepoRoute(parts[1], parts[2:], query, fragment, gitBase)
	}
	if len(parts) >= 3 && strings.EqualFold(parts[1], "_git") {
		return parts[0], parts[2], classifyHTTPSRepoRoute(parts[2], parts[3:], query, fragment, gitBase)
	}
	if gitBase || !safeHTTPSRouteText(fragment) {
		return "", "", ""
	}
	if len(parts) == 4 && strings.EqualFold(parts[1], "_workitems") && strings.EqualFold(parts[2], "edit") &&
		positiveHTTPSRouteID(parts[3]) && len(query) == 0 {
		return parts[0], "", httpsActionRedirect
	}
	if len(parts) == 3 && strings.EqualFold(parts[1], "_build") && strings.EqualFold(parts[2], "results") &&
		validHTTPSRouteQuery(query, "buildid", "view") && positiveHTTPSRouteID(queryValueFold(query, "buildid")) {
		return parts[0], "", httpsActionRedirect
	}
	return "", "", ""
}

func classifyHTTPSRepoRoute(repository string, tail []string, query url.Values, fragment string, gitBase bool) string {
	if gitBase {
		if len(tail) == 0 {
			return httpsActionGit
		}
		return ""
	}
	if fragment == "" {
		if len(tail) == 1 && (tail[0] == "git-upload-pack" || tail[0] == "git-receive-pack") && len(query) == 0 {
			return httpsActionGit
		}
		if len(tail) == 2 && tail[0] == "info" && tail[1] == "refs" && len(query) == 1 && len(query["service"]) == 1 &&
			(query.Get("service") == "git-upload-pack" || query.Get("service") == "git-receive-pack") {
			return httpsActionGit
		}
		if len(tail) == 0 && len(query) == 0 && strings.HasSuffix(strings.ToLower(repository), ".git") {
			return httpsActionGit
		}
	}
	if !safeHTTPSRouteText(fragment) {
		return ""
	}
	switch {
	case len(tail) == 0 && validHTTPSRouteQuery(query, "view", "path", "version", "_a"):
		return httpsActionRedirect
	case len(tail) == 2 && strings.EqualFold(tail[0], "pullrequest") && positiveHTTPSRouteID(tail[1]) &&
		validHTTPSRouteQuery(query, "view", "iteration", "baseiteration", "_a", "path"):
		return httpsActionRedirect
	case len(tail) == 2 && strings.EqualFold(tail[0], "commit") && len(tail[1]) <= 64 &&
		strings.Trim(tail[1], "0123456789abcdefABCDEF") == "" && validHTTPSRouteQuery(query, "path", "_a"):
		return httpsActionRedirect
	default:
		return ""
	}
}

func positiveHTTPSRouteID(s string) bool {
	if strings.Trim(s, "0123456789") != "" {
		return false
	}
	id, err := strconv.ParseUint(s, 10, 53)
	return err == nil && id > 0
}

func queryValueFold(query url.Values, key string) string {
	for k, vals := range query {
		if strings.EqualFold(k, key) && len(vals) == 1 {
			return vals[0]
		}
	}
	return ""
}

func validHTTPSRouteQuery(query url.Values, allowed ...string) bool {
	seen := make(map[string]bool, len(query))
	for key, values := range query {
		key = strings.ToLower(key)
		if seen[key] || len(values) != 1 {
			return false
		}
		seen[key] = true
		valid := false
		for _, name := range allowed {
			if name == key {
				valid = safeHTTPSRouteText(values[0])
				break
			}
		}
		if !valid {
			return false
		}
	}
	return true
}

func safeHTTPSRouteText(s string) bool {
	for {
		if !utf8.ValidString(s) || strings.ContainsAny(s, ":\\;#?&=") || strings.Contains(s, "//") ||
			strings.ContainsFunc(s, unicode.IsControl) {
			return false
		}
		decoded, err := url.PathUnescape(s)
		if err != nil || decoded == s {
			return true
		}
		s = decoded
	}
}
