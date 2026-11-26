package az

import (
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
)

func TestSetupBindsRepositoryToCollection(t *testing.T) {
	p := Profile{ID: "first", CollectionURL: "https://server.invalid:8443/tfs/Second%20Collection", Organization: "https://dev.azure.com/first"}
	other := Profile{ID: "other", CollectionURL: "https://server.invalid:9443/tfs/Second%20Collection", Organization: "https://other.visualstudio.com"}
	cfg := config{Profiles: []Profile{p, other}}
	const remote = "ssh://git@server.invalid:2222/tfs/Second%20Collection/Team%20Project/_git/Tool%20Kit"
	dir := t.TempDir()
	bind := func(original, override, profile string) (setupRecord, error) {
		return makeSetupRecord(t.Context(), &memoryStore{}, cfg, dir, dir, original, override, profile, filepath.Join(dir, "managed.conf"))
	}
	if _, err := bind(remote, "", ""); err == nil {
		t.Fatal("SSH origin is ambiguous across collection listeners")
	}
	r, err := bind(remote, "", "first")
	if err != nil {
		t.Fatal(err)
	}
	httpsBinding, err := bind(p.CollectionURL+"/Team%20Project/_git/Tool%20Kit", remote, "")
	if err != nil || httpsBinding.Host != r.Host {
		t.Fatalf("HTTPS binding differs: %v", err)
	}
	if _, err := bind(p.CollectionURL+"/Another/_git/Tool%20Kit", remote, ""); err == nil {
		t.Fatal("accepted SSH target from a different project")
	}
	if err := cfg.addSetupAliases([]setupRecord{r, r}); err != nil {
		t.Fatal(err)
	}
	target, scope := cfg.remoteScope(r.RemoteURL)
	if target == nil || target.ID != p.ID || scope.Project != "Team Project" || scope.Repository != "Tool Kit" || scope.WebURL != "https://"+r.Host+"/Team%20Project/_git/Tool%20Kit" {
		t.Fatalf("unexpected alias scope %+v", scope)
	}
	explicit, _ := cfg.selectBase(scope.Organization)
	if explicit != target {
		t.Fatal("T3 explicit organization and checkout disagree")
	}
	if len(target.Aliases) != 1 {
		t.Fatal("duplicate binding for duplicate checkout")
	}
	changed := config{Profiles: []Profile{other}}
	if err := changed.addSetupAliases([]setupRecord{r}); err != nil {
		t.Fatal(err)
	}
	if selected, _ := changed.selectBase(scope.Organization); selected != nil {
		t.Fatal("stale binding routed to another profile")
	}
	changed.Profiles[0].Aliases = []string{scope.Organization}
	if err := changed.addSetupAliases([]setupRecord{r}); err == nil {
		t.Fatal("stale binding reassigned to another profile")
	}
}

func TestSetupReadsSSHURLWithInjectedStore(t *testing.T) {
	var sshURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkClientTestAuth(t, r)
		if got := r.URL.EscapedPath(); got != "/tfs/Collection/Project/_apis/git/repositories/Repository" {
			t.Errorf("request path = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"sshUrl": sshURL})
	}))
	defer server.Close()
	p := clientTestProfile(t, server, "/tfs/Collection")
	p.ID = "setup-ssh-" + rand.Text()
	p.Organization = "https://setup-ssh.visualstudio.com"
	sshURL = "ssh://git@" + server.Listener.Addr().(*net.TCPAddr).IP.String() + ":22/tfs/Collection/Project/_git/Repository"
	store := &memoryStore{values: map[string]string{p.ID: clientTestPassword}}
	dir := t.TempDir()
	r, err := makeSetupRecord(t.Context(), store, config{Profiles: []Profile{p}}, dir, dir, p.CollectionURL+"/Project/_git/Repository", "", "", filepath.Join(dir, "managed.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(store.reads, p.ID) || r.SSHURL != sshURL {
		t.Fatalf("SSH URL %q from reads %q, want %q from the injected store", r.SSHURL, store.reads, sshURL)
	}
}

func TestSetupRefusesCredentialBearingURLs(t *testing.T) {
	for _, raw := range []string{"https://private@server.invalid/C/P/_git/R", "ssh://name:private@server.invalid/C/P/_git/R", "https://server.invalid/C/P/_git/R?key=private"} {
		if _, err := nativeSetupURL(raw); err == nil {
			t.Fatal("accepted credentials in a setup URL")
		}
	}
}
