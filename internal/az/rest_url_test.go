package az

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRestTarget(t *testing.T) {
	p := Profile{
		ID:            "one",
		Organization:  "https://dev.azure.com/One",
		CollectionURL: "https://server.example.invalid:8443/tfs/First Collection",
		Aliases:       []string{"https://one.visualstudio.com", "https://lan.example.invalid/First Collection"},
	}
	second := Profile{ID: "two", Organization: "https://two.visualstudio.com", CollectionURL: "https://server.example.invalid:8443/Second"}
	setConfig(t, p, second)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	managedHost := setupHost(p.ID, cfg.Profiles[0].CollectionURL, "Project X", "Repo")
	if err := cfg.addSetupAliases([]setupRecord{{
		ProfileID: p.ID, CollectionURL: cfg.Profiles[0].CollectionURL, Host: managedHost, Project: "Project X", Repository: "Repo",
	}}); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	for _, tc := range []struct {
		name  string
		raw   string
		id    string
		parts []string
		query url.Values
	}{
		{"collection URL with port and path prefix", "https://SERVER.example.invalid:8443/TFS/FIRST%20COLLECTION/_apis/projects", "one", []string{"_apis", "projects"}, url.Values{}},
		{"collection URL without virtual directory", "https://server.example.invalid:8443/Second/Project/_apis/git/repositories", "two", []string{"Project", "_apis", "git", "repositories"}, url.Values{}},
		{"modern alias and query parameters", "https://dev.azure.com/one/Pr%C3%B6ject/_apis/wit/workItems?api-version=7.0&ids=1&ids=2&%24select=Id%2CTitle&plus=%2B&blank=", "one", []string{"Pröject", "_apis", "wit", "workItems"}, url.Values{
			"api-version": {"7.0"}, "ids": {"1", "2"}, "$select": {"Id,Title"}, "plus": {"+"}, "blank": {""},
		}},
		{"legacy alias with DefaultCollection", "https://one.visualstudio.com/DefaultCollection/Project%20X/Team%20A/_apis/work/teamsettings", "one", []string{"Project X", "Team A", "_apis", "work", "teamsettings"}, url.Values{}},
		{"managed alias", "https://" + managedHost + "/_apis/ConnectionData", "one", []string{"_apis", "ConnectionData"}, url.Values{}},
		{"explicit LAN alias", "https://lan.example.invalid/First%20Collection/_apis/projects", "one", []string{"_apis", "projects"}, url.Values{}},
		{"REST discovery root", "https://dev.azure.com/one/_apis", "one", []string{"_apis"}, url.Values{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, parts, query, err := cfg.restTarget(context.Background(), cwd, tc.raw)
			if err != nil || profile == nil || profile.ID != tc.id || !slices.Equal(parts, tc.parts) || !reflect.DeepEqual(query, tc.query) {
				t.Fatalf("target = %v, %v, %v, %v", profile, parts, query, err)
			}
		})
	}

	for _, raw := range []string{
		"http://server.example.invalid:8443/tfs/First%20Collection/_apis/projects",
		"https://server.example.invalid/tfs/First%20Collection/_apis/projects",
		"https://server.example.invalid:8443/tfs/First%20CollectionTwo/_apis/projects",
		"https://elsewhere.example.invalid/tfs/First%20Collection/_apis/projects",
		"https://server.example.invalid:8443@elsewhere.example.invalid/tfs/First%20Collection/_apis/projects",
		"https://secret@dev.azure.com/one/_apis/projects",
		"https://management.azure.com/subscriptions/one",
		"https://dev.azure.com/unknown/_apis/projects",
		"https://dev.azure.com/one/_apis/projects#",
		"https://dev.azure.com/one/_apis/projects?token=secret#fragment",
		"https://dev.azure.com/one/_apis/projects?token=secret%ZZ",
		"https://dev.azure.com/one/_git/repo",
		"https://dev.azure.com/one/Project/Team/Extra/_apis/projects",
		"https://dev.azure.com/one//_apis/projects",
		"https://dev.azure.com/one/%2F_apis/projects",
		"https://dev.azure.com/one/%255c_apis/projects",
		"https://dev.azure.com/one/%252E%252e/_apis/projects",
		"https://dev.azure.com/one/../_apis/projects",
		"https://dev.azure.com/one/%00/_apis/projects",
		"//dev.azure.com/one/_apis/projects",
		"///_apis/projects",
		"https:/_apis/projects",
		"https:_apis/projects",
	} {
		profile, parts, query, err := cfg.restTarget(context.Background(), cwd, raw)
		if err == nil || profile != nil || parts != nil || query != nil || strings.Contains(err.Error(), "secret") {
			t.Errorf("rejected input %q produced %v, %v, %v, %v", raw, profile, parts, query, err)
		}
	}
}

func TestRestTargetCheckout(t *testing.T) {
	p := Profile{ID: "one", Organization: "https://dev.azure.com/one", CollectionURL: "https://server.example.invalid/tfs/One"}
	setConfig(t, p)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	gitAt(t, cwd, "init", "-q")
	gitAt(t, cwd, "remote", "add", "origin", "https://unconfigured.visualstudio.com/Project/_git/Repo")
	if profile, _, _, err := cfg.restTarget(context.Background(), cwd, "/_apis/projects"); profile != nil || err == nil {
		t.Fatalf("relative target selected a profile for an unconfigured checkout: %v, %v", profile, err)
	}
	gitAt(t, cwd, "remote", "set-url", "origin", "https://dev.azure.com/one/Project/_git/Repo")
	for _, raw := range []string{"Project/_apis/wit/workItems?ids=1", "/Project/_apis/wit/workItems?ids=1"} {
		profile, parts, query, err := cfg.restTarget(context.Background(), cwd, raw)
		if err != nil || profile == nil || profile.ID != "one" || !slices.Equal(parts, []string{"Project", "_apis", "wit", "workItems"}) || query.Get("ids") != "1" {
			t.Fatalf("relative target = %v, %v, %v, %v", profile, parts, query, err)
		}
	}
	if profile, _, _, err := cfg.restTarget(context.Background(), cwd, "https://other.visualstudio.com/_apis/projects"); profile != nil || err == nil {
		t.Fatalf("unconfigured absolute target fell back to checkout: %v, %v", profile, err)
	}
}

func TestRestTargetOverlappingCollections(t *testing.T) {
	parent := Profile{ID: "parent", Organization: "https://parent.visualstudio.com", CollectionURL: "https://server.example.invalid/tfs"}
	child := Profile{ID: "child", Organization: "https://child.visualstudio.com", CollectionURL: "https://server.example.invalid/tfs/Child"}
	setConfig(t, parent, child)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	gitAt(t, cwd, "init", "-q")
	gitAt(t, cwd, "remote", "add", "origin", "https://parent.visualstudio.com/Project/_git/Repo")
	for _, raw := range []string{
		"https://server.example.invalid/tfs/Child/_apis/projects",
		"https://server.example.invalid/tfs/Child/_apis",
		"https://parent.visualstudio.com/cHiLd/_apis/projects",
		"/Child/_apis/projects",
	} {
		if profile, _, _, err := cfg.restTarget(context.Background(), cwd, raw); profile != nil || !errors.Is(err, errRestAmbiguous) {
			t.Errorf("target %q = %v, %v, want ambiguous collection error", raw, profile, err)
		}
	}
	for _, raw := range []string{
		"https://child.visualstudio.com/_apis/projects",
		"https://server.example.invalid/tfs/Child/Project/Team/_apis/work/teamsettings",
	} {
		if profile, _, _, err := cfg.restTarget(context.Background(), cwd, raw); profile == nil || profile.ID != "child" || err != nil {
			t.Errorf("target %q = %v, %v, want child", raw, profile, err)
		}
	}

	cfg.Profiles[1].CollectionURL = "https://server.example.invalid:443/tfs/Child"
	if profile, _, _, err := cfg.restTarget(context.Background(), cwd, "/Child/_apis/projects"); profile != nil || !errors.Is(err, errRestAmbiguous) {
		t.Errorf("default HTTPS port bypassed overlap protection: %v, %v", profile, err)
	}
	cfg.Profiles[1].CollectionURL = "https://different.example.invalid/Child"
	cfg.Profiles[1].Aliases = []string{"https://server.example.invalid/tfs/Child"}
	if profile, _, _, err := cfg.restTarget(context.Background(), cwd, "/Child/_apis/projects"); profile != nil || !errors.Is(err, errRestAmbiguous) {
		t.Errorf("explicit nested server alias bypassed overlap protection: %v, %v", profile, err)
	}
	cfg.Profiles[1].CollectionURL = "https://server.example.invalid/tfs/Child%20Collection"
	if profile, _, _, err := cfg.restTarget(context.Background(), cwd, "/Child%2520Collection/_apis/projects"); profile != nil || !errors.Is(err, errRestAmbiguous) {
		t.Errorf("repeated encoding bypassed overlap protection: %v, %v", profile, err)
	}
}
