package az

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func setConfig(t *testing.T, profiles ...Profile) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	data, err := json.Marshal(config{Profiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TFSAZ_CONFIG", path)
}

func gitAt(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git: %v: %s", err, out)
	}
}

func TestRemoteScope(t *testing.T) {
	setConfig(t, Profile{ID: "one", Organization: "https://dev.azure.com/example", CollectionURL: "https://host.example.invalid:8443/tfs/First Collection/", Aliases: []string{"https://other.example.invalid/First Collection", "https://example.visualstudio.com"}})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ remote, web, project string }{
		{"https://example@dev.azure.com/example/P%20Q/_git/R.git", "https://dev.azure.com/example/P%20Q/_git/R.git", "P Q"},
		{"https://dev.azure.com/example/_git/R.git", "https://dev.azure.com/example/_git/R.git", "R.git"},
		{"git@ssh.dev.azure.com:v3/example/P%20Q/R.git", "https://dev.azure.com/example/P%20Q/_git/R.git", "P Q"},
		{"ssh://git@ssh.dev.azure.com:22/v3/example/P%20Q/R.git", "https://dev.azure.com/example/P%20Q/_git/R.git", "P Q"},
		{"git@vs-ssh.visualstudio.com:v3/example/P%20Q/R.git", "https://example.visualstudio.com/P%20Q/_git/R.git", "P Q"},
		{"https://example.visualstudio.com/DefaultCollection/P%20Q/_git/R.git", "https://example.visualstudio.com/DefaultCollection/P%20Q/_git/R.git", "P Q"},
		{"https://example.visualstudio.com/_git/R.git", "https://example.visualstudio.com/_git/R.git", "R.git"},
		{"https://host.example.invalid:8443/tfs/First%20Collection/P%20Q/_git/R.git", "https://dev.azure.com/example/P%20Q/_git/R.git", "P Q"},
		{"https://host.example.invalid:8443/tfs/First%20Collection/_git/R.git", "https://dev.azure.com/example/R.git/_git/R.git", "R.git"},
		{"https://other.example.invalid/First%20Collection/_git/R.git", "https://dev.azure.com/example/R.git/_git/R.git", "R.git"},
		{"https://other.example.invalid/First%20Collection/P%20Q/_git/R.git", "https://dev.azure.com/example/P%20Q/_git/R.git", "P Q"},
	} {
		t.Run(tc.remote, func(t *testing.T) {
			p, s := cfg.remoteScope(tc.remote)
			if p == nil || p.ID != "one" || s.WebURL != tc.web || s.Project != tc.project || s.Repository != "R.git" {
				t.Fatalf("wrong scope %+v", s)
			}
		})
	}
	for _, raw := range []string{"https://unknown.example.invalid/First%20Collection/P/_git/R", "https://host.example.invalid/tfs/First%20Collection/P/_git/R", "https://host.example.invalid:8443/First%20Collection/P/_git/R", "https://dev.azure.com/example/P%2fX/_git/R", "https://dev.azure.com/elsewhere/P/_git/R", "https://example.visualstudio.com/P/_git/%252e%252e", "https://dev.azure.com/example/P/_git/R?x=1"} {
		if p, _ := cfg.remoteScope(raw); p != nil {
			t.Errorf("unexpected profile for %s", raw)
		}
	}
}

func TestCheckoutSelectsT3PrimaryRemote(t *testing.T) {
	setConfig(t, Profile{ID: "two", Organization: "https://two.visualstudio.com", CollectionURL: "https://server.example.invalid/Two"})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	gitAt(t, cwd, "init", "-q")
	gitAt(t, cwd, "remote", "add", "upstream", "https://two.visualstudio.com/P/_git/R")
	cfgScope := func(want bool) {
		t.Helper()
		p, _, err := cfg.scope(context.Background(), cwd, nil)
		if (p != nil) != want || (err == nil) != want {
			t.Fatalf("scope: %v, %v", p, err)
		}
	}
	cfgScope(true)
	gitAt(t, cwd, "remote", "add", "origin", "https://unknown.example.invalid/P/_git/R")
	cfgScope(false)
	gitAt(t, cwd, "remote", "set-url", "origin", "git@vs-ssh.visualstudio.com:v3/two/P/R")
	t.Setenv("GIT_DIR", filepath.Join(cwd, "bogus"))
	cfgScope(true)
	if _, _, err := cfg.scope(context.Background(), cwd, map[string]string{"organization": "https://dev.azure.com/unconfigured"}); err == nil {
		t.Fatal("unknown explicit scope fell back to origin")
	}
}

func TestConfigValidation(t *testing.T) {
	p := Profile{ID: "one", Organization: "https://dev.azure.com/one", CollectionURL: "https://server.example.invalid/One", CAFile: "ca.pem"}
	setConfig(t, p)
	cfg, err := loadConfig()
	if err != nil || cfg.Profiles[0].CAFile != filepath.Join(filepath.Dir(os.Getenv("TFSAZ_CONFIG")), "ca.pem") {
		t.Fatal("CA path", cfg, err)
	}
	for _, bad := range []string{"http://server.example.invalid/One", "https://user@server.example.invalid/One", "https://server.example.invalid/tfs/%2e%2e/One", "https://server.example.invalid/One?query=x", "https://server.example.invalid/One%2fTwo", "https://server.example.invalid/", "https://server.example.invalid/One#fragment"} {
		b := p
		b.CollectionURL = bad
		setConfig(t, b)
		if _, err := loadConfig(); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	b := p
	b.ID = "two"
	b.Organization = "https://two.visualstudio.com"
	setConfig(t, p, b)
	if _, err := loadConfig(); err == nil {
		t.Fatal("accepted ambiguous collection URL")
	}
}

func TestAmbiguousServerCheckout(t *testing.T) {
	setConfig(t, Profile{ID: "parent", Organization: "https://parent.visualstudio.com", CollectionURL: "https://server.example.invalid/tfs"},
		Profile{ID: "child", Organization: "https://child.visualstudio.com", CollectionURL: "https://server.example.invalid/tfs/Child"})
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := cfg.remoteScope("https://server.example.invalid/tfs/Child/_git/Repo"); p != nil {
		t.Fatal("ambiguous checkout selected a credential profile")
	}
}

func TestCanonicalOrganizationAmbiguityLeavesRawRESTSelectorsUnchanged(t *testing.T) {
	legacy := Profile{ID: "legacy", Organization: "https://shared-name.visualstudio.com", CollectionURL: "https://legacy.example.invalid/Collection"}
	modern := Profile{ID: "modern", Organization: "https://dev.azure.com/shared-name", CollectionURL: "https://modern.example.invalid/Collection"}
	const path = "/Project/_git/Repo"
	cfg := config{Profiles: []Profile{legacy}}
	if p, _ := cfg.selectBase(modern.Organization); p == nil || p.ID != legacy.ID {
		t.Fatal("canonical organization did not select the legacy profile")
	}
	if p, _, _, err := cfg.restTarget(t.Context(), t.TempDir(), modern.Organization+"/_apis/projects"); p != nil || err == nil {
		t.Fatal("an inferred CLI organization appeared as a raw REST URL selector")
	}
	cfg.Profiles = append(cfg.Profiles, modern)
	if p, _ := cfg.selectBase(modern.Organization); p != nil {
		t.Fatal("selected a credential for an ambiguous explicit organization")
	}
	if p, _ := cfg.remoteScope(modern.Organization + path); p != nil {
		t.Fatal("selected a credential for an ambiguous canonical checkout")
	}
	if p, _ := cfg.remoteScope(legacy.Organization + path); p == nil || p.ID != legacy.ID {
		t.Fatal("canonical ambiguity tainted the legacy checkout")
	}
	if p, _, _, err := cfg.restTarget(t.Context(), t.TempDir(), modern.Organization+"/_apis/projects"); err != nil || p == nil || p.ID != modern.ID {
		t.Fatalf("explicit raw REST selector changed: %v, %v", p, err)
	}
}
