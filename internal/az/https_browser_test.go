package az

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func browserRuleFixture() (config, []setupRecord) {
	collection := "https://server.example.invalid:8443/tfs/Team%20Collection"
	cfg := config{Profiles: []Profile{
		{ID: "team", Organization: "https://team-proxy.visualstudio.com", CollectionURL: collection,
			Aliases: []string{"https://dev.azure.com/extra-team", "https://vpn.example.invalid/tfs/Team%20Collection"}},
		{ID: "other", Organization: "https://dev.azure.com/other", CollectionURL: "https://server.example.invalid:8443/Other"},
	}}
	active := setupRecord{
		ProfileID: "team", CollectionURL: collection, Project: "Web App", Repository: "My Repo", Phase: setupPhaseActive,
	}
	active.Host = setupHost(active.ProfileID, active.CollectionURL, active.Project, active.Repository)
	pending := setupRecord{
		ProfileID: "team", CollectionURL: collection, Project: "Unready", Repository: "Repo", Phase: setupPhaseRestoring,
	}
	pending.Host = setupHost(pending.ProfileID, pending.CollectionURL, pending.Project, pending.Repository)
	stale := setupRecord{
		ProfileID: "team", CollectionURL: "https://old.example.invalid/Collection", Project: "Old", Repository: "Repo", Phase: setupPhaseActive,
	}
	stale.Host = setupHost(stale.ProfileID, stale.CollectionURL, stale.Project, stale.Repository)
	return cfg, []setupRecord{active, pending, stale}
}

func TestWriteHTTPSBrowserExtension(t *testing.T) {
	cfg, records := browserRuleFixture()
	dir := filepath.Join(t.TempDir(), "browser")
	if err := writeHTTPSBrowserExtension(dir, cfg, records); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name() != "manifest.json" || files[1].Name() != browserRulesFile {
		t.Fatalf("unexpected browser extension contents: %v", files)
	}
	var manifest browserExtensionManifest
	readBrowserFixtureJSON(t, filepath.Join(dir, "manifest.json"), &manifest)
	wantPermissions := []string{
		"https://dev.azure.com/*",
		"https://team-proxy.visualstudio.com/*",
		"https://" + records[0].Host + "/*",
	}
	slices.Sort(wantPermissions)
	if manifest.Version != 3 || !slices.Equal(manifest.Permissions, []string{"declarativeNetRequest"}) ||
		!slices.Equal(manifest.HostPermissions, wantPermissions) ||
		!slices.Equal(manifest.NetworkRules.Resources, []browserRuleResource{{ID: "routes", Path: browserRulesFile, Enabled: true}}) {
		t.Fatalf("unexpected browser extension manifest: %+v", manifest)
	}
	var rules []browserRule
	readBrowserFixtureJSON(t, filepath.Join(dir, browserRulesFile), &rules)
	for i, rule := range rules {
		if rule.ID != i+1 {
			t.Fatalf("unexpected rule ID: %+v", rule)
		}
		if _, err := regexp.Compile("(?i)" + rule.Condition.Regex); err != nil {
			t.Fatalf("rule %d: %v", rule.ID, err)
		}
	}
	if err := writeHTTPSBrowserExtension(dir, cfg, records); err == nil {
		t.Fatal("export accepted an existing destination")
	}
	if err := verifyHTTPSBrowserExtension(dir, cfg, records); err != nil {
		t.Fatalf("verify current export: %v", err)
	}
	if err := verifyHTTPSBrowserExtension(dir, cfg, nil); err == nil {
		t.Fatal("verification accepted an export with retired repository routes")
	}
	var after []browserRule
	readBrowserFixtureJSON(t, filepath.Join(dir, browserRulesFile), &after)
	if len(after) != len(rules) {
		t.Fatal("rejected export changed the existing rules")
	}
}

func readBrowserFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPSBrowserRoutes(t *testing.T) {
	cfg, records := browserRuleFixture()
	_, rules, err := makeBrowserRules(cfg, records)
	if err != nil {
		t.Fatal(err)
	}
	team := cfg.Profiles[0].CollectionURL
	managed := "https://" + records[0].Host
	modern := "https://dev.azure.com/" + strings.TrimSuffix(records[0].Host, ".visualstudio.com")
	tests := []struct {
		name, raw, method, resource, want string
	}{
		{"legacy PR", "https://team-proxy.visualstudio.com/DefaultCollection/Web%20App/_git/My%20Repo/pullrequest/42?view=files#thread", "", "", team + "/Web%20App/_git/My%20Repo/pullrequest/42?view=files#thread"},
		{"legacy default repository", "https://team-proxy.visualstudio.com/DefaultCollection/_git/Repo", "", "", team + "/_git/Repo"},
		{"modernized legacy PR", "https://dev.azure.com/team-proxy/Web%20App/_git/My%20Repo/pullrequest/42", "", "", team + "/Web%20App/_git/My%20Repo/pullrequest/42"},
		{"managed PR", managed + "/Web%20App/_git/My%20Repo/pullrequest/9", "", "", team + "/Web%20App/_git/My%20Repo/pullrequest/9"},
		{"canonical repository with selectors", modern + "/Web%20App/_git/My%20Repo?path=%2Ffolder%2Ffile.txt&version=GBmain#L10", "", "", team + "/Web%20App/_git/My%20Repo?path=%2Ffolder%2Ffile.txt&version=GBmain#L10"},
		{"short repository", "https://dev.azure.com/extra-team/_git/Repo", "", "", team + "/_git/Repo"},
		{"short repository PR", "https://dev.azure.com/extra-team/_git/Repo/pullrequest/42?iteration=2&baseIteration=1#L2", "", "", team + "/_git/Repo/pullrequest/42?iteration=2&baseIteration=1#L2"},
		{"work item", modern + "/Web%20App/_workitems/edit/7?token=private#focus", "", "", team + "/Web%20App/_workitems/edit/7#focus"},
		{"build", modern + "/Web%20App/_build/results?view=logs&buildId=19#step", "", "", team + "/Web%20App/_build/results?view=logs&buildId=19#step"},
		{"build extra parameters", modern + "/Web%20App/_build/results?token=private&buildId=19&continue=https://outside.invalid", "", "", team + "/Web%20App/_build/results?buildId=19"},
		{"build invalid ID", modern + "/Web%20App/_build/results?buildId=invalid", "", "", "block"},
		{"different profile", "https://dev.azure.com/other/Project/_git/Repo/commit/aBc123?_a=summary", "", "", cfg.Profiles[1].CollectionURL + "/Project/_git/Repo/commit/aBc123?_a=summary"},
		{"unknown query dropped", modern + "/Web%20App/_git/My%20Repo/pullrequest/9?view=files&redirect=https://outside.invalid#L5", "", "", team + "/Web%20App/_git/My%20Repo/pullrequest/9#L5"},
		{"encoded query injection dropped", modern + "/Web%20App/_git/My%20Repo?path=%2F%2Foutside.invalid%26token%3Dprivate", "", "", team + "/Web%20App/_git/My%20Repo"},
		{"path query network URL dropped", modern + "/Web%20App/_git/My%20Repo?path=%2F/outside.invalid", "", "", team + "/Web%20App/_git/My%20Repo"},
		{"missing selector delimiter", modern + "/Web%20App/_git/My%20Repo?view=filespath=hidden", "", "", team + "/Web%20App/_git/My%20Repo"},
		{"other managed repository", modern + "/Web%20App/_git/Other/pullrequest/42", "", "", "block"},
		{"other managed project", managed + "/Other/_workitems/edit/7", "", "", "block"},
		{"pending managed host", "https://" + records[1].Host + "/Unready/_git/Repo", "", "", "block"},
		{"stale managed host", "https://" + records[2].Host + "/Old/_git/Repo", "", "", "block"},
		{"remote API", managed + "/Web%20App/_apis/git", "", "", "block"},
		{"raw API root project", "https://dev.azure.com/other/_apis/_git/Repo", "", "", "block"},
		{"encoded API root project", "https://dev.azure.com/other/_%61pis/_git/Repo", "", "", "block"},
		{"smart Git discovery", managed + "/Web%20App/_git/My%20Repo/info/refs?service=git-upload-pack", "", "", "block"},
		{"Git clone URL", "https://dev.azure.com/other/Project/_git/Repo.git", "", "", "block"},
		{"encoded Git clone URL", "https://dev.azure.com/other/Project/_git/Repo%2eg%69t", "", "", "block"},
		{"POST", managed + "/Web%20App/_git/My%20Repo", "post", "", "block"},
		{"HEAD", managed + "/Web%20App/_git/My%20Repo", "head", "", "block"},
		{"subframe", managed + "/Web%20App/_git/My%20Repo", "", "sub_frame", "block"},
		{"fetch", managed + "/Web%20App/_git/My%20Repo", "", "xmlhttprequest", "block"},
		{"websocket", strings.Replace(managed, "https:", "wss:", 1) + "/socket", "", "websocket", "block"},
		{"untrusted explicit port", managed + ":8443/Web%20App/_git/My%20Repo", "", "", "block"},
		{"embedded authority credentials", strings.Replace(managed, "https://", "https://test@", 1) + "/Web%20App/_git/My%20Repo", "", "", "block"},
		{"dot-segment attack", "https://dev.azure.com/other/%2e%2e/_git/Repo", "", "", "block"},
		{"encoded separator attack", "https://dev.azure.com/other/P%252foutside/_git/Repo", "", "", "block"},
		{"semicolon attack", "https://dev.azure.com/other/Project%3Bother/_git/Repo", "", "", "block"},
		{"raw collection HTTPS", team + "/Web%20App/_git/My%20Repo", "", "", ""},
		{"native VPN alias", "https://vpn.example.invalid/tfs/Team%20Collection/Web%20App/_git/My%20Repo", "", "", ""},
		{"unconfigured cloud organization", "https://dev.azure.com/other-extra/Project/_git/Repo", "", "", ""},
		{"suffix lookalike host", managed + ".outside.invalid/Web%20App/_git/My%20Repo", "", "", ""},
		{"prefix lookalike host", strings.Replace(managed, "https://", "https://prefix.", 1) + "/Web%20App/_git/My%20Repo", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			method, resource := tc.method, tc.resource
			if method == "" {
				method = "get"
			}
			if resource == "" {
				resource = "main_frame"
			}
			if got := evaluateBrowserRules(t, rules, tc.raw, method, resource); got != tc.want {
				t.Errorf("browser rule result = %q, want %q", got, tc.want)
			}
		})
	}
}

func evaluateBrowserRules(t *testing.T, rules []browserRule, raw, method, resource string) string {
	t.Helper()
	var winner browserRule
	var re *regexp.Regexp
	for _, candidate := range rules {
		condition := candidate.Condition
		if candidate.Priority <= winner.Priority ||
			(len(condition.Methods) > 0 && !slices.Contains(condition.Methods, method)) ||
			(len(condition.ResourceTypes) > 0 && !slices.Contains(condition.ResourceTypes, resource)) ||
			(len(condition.ResourceTypes) == 0 && resource == "main_frame") {
			continue
		}
		pattern := regexp.MustCompile("(?i)" + condition.Regex)
		if pattern.MatchString(raw) {
			winner, re = candidate, pattern
		}
	}
	switch winner.Action.Type {
	case "block":
		return "block"
	case "redirect":
		rewrite := strings.NewReplacer(`\1`, "${1}", `\2`, "${2}", `\3`, "${3}", `\4`, "${4}", `\5`, "${5}")
		return re.ReplaceAllString(raw, rewrite.Replace(winner.Action.Redirect.Substitution))
	default:
		return ""
	}
}

func TestHTTPSBrowserRejectsAmbiguousConfiguration(t *testing.T) {
	t.Run("same canonical alias", func(t *testing.T) {
		cfg, records := browserRuleFixture()
		cfg.Profiles[1].Organization = "https://dev.azure.com/team-proxy"
		dir := filepath.Join(t.TempDir(), "browser")
		if err := writeHTTPSBrowserExtension(dir, cfg, records); err == nil {
			t.Fatal("export accepted a conflicting alias")
		}
		if _, err := os.Lstat(dir); !os.IsNotExist(err) {
			t.Fatalf("incomplete extension left behind: %v", err)
		}
	})
	t.Run("nested collections", func(t *testing.T) {
		cfg, records := browserRuleFixture()
		cfg.Profiles[1].CollectionURL = cfg.Profiles[0].CollectionURL + "/Child"
		if _, _, err := makeBrowserRules(cfg, records); err == nil {
			t.Fatal("export accepted overlapping collection paths")
		}
	})
	t.Run("collection goes to alias", func(t *testing.T) {
		cfg, records := browserRuleFixture()
		cfg.Profiles[1].CollectionURL = "https://team-proxy.visualstudio.com/Child"
		if _, _, err := makeBrowserRules(cfg, records); err == nil {
			t.Fatal("export accepted a collection URL that would trigger browser routing")
		}
	})
}
