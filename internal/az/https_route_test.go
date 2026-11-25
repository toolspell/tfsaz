package az

import (
	"errors"
	"strings"
	"testing"
)

func httpsRouteTestConfig() config {
	return config{Profiles: []Profile{
		{
			ID:            "legacy",
			Organization:  "https://example.visualstudio.com",
			CollectionURL: "https://server.invalid:8443/tfs/Main%20Collection",
			Aliases:       []string{"https://dev.azure.com/another", "https://vpn.invalid/tfs/Main%20Collection"},
		},
		{
			ID:            "modern",
			Organization:  "https://dev.azure.com/current",
			CollectionURL: "https://server.invalid:8443/RootCollection",
		},
	}}
}

func TestMapHTTPSRoute(t *testing.T) {
	cfg := httpsRouteTestConfig()
	const legacy = "https://server.invalid:8443/tfs/Main%20Collection"
	const modern = "https://server.invalid:8443/RootCollection"
	for _, tc := range []struct {
		name, source, destination, profileID, action string
	}{
		{"repository", "https://example.visualstudio.com/My%20Project/_git/Repo", legacy + "/My%20Project/_git/Repo", "legacy", httpsActionRedirect},
		{"qualified PR", "https://example.visualstudio.com/DefaultCollection/My%20Project/_git/Repo/pullrequest/42?view=files&iteration=3#details", legacy + "/My%20Project/_git/Repo/pullrequest/42?view=files&iteration=3#details", "legacy", httpsActionRedirect},
		{"canonical commit link", "https://DEV.AZURE.COM/EXAMPLE/my%20project/_git/repo/commit/AbC09?path=%2Fsrc%2Fa%20file.go", legacy + "/my%20project/_git/repo/commit/AbC09?path=%2Fsrc%2Fa%20file.go", "legacy", httpsActionRedirect},
		{"explicit alias work item", "https://dev.azure.com/another/My%20Project/_workitems/edit/45", legacy + "/My%20Project/_workitems/edit/45", "legacy", httpsActionRedirect},
		{"modern build", "https://dev.azure.com/current/0807f858-dd28-4f2a-a5f0-eeeb4d4e09cd/_build/results?buildId=13&view=logs", modern + "/0807f858-dd28-4f2a-a5f0-eeeb4d4e09cd/_build/results?buildId=13&view=logs", "modern", httpsActionRedirect},
		{"default repository", "https://dev.azure.com/current/_git/Repo/", modern + "/_git/Repo/", "modern", httpsActionRedirect},
		{"git base", "https://dev.azure.com/current/Project/_git/Repo.git", modern + "/Project/_git/Repo.git", "modern", httpsActionGit},
		{"git fetch discovery", "https://dev.azure.com/current/Project/_git/Repo/info/refs?service=git-upload-pack", modern + "/Project/_git/Repo/info/refs?service=git-upload-pack", "modern", httpsActionGit},
		{"git push discovery", "https://dev.azure.com/current/Project/_git/Repo/info/refs?service=git-receive-pack", modern + "/Project/_git/Repo/info/refs?service=git-receive-pack", "modern", httpsActionGit},
		{"git fetch", "https://dev.azure.com/current/Project/_git/Repo/git-upload-pack", modern + "/Project/_git/Repo/git-upload-pack", "modern", httpsActionGit},
		{"git push", "https://dev.azure.com/current/Project/_git/Repo/git-receive-pack", modern + "/Project/_git/Repo/git-receive-pack", "modern", httpsActionGit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mapHTTPSRoute(cfg, nil, tc.source)
			want := httpsRoute{ProfileID: tc.profileID, Destination: tc.destination, Action: tc.action}
			if err != nil || got != want {
				t.Fatalf("route = %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestMapHTTPSRouteRejectsUnsafeAndUnknownURLs(t *testing.T) {
	cfg := httpsRouteTestConfig()
	const base = "https://dev.azure.com/current"
	const repo = base + "/Project/_git/Repo"
	for _, tc := range []struct{ name, source string }{
		{"scheme", "http://dev.azure.com/current/Project/_git/Repo"},
		{"relative", "//dev.azure.com/current/Project/_git/Repo"},
		{"userinfo", "https://login@dev.azure.com/current/Project/_git/Repo"},
		{"password", "https://login:password@dev.azure.com/current/Project/_git/Repo"},
		{"port", "https://dev.azure.com:443/current/Project/_git/Repo"},
		{"host dot", "https://dev.azure.com./current/Project/_git/Repo"},
		{"host suffix", "https://dev.azure.com.external.invalid/current/Project/_git/Repo"},
		{"unknown legacy", "https://current.visualstudio.com/Project/_git/Repo"},
		{"organization suffix", "https://dev.azure.com/current-suffix/Project/_git/Repo"},
		{"native alias", "https://vpn.invalid/tfs/Main%20Collection/Project/_git/Repo"},
		{"raw API", base + "/Project/_apis/git/repositories/Repo"},
		{"embedded raw API", repo + "/_apis"},
		{"dumb Git", repo + "/HEAD"},
		{"invalid Git service", repo + "/info/refs?service=external"},
		{"missing Git service", repo + "/info/refs"},
		{"repeated Git service", repo + "/info/refs?service=git-upload-pack&service=git-receive-pack"},
		{"Git service fragment", repo + "/info/refs?service=git-upload-pack#other"},
		{"query redirect", repo + "/pullrequest/42?returnUrl=https%3A%2F%2Fexternal.invalid"},
		{"double escaped query redirect", repo + "/pullrequest/42?path=%252F%252Fexternal.invalid"},
		{"query contains another key", repo + "/pullrequest/42?view=files%26returnUrl%3Dlocal"},
		{"query duplicate case", repo + "/pullrequest/42?view=files&View=summary"},
		{"query control", repo + "/pullrequest/42?view=files%0D%0A"},
		{"fragment redirect", repo + "/pullrequest/42#//external.invalid"},
		{"dot path", base + "/Project/../_git/Repo"},
		{"nested path encoding", base + "/Project%252FNested/_git/Repo"},
		{"nested windows separator", base + "/Project%255CNested/_git/Repo"},
		{"server path parameter", base + "/Project%253Bname/_git/Repo"},
		{"decoded control", base + "/Project%00/_git/Repo"},
		{"trailing project dot", base + "/Project./_git/Repo"},
		{"padding", base + "/Project%20/_git/Repo"},
		{"empty path segment", base + "//Project/_git/Repo"},
		{"zero ID", repo + "/pullrequest/0"},
		{"overflow ID", repo + "/pullrequest/9007199254740992"},
		{"build missing ID", base + "/Project/_build/results?view=logs"},
		{"unsupported route", repo + "/settings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mapHTTPSRoute(cfg, nil, tc.source)
			if err == nil || got != (httpsRoute{}) || strings.Contains(err.Error(), tc.source) {
				t.Fatalf("route = %+v, %v; want an error without the raw URL", got, err)
			}
		})
	}
}

func TestMapHTTPSRouteManagedBinding(t *testing.T) {
	cfg := httpsRouteTestConfig()
	profile := cfg.Profiles[0]
	record := setupRecord{ProfileID: profile.ID, CollectionURL: profile.CollectionURL, Project: "Work Space", Repository: "Repo", Phase: setupPhaseActive}
	record.Host = setupHost(record.ProfileID, record.CollectionURL, record.Project, record.Repository)
	legacy := "https://" + record.Host
	canonical := "https://dev.azure.com/" + strings.TrimSuffix(record.Host, ".visualstudio.com")
	for _, alias := range []string{legacy, canonical} {
		for _, path := range []string{
			"/work%20space/_git/repo/pullrequest/42", "/Work%20Space/_workitems/edit/42", "/Work%20Space/_build/results?buildId=42",
		} {
			route, err := mapHTTPSRoute(cfg, []setupRecord{record, record}, alias+path)
			if err != nil || route.ProfileID != profile.ID || route.Destination != profile.CollectionURL+path {
				t.Fatalf("route for %s = %+v, %v", alias+path, route, err)
			}
		}
		for _, path := range []string{"/Other/_git/Repo", "/Work%20Space/_git/Repo-extra", "/Other/_workitems/edit/42", "/Other/_build/results?buildId=42", "/_git/Repo"} {
			if _, err := mapHTTPSRoute(cfg, []setupRecord{record}, alias+path); err == nil {
				t.Fatalf("mapped %s outside managed binding", alias+path)
			}
		}
	}
	for _, stale := range []setupRecord{
		{ProfileID: record.ProfileID, CollectionURL: record.CollectionURL, Project: record.Project, Repository: record.Repository, Host: record.Host, Phase: setupPhasePreparing},
		{ProfileID: record.ProfileID, CollectionURL: record.CollectionURL, Project: record.Project, Repository: record.Repository, Host: record.Host, Phase: setupPhaseRestoring},
		{ProfileID: record.ProfileID, CollectionURL: "https://stale.invalid/Collection", Project: record.Project, Repository: record.Repository, Host: record.Host, Phase: setupPhaseActive},
		{ProfileID: record.ProfileID, CollectionURL: record.CollectionURL, Project: record.Project, Repository: "Changed", Host: record.Host, Phase: setupPhaseActive},
	} {
		if _, err := mapHTTPSRoute(cfg, []setupRecord{stale}, legacy+"/Work%20Space/_git/Repo"); !errors.Is(err, errNoHTTPSRoute) {
			t.Fatalf("stale or invalid binding got %v; want no route", err)
		}
	}
	cfg.Profiles[0].Aliases = append(cfg.Profiles[0].Aliases, legacy)
	if _, err := mapHTTPSRoute(cfg, []setupRecord{record}, legacy+"/Other/_git/Repo"); err == nil {
		t.Fatal("a configured selector widened the managed binding")
	}
}

func TestMapHTTPSRouteRejectsAmbiguityAndNestedCollection(t *testing.T) {
	cfg := httpsRouteTestConfig()
	cfg.Profiles[1].Aliases = []string{"https://dev.azure.com/example"}
	if _, err := mapHTTPSRoute(cfg, nil, "https://dev.azure.com/example/Project/_git/Repo"); err == nil {
		t.Fatal("mapped a collision between an explicit and a derived alias")
	}
	cfg.Profiles[1].Aliases = nil
	cfg.Profiles[1].CollectionURL = "https://server.invalid.:443/tfs/Main Collection/Child Collection"
	cfg.Profiles[0].CollectionURL = "https://server.invalid/tfs/Main Collection"
	if _, err := mapHTTPSRoute(cfg, nil, "https://example.visualstudio.com/Child%2520Collection/_git/Repo"); err == nil {
		t.Fatal("mapped a path inside another configured collection")
	}
	cfg.Profiles[0].CollectionURL = "https://example.visualstudio.com./DefaultCollection"
	if _, err := mapHTTPSRoute(cfg, nil, "https://example.visualstudio.com/Project/_git/Repo"); err == nil {
		t.Fatal("mapped a destination under the source HTTPS alias")
	}
}

func TestMapHTTPSGitBase(t *testing.T) {
	cfg := httpsRouteTestConfig()
	const input = "https://dev.azure.com/current/Project/_git/Repo"
	got, err := mapHTTPSGitBase(cfg, nil, input)
	want := httpsRoute{"modern", "https://server.invalid:8443/RootCollection/Project/_git/Repo", httpsActionGit}
	if err != nil || got != want {
		t.Fatalf("route = %+v, %v; want %+v", got, err, want)
	}
	for _, raw := range []string{input + "?", input + "#", input + "?view=x", input + "/pullrequest/1", input + "/info/refs?service=git-upload-pack",
		"https://login@dev.azure.com/current/Project/_git/Repo", "https://dev.azure.com:443/current/Project/_git/Repo", "https://dev.azure.com/current/Project/_git/%xx"} {
		if _, err := mapHTTPSGitBase(cfg, nil, raw); err == nil || errors.Is(err, errNoHTTPSRoute) {
			t.Fatalf("alias route for %s = %v; want known alias failure", raw, err)
		}
	}
	if _, err := mapHTTPSGitBase(cfg, nil, "https://other@dev.azure.com/other/Project/_git/Repo"); !errors.Is(err, errNoHTTPSRoute) {
		t.Fatalf("unrelated route error = %v", err)
	}
}
