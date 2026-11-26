package az

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type browserExtensionManifest struct {
	Version         int      `json:"manifest_version"`
	Name            string   `json:"name"`
	Release         string   `json:"version"`
	Permissions     []string `json:"permissions"`
	HostPermissions []string `json:"host_permissions"`
	NetworkRules    struct {
		Resources []browserRuleResource `json:"rule_resources"`
	} `json:"declarative_net_request"`
}

type browserRuleResource struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

type browserRule struct {
	ID        int                  `json:"id"`
	Priority  int                  `json:"priority"`
	Action    browserRuleAction    `json:"action"`
	Condition browserRuleCondition `json:"condition"`
}

type browserRuleAction struct {
	Type     string           `json:"type"`
	Redirect *browserRedirect `json:"redirect,omitempty"`
}

type browserRedirect struct {
	Substitution string `json:"regexSubstitution"`
}

type browserRuleCondition struct {
	Regex         string   `json:"regexFilter"`
	CaseSensitive bool     `json:"isUrlFilterCaseSensitive"`
	ResourceTypes []string `json:"resourceTypes,omitempty"`
	Methods       []string `json:"requestMethods,omitempty"`
}

const browserRulesFile = "rules.json"

// These expressions match Chromium's escaped URL. A higher-priority block
// rejects separators, control characters and dot segments hidden in escapes.
const (
	browserSegment  = `[^/?#]+`
	browserID       = `0*[1-9][0-9]*`
	browserTail     = `(?:\?[^#]*)?(#.*)?$`
	browserFragment = `(#.*)?$`
	browserView     = `[a-z0-9_-]+`
	browserValue    = `[^&#=:;?\\]+`
)

const browserExtensionName = "TFSaz Azure DevOps Server Links"

// Exports from before the product name was capitalized carry this name.
// Verify still accepts them, so an existing export does not report drift.
const legacyBrowserExtensionName = "tfsaz Azure DevOps Server Links" // naming-allow: the name older exports carry

func makeBrowserRules(cfg config, records []setupRecord) (browserExtensionManifest, []browserRule, error) {
	manifest := browserExtensionManifest{
		Version:     3,
		Name:        browserExtensionName,
		Release:     "1.0.0",
		Permissions: []string{"declarativeNetRequest"},
	}
	manifest.NetworkRules.Resources = []browserRuleResource{{ID: "routes", Path: browserRulesFile, Enabled: true}}
	var rules []browserRule
	add := func(priority int, pattern, substitution string, resourceTypes []string) {
		rule := browserRule{
			ID:        len(rules) + 1,
			Priority:  priority,
			Action:    browserRuleAction{Type: "block"},
			Condition: browserRuleCondition{Regex: pattern, ResourceTypes: resourceTypes},
		}
		if substitution != "" {
			rule.Action = browserRuleAction{Type: "redirect", Redirect: &browserRedirect{Substitution: substitution}}
			rule.Condition.ResourceTypes = []string{"main_frame"}
			rule.Condition.Methods = []string{"get"}
		}
		rules = append(rules, rule)
	}

	// Blocking also covers alias spellings that we never redirect, including
	// alternate ports and unencrypted schemes. Chrome omits main_frame when
	// resourceTypes is absent, so a separate block covers navigations.
	blocked := map[string]*url.URL{}
	block := func(base *url.URL) {
		key := strings.ToLower(base.String())
		if blocked[key] != nil {
			return
		}
		blocked[key] = base
		pattern := browserAliasBlock(base)
		add(1, pattern, "", []string{"main_frame"})
		add(1, pattern, "", nil)
	}

	bindings := httpsRouteBindings(cfg, records)
	seen := map[string]httpsRouteBinding{}
	for _, binding := range bindings {
		key := strings.ToLower(binding.base.String())
		if previous, ok := seen[key]; ok && (previous.profile != binding.profile ||
			!strings.EqualFold(previous.project, binding.project) || !strings.EqualFold(previous.repository, binding.repository)) {
			return manifest, nil, errors.New("ambiguous Azure DevOps browser aliases")
		}
		seen[key] = binding
		block(binding.base)
		hostPermission := "https://" + binding.base.Hostname() + "/*"
		if !slices.Contains(manifest.HostPermissions, hostPermission) {
			manifest.HostPermissions = append(manifest.HostPermissions, hostPermission)
		}
	}
	// A registered alias with a pending transaction or a stale profile still
	// belongs to this installation, even when it has no destination.
	for _, r := range records {
		if !validSegment(r.Project) || !validSegment(r.Repository) ||
			r.Host != setupHost(r.ProfileID, r.CollectionURL, r.Project, r.Repository) {
			continue
		}
		block(&url.URL{Scheme: "https", Host: r.Host})
		org := strings.TrimSuffix(r.Host, ".visualstudio.com")
		block(&url.URL{Scheme: "https", Host: "dev.azure.com", Path: "/" + org})
	}
	if len(bindings) == 0 {
		return manifest, nil, errors.New("no Azure DevOps HTTPS aliases are configured for browser routing")
	}
	for _, p := range cfg.Profiles {
		collection, ok := baseURL(p.CollectionURL)
		if !ok {
			return manifest, nil, errors.New("invalid browser route collection URL")
		}
		collectionPath, ok := httpsRoutePath(collection)
		if !ok {
			return manifest, nil, errors.New("invalid browser route collection path")
		}
		// Static wildcard rules cannot safely resolve destinations inside a
		// second configured collection, including its equivalent spellings.
		for i := range cfg.Profiles {
			other := &cfg.Profiles[i]
			if other.ID == p.ID {
				continue
			}
			for _, selector := range other.selectors() {
				base, ok := baseURL(selector)
				if !ok || !sameRESTServer(collection, base) {
					continue
				}
				candidate, _ := pathSegments(base)
				if len(candidate) >= len(collectionPath) &&
					slices.EqualFunc(candidate[:len(collectionPath)], collectionPath, sameRESTBoundarySegment) {
					return manifest, nil, fmt.Errorf("collection for profile %s overlaps another profile's path", p.ID)
				}
			}
		}
		for _, alias := range blocked {
			aliasPath, _ := pathSegments(alias)
			if strings.EqualFold(strings.TrimSuffix(collection.Hostname(), "."), alias.Hostname()) &&
				restPathHasPrefix(collectionPath, aliasPath) {
				return manifest, nil, fmt.Errorf("collection for profile %s overlaps an Azure DevOps browser alias", p.ID)
			}
		}
	}

	for _, binding := range bindings {
		base := binding.base
		key := strings.ToLower(base.String())
		if _, ok := seen[key]; !ok {
			continue
		}
		delete(seen, key)
		prefix := "^" + regexp.QuoteMeta(base.String())
		dest, _ := baseURL(binding.profile.CollectionURL)
		direct := add
		if legacyHost.MatchString(base.Host) {
			addBrowserBindingRules(add, binding, prefix, dest.String())
			// Stock T3 treats DefaultCollection as an optional prefix on legacy hosts.
			direct = func(priority int, pattern, substitution string, resourceTypes []string) {
				if substitution != "" {
					priority += 3
				}
				add(priority, pattern, substitution, resourceTypes)
			}
		}
		if legacyHost.MatchString(base.Host) {
			prefix += "/DefaultCollection"
		}
		addBrowserBindingRules(direct, binding, prefix, dest.String())
	}
	if len(rules) > 1000 {
		return manifest, nil, errors.New("too many Azure DevOps browser routes for a Chromium extension")
	}
	slices.Sort(manifest.HostPermissions)
	return manifest, rules, nil
}

func browserAliasBlock(base *url.URL) string {
	return `^(?:https?|wss?)://(?:[^/?#@]*@)?` + regexp.QuoteMeta(base.Hostname()) +
		`\.?(?::[0-9]+)?` + regexp.QuoteMeta(base.EscapedPath()) + `(?:[/?#]|$)`
}

func addBrowserBindingRules(add func(int, string, string, []string), binding httpsRouteBinding, prefix, target string) {
	add(8, prefix+`/[^?#]*(?:\\|;|%(?:[01][0-9a-f]|2[35f]|3[0-9bf]|[46][1-9a-f]|5[0-9acf]|7[0-9af]))`, "", []string{"main_frame"})
	add(8, prefix+`/(?:[^?#]*/)?(?:[^?#/]*(?:\.|%2e)|_apis|_odata|_packaging)(?:[/#?]|$)`, "", []string{"main_frame"})
	add(8, prefix+`/(?:[^/?#]+/)?_git/[^/?#]+(?:\.|%2e)git/?(?:[?#]|$)`, "", []string{"main_frame"})

	project, repo := "("+browserSegment+")", "("+browserSegment+")"
	webProject, webRepo, bareRepo := `\1`, `\2`, `\1`
	projectGroups, repoGroups, bareGroups := 1, 2, 1
	if binding.project != "" {
		webProject = url.PathEscape(binding.project)
		webRepo = url.PathEscape(binding.repository)
		bareRepo = webRepo
		project = regexp.QuoteMeta(webProject)
		repo = regexp.QuoteMeta(webRepo)
		projectGroups, repoGroups, bareGroups = 0, 0, 0
	}

	projectPath := prefix + "/" + project
	projectTarget := target + "/" + webProject
	addBrowserRepoRules(add, projectPath+`/_git/`+repo, projectTarget+`/_git/`+webRepo, repoGroups)
	if binding.project == "" || strings.EqualFold(binding.project, binding.repository) {
		addBrowserRepoRules(add, prefix+`/_git/`+repo, target+`/_git/`+bareRepo, bareGroups)
	}
	captures := fmt.Sprintf(`\%d\%d`, projectGroups+1, projectGroups+2)
	add(2, projectPath+`/_workitems/edit/(`+browserID+`)/?`+browserTail,
		projectTarget+`/_workitems/edit/`+captures, nil)
	build := projectPath + `/_build/results/?\?`
	buildDest := projectTarget + `/_build/results?`
	add(3, build+`(buildId=`+browserID+`(?:&view=`+browserView+`)?)`+browserFragment, buildDest+captures, nil)
	add(3, build+`(view=`+browserView+`&buildId=`+browserID+`)`+browserFragment, buildDest+captures, nil)
	// Preserve the numeric key even when a caller adds unknown parameters.
	add(2, build+`(?:[^#]*&)?buildId=(`+browserID+`)(?:&[^#]*)?`+browserFragment, buildDest+"buildId="+captures, nil)
}

func addBrowserRepoRules(add func(int, string, string, []string), pattern, target string, groups int) {
	browserRepoPage(add, pattern, target, `view|_a|path|version`, groups)
	id := fmt.Sprintf(`\%d`, groups+1)
	browserRepoPage(add, pattern+`/pullrequest/(`+browserID+`)`, target+`/pullrequest/`+id,
		`view|_a|(?:base)?iteration|path`, groups+1)
	browserRepoPage(add, pattern+`/commit/([0-9a-f]+)`, target+`/commit/`+id,
		`_a|path`, groups+1)
}

func browserRepoPage(add func(int, string, string, []string), pattern, target, keys string, groups int) {
	// Only a query consisting entirely of safe keys and values is preserved.
	selector := `(?:` + keys + `)=` + browserValue
	fragment := fmt.Sprintf(`\%d`, groups+1)
	queryAndFragment := fmt.Sprintf(`\%d\%d`, groups+1, groups+2)
	add(4, pattern+`/?\?[^#]*(?:\?|%(?:[01][0-9a-f]|2[356]|3[a-df]|5c|7f)|(?:/|%2f){2})[^#]*`+browserFragment,
		target+fragment, nil)
	add(4, pattern+`/?\?[^#]*=[^&#]*=[^#]*`+browserFragment, target+fragment, nil)
	add(3, pattern+`/?(\?(?:`+selector+`&?)+)`+browserFragment, target+queryAndFragment, nil)
	add(2, pattern+`/?`+browserTail, target+fragment, nil)
}

func writeHTTPSBrowserExtension(dir string, cfg config, records []setupRecord) error {
	if dir == "" {
		return errors.New("browser extension output directory is required")
	}
	manifest, rules, err := makeBrowserRules(cfg, records)
	if err != nil {
		return err
	}
	dir = filepath.Clean(dir)
	// Reserve a new directory without replacing an existing path. Publish the
	// manifest last so the browser can never load a partially written ruleset.
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create browser extension directory: %w", err)
	}
	pending := filepath.Join(dir, ".manifest.json")
	rulesFile := filepath.Join(dir, browserRulesFile)
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(pending)
			_ = os.Remove(rulesFile)
			_ = os.Remove(dir)
		}
	}()
	for _, output := range []struct {
		path  string
		value any
	}{{rulesFile, rules}, {pending, manifest}} {
		if err := writeBrowserJSON(output.path, output.value); err != nil {
			return fmt.Errorf("write browser extension: %w", err)
		}
	}
	if err := os.Rename(pending, filepath.Join(dir, "manifest.json")); err != nil {
		return fmt.Errorf("publish browser extension manifest: %w", err)
	}
	complete = true
	return nil
}

func writeBrowserJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func verifyHTTPSBrowserExtension(dir string, cfg config, records []setupRecord) error {
	manifest, rules, err := makeBrowserRules(cfg, records)
	if err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		data any
	}{{"manifest.json", manifest}, {browserRulesFile, rules}} {
		want, err := json.MarshalIndent(f.data, "", "  ")
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			return err
		}
		if f.name == "manifest.json" {
			legacy := manifest
			legacy.Name = legacyBrowserExtensionName
			old, err := json.MarshalIndent(legacy, "", "  ")
			if err != nil {
				return err
			}
			if string(got) == string(append(old, '\n')) {
				continue
			}
		}
		if string(got) != string(append(want, '\n')) {
			return errors.New("browser export differs from the active routes; export to a new directory and reload the extension")
		}
	}
	return nil
}
