package az

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gofrs/flock"
)

func profileTestInput(t *testing.T, pat string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(r) })
	if _, err := io.WriteString(w, pat+"\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return r
}

// testKeyringList keeps the keyring entry list out of the real home directory.
func testKeyringList(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), ".tfsaz", "keyring-entries.json")
}

func readTestKeyringList(t *testing.T, path string) []keyringEntry {
	t.Helper()
	entries, err := readKeyringList(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestProfileOnboardingAndLogout(t *testing.T) {
	firstPAT, secondPAT := rand.Text(), rand.Text()
	var firstRequests, secondRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sent, _ := r.BasicAuth()
		var pat, account string
		switch r.URL.EscapedPath() {
		case "/First/_apis/connectionData":
			firstRequests.Add(1)
			pat, account = firstPAT, "first@example.invalid"
		case "/prefix/Second%20Collection/_apis/connectionData":
			secondRequests.Add(1)
			pat, account = secondPAT, "second@example.invalid"
		default:
			t.Error("profile login sent a request outside the configured collection")
			http.NotFound(w, r)
			return
		}
		if sent != pat {
			t.Error("profile login did not use the credential for its configured collection")
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":%q}}`, repoTestViewerID, account)
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "fresh", "config.json")
	t.Setenv("TFSAZ_CONFIG", path)
	ca := clientTestProfile(t, server, "/First").CAFile
	store := &memoryStore{values: map[string]string{"shared": rand.Text()}}
	legacy := store.values["shared"]
	var out bytes.Buffer
	list := testKeyringList(t)
	a := app{cwd: filepath.Dir(ca), stdout: &out, stderr: &out, store: store, keyringList: list}
	run := func(args ...string) string {
		t.Helper()
		out.Reset()
		if err := a.run(t.Context(), args); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := run("profile", "list"); got != "No collection profiles configured\n" || len(store.reads) != 0 {
		t.Fatal("fresh profile list must work without a credential store")
	}
	a.stdin = profileTestInput(t, firstPAT)
	run("init", "--id=shared", "--collection", server.URL+"/First/", "--ca-file", filepath.Base(ca))
	firstCfg, err := loadConfig()
	if err != nil || len(firstCfg.Profiles) != 1 {
		t.Fatalf("load initialized config: %v", err)
	}
	first := firstCfg.Profiles[0]
	if first.CollectionURL != server.URL+"/First" || first.CAFile != ca || first.ID != "shared" ||
		!strings.HasSuffix(first.Organization, ".visualstudio.com") || first.CredentialVersion != collectionCredentialVersion {
		t.Fatalf("unexpected initialized profile: %#v", first)
	}

	run("profile", "add", "--collection="+server.URL+"/prefix/Second Collection", "--ca-file", ca, "--no-login")
	cfg, err := loadConfig()
	if err != nil || len(cfg.Profiles) != 2 {
		t.Fatalf("load two profiles: %v", err)
	}
	second := cfg.Profiles[1]
	if second.CollectionURL != server.URL+"/prefix/Second%20Collection" || second.Organization == first.Organization {
		t.Fatalf("unexpected second profile: %#v", second)
	}
	if firstRequests.Load() != 1 || secondRequests.Load() != 0 {
		t.Fatal("config-only onboarding made a server request")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	run("init", "--collection", server.URL+"/prefix/Second%20Collection/", "--ca-file", ca, "--no-login")
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || bytes.Contains(after, []byte(firstPAT)) {
		t.Fatal("an equivalent retry changed the saved configuration or saved a PAT")
	}
	wantList := fmt.Sprintf("%s\t%s\t%s\tPAT stored\n%s\t%s\t%s\tno PAT\n",
		first.ID, first.Organization, first.CollectionURL, second.ID, second.Organization, second.CollectionURL)
	if got := run("profile", "list"); got != wantList {
		t.Fatalf("profile list = %q, want %q", got, wantList)
	}
	a.stdin = profileTestInput(t, secondPAT)
	run("devops", "login", "--organization", second.Organization)
	if got := run("account", "show", "--query", "user.name", "-o", "tsv"); got != "first@example.invalid\n" {
		t.Fatalf("unexpected global account: %q", got)
	}
	for range 2 {
		run("profile", "logout", "--profile", first.ID)
	}
	if got := run("account", "show", "--query", "user.name", "-o", "tsv"); got != "second@example.invalid\n" {
		t.Fatalf("unexpected global account after logout: %q", got)
	}
	if store.values["shared"] != legacy || secondRequests.Load() != 2 {
		t.Fatal("logout changed credentials outside the selected profile")
	}
	data, err := os.ReadFile(list)
	if err != nil || bytes.Contains(data, []byte(secondPAT)) ||
		!slices.Equal(readTestKeyringList(t, list), []keyringEntry{profileEntry(second)}) {
		t.Fatalf("keyring entry list after logout = %s, %v", data, err)
	}
	run("profile", "remove", "--profile", second.ID)
	cfg, err = loadConfig()
	if err != nil || len(cfg.Profiles) != 1 || cfg.Profiles[0].ID != first.ID {
		t.Fatalf("profile remove left %v, %v", cfg.Profiles, err)
	}
	if len(store.values) != 1 || store.values["shared"] != legacy {
		t.Fatal("profile remove kept its PAT or deleted another one")
	}
	for _, leftover := range []string{path + ".lock", setupRegistryForConfig(path) + ".lock", list, filepath.Dir(list)} {
		if _, err := os.Lstat(leftover); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("profile commands left %s: %v", leftover, err)
		}
	}
}

func TestProfileConfigFilesCanReuseIDForDifferentEndpoints(t *testing.T) {
	pat := rand.Text()
	var foreignRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/First/_apis/connectionData" {
			foreignRequests.Add(1)
			http.NotFound(w, r)
			return
		}
		if _, sent, ok := r.BasicAuth(); !ok || sent != pat {
			t.Error("incorrect credential for first collection")
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"first@example.invalid"}}`, repoTestViewerID)
	}))
	defer server.Close()
	ca := clientTestProfile(t, server, "/First").CAFile
	a := app{stdout: io.Discard, stderr: io.Discard, store: &memoryStore{}, keyringList: testKeyringList(t)}
	run := func(args ...string) error {
		return a.run(t.Context(), args)
	}
	firstPath := filepath.Join(t.TempDir(), "first.json")
	secondPath := filepath.Join(t.TempDir(), "second.json")
	for path, endpoint := range map[string]string{firstPath: "/First", secondPath: "/Second"} {
		t.Setenv("TFSAZ_CONFIG", path)
		if err := run("init", "--id", "shared", "--collection", server.URL+endpoint, "--ca-file", ca, "--no-login"); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TFSAZ_CONFIG", firstPath)
	a.stdin = profileTestInput(t, pat)
	if err := run("devops", "login", "--organization", server.URL+"/First"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TFSAZ_CONFIG", secondPath)
	if err := run("account", "show", "--query", "user.name", "-o", "tsv"); err == nil || !strings.Contains(err.Error(), "has no PAT") || foreignRequests.Load() != 0 {
		t.Fatalf("second config accessed the credential for a different endpoint: %v", err)
	}
	if err := run("profile", "logout", "--profile", "shared"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TFSAZ_CONFIG", firstPath)
	if err := run("account", "show", "--query", "user.name", "-o", "tsv"); err != nil {
		t.Fatalf("second config logout affected the first credential: %v", err)
	}
}

func TestProfileAddRejectsInvalidAndConflictingInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TFSAZ_CONFIG", path)
	store := &memoryStore{}
	a := app{stdout: io.Discard, stderr: io.Discard, store: store}
	const first = "https://ado.example.invalid:8443/tfs/First%20Collection"
	const second = "https://ado.example.invalid:8443/Second"
	for _, args := range [][]string{
		{"init", "--no-login"},
		{"init", "--collection", "http://ado.example.invalid/First", "--no-login"},
		{"init", "--collection", "https://ado.example.invalid", "--no-login"},
		{"init", "--collection", "https://ado.example.invalid:65536/First", "--no-login"},
		{"init", "--collection", "https://private-marker@ado.example.invalid/First", "--no-login"},
		{"init", "--collection", "https://ado.example.invalid/tfs/%252e%252e/First", "--no-login"},
		{"init", "--collection", first, "--organization", second, "--no-login"},
		{"init", "--collection", first, "--id", "invalid/id", "--no-login"},
		{"init", "--collection", first, "--pat=private-marker"},
	} {
		if err := a.run(t.Context(), args); ExitCode(err) != 2 || strings.Contains(err.Error(), "private-marker") {
			t.Fatalf("expected sanitized usage error, got %v", err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || len(store.reads) != 0 {
		t.Fatal("invalid input changed configuration or accessed the keyring")
	}
	if err := a.run(t.Context(), []string{"profile", "add", "--id", "first", "--collection", first,
		"--organization", "https://example-org.visualstudio.com", "--no-login"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--id", "first", "--collection", second},
		{"--id", "FIRST", "--collection", second},
		{"--id", "second", "--collection", first},
		{"--id", "second", "--collection", second, "--organization", "https://dev.azure.com/example-org"},
	} {
		if err := a.run(t.Context(), append([]string{"profile", "add", "--no-login"}, args...)); err == nil {
			t.Fatalf("accepted conflicting profile: %v", args)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || len(store.reads) != 0 {
		t.Fatal("a conflicting profile changed the config or accessed the keyring")
	}
	if err := a.run(t.Context(), []string{"profile", "logout", "--profile", "unknown"}); err == nil {
		t.Fatal("logout with an unknown profile should fail")
	}
}

func TestProfileAddWithoutLoginIgnoresKeyringAvailability(t *testing.T) {
	t.Setenv("TFSAZ_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	store := &memoryStore{lockedProfile: "shared"}
	a := app{stdout: io.Discard, stderr: io.Discard, store: store}
	if err := a.run(t.Context(), []string{"profile", "add", "--id", "shared", "--collection", "https://ado.example.invalid/One", "--no-login"}); err != nil {
		t.Fatal(err)
	}
	if len(store.reads) != 0 || len(store.values) != 0 {
		t.Fatal("config-only onboarding used the keyring")
	}
}

type failingProfileStore struct {
	*memoryStore
	failSet bool
}

func (s *failingProfileStore) Set(service, id, pat string) error {
	if s.failSet {
		return errors.New("fixture write failure")
	}
	return s.memoryStore.Set(service, id, pat)
}

func TestProfileAddCredentialFailureAndRetry(t *testing.T) {
	pat := rand.Text()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, sent, _ := r.BasicAuth(); sent != pat {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"fixture@example.invalid"}}`, repoTestViewerID)
	}))
	defer server.Close()
	p := clientTestProfile(t, server, "/One")
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TFSAZ_CONFIG", path)
	store := &failingProfileStore{memoryStore: &memoryStore{}}
	var out bytes.Buffer
	a := app{stdout: &out, stderr: &out, store: store, keyringList: testKeyringList(t)}
	args := []string{"init", "--collection", p.CollectionURL, "--id", p.ID, "--ca-file", p.CAFile}
	a.stdin = profileTestInput(t, rand.Text())
	if err := a.run(t.Context(), args); !errors.Is(err, errRejectedPAT) {
		t.Fatalf("expected rejected PAT, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) || len(store.values) != 0 || out.Len() != 0 {
		t.Fatal("a rejected credential changed the configuration or the keyring")
	}
	store.lockedProfile = p.ID
	if err := a.run(t.Context(), args); !errors.Is(err, errStoreUnavailable) {
		t.Fatalf("expected locked keyring, got %v", err)
	}
	store.lockedProfile = ""
	p.CredentialVersion = collectionCredentialVersion
	if err := store.Set(credentialService(p), p.ID, rand.Text()); err != nil {
		t.Fatal(err)
	}
	if err := a.run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "already has a credential") {
		t.Fatalf("login would overwrite a pre-existing keyring credential: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused profile appears in the configuration")
	}
	delete(store.values, memoryCredentialKey(credentialService(p), p.ID))

	store.failSet = true
	a.stdin = profileTestInput(t, pat)
	if err := a.run(t.Context(), args); !errors.Is(err, errStoreUnavailable) || !strings.Contains(err.Error(), "configuration was saved") {
		t.Fatalf("expected a recoverable keyring write failure, got %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil || len(store.values) != 0 {
		t.Fatal("verified setup did not leave a retryable config-only profile")
	}
	store.failSet = false
	a.stdin = profileTestInput(t, pat)
	if err := a.run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || store.values[memoryCredentialKey(credentialService(p), p.ID)] != pat {
		t.Fatal("the exact retry changed the config or failed to save the credential")
	}
}

func TestProfileAddSerializesConfigurationChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TFSAZ_CONFIG", path)
	lock := flock.New(path + ".lock")
	if locked, err := lock.TryLock(); err != nil || !locked {
		t.Fatalf("test configuration lock: %v", err)
	}
	t.Cleanup(func() { closeQuietly(lock) })
	store := &memoryStore{}
	a := app{stdout: io.Discard, stderr: io.Discard, store: store}
	args := []string{"profile", "add", "--collection", "https://ado.example.invalid/One", "--no-login"}
	if err := a.run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "configuration lock") || len(store.reads) != 0 {
		t.Fatalf("profile write did not honor the configuration lock: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a competing write created a config")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.run(t.Context(), args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile add left the configuration lock: %v", err)
	}
}

func TestProfileRemoveFindsStrandedKeyringEntries(t *testing.T) {
	pat := rand.Text()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, sent, _ := r.BasicAuth(); sent != pat || r.URL.Path != "/One/_apis/connectionData" {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"fixture@example.invalid"}}`, repoTestViewerID)
	}))
	defer server.Close()
	ca := clientTestProfile(t, server, "/One").CAFile
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("TFSAZ_CONFIG", path)
	store := &memoryStore{values: map[string]string{}}
	list := testKeyringList(t)
	var out bytes.Buffer
	a := app{stdout: &out, stderr: io.Discard, store: store, keyringList: list}
	run := func(args ...string) (string, error) {
		t.Helper()
		out.Reset()
		err := a.run(t.Context(), args)
		return out.String(), err
	}

	a.stdin = profileTestInput(t, pat)
	if _, err := run("init", "--id", "original", "--collection", server.URL+"/One", "--ca-file", ca); err != nil {
		t.Fatal(err)
	}
	stranded := keyringEntry{credentialService(Profile{CollectionURL: server.URL + "/One", CredentialVersion: collectionCredentialVersion}), "original"}
	if got := readTestKeyringList(t, list); !slices.Equal(got, []keyringEntry{stranded}) {
		t.Fatalf("login recorded %v", got)
	}
	// Renaming the profile in the file strands the PAT saved for the old ID.
	renamed := Profile{ID: "renamed", Organization: "https://one-example.visualstudio.com", CollectionURL: server.URL + "/One", CAFile: ca, CredentialVersion: collectionCredentialVersion}
	data, err := json.Marshal(config{Profiles: []Profile{renamed}})
	if err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, path, string(data))
	want := fmt.Sprintf("renamed\t%s\t%s\tno PAT\n\nKeyring entries that no profile in this configuration uses:\noriginal\t%s\tPAT stored\n",
		renamed.Organization, renamed.CollectionURL, stranded.Service)
	if got, err := run("profile", "list"); err != nil || !strings.HasPrefix(got, want) {
		t.Fatalf("profile list = %q, %v; want prefix %q", got, err, want)
	}

	for _, args := range [][]string{
		{"profile", "remove"},
		{"profile", "remove", "--profile", "original", "--collection", server.URL + "/One", "--service", "tfsaz"},
		{"profile", "remove", "--profile", "original", "--service", "tfsaz/v2/unknown"},
		{"profile", "remove", "--profile", "original", "--service", "other"},
		{"profile", "remove", "--profile", "original", "--collection", "http://ado.example.invalid/One"},
	} {
		if _, err := run(args...); ExitCode(err) != 2 {
			t.Errorf("%v: expected a usage error, got %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"profile", "remove", "--profile", "original"},
		{"profile", "remove", "--profile", "renamed", "--collection", server.URL + "/One/"},
	} {
		if _, err := run(args...); err == nil || len(store.values) != 1 {
			t.Errorf("%v removed the entry of an unknown profile or a configured one: %v", args, err)
		}
	}
	if got, err := run("profile", "remove", "--profile", "original", "--service", stranded.Service); err != nil || got != "Removed keyring entry "+stranded.Service+" for original\n" {
		t.Fatalf("remove recorded entry = %q, %v", got, err)
	}
	if len(store.values) != 0 || readTestKeyringList(t, list) != nil {
		t.Fatal("removal kept the stranded entry or its name")
	}

	// An entry written before the list existed is found by ID and collection.
	unrecorded := Profile{ID: "old", CollectionURL: server.URL + "/Two", CredentialVersion: collectionCredentialVersion}
	if err := store.Set(credentialService(unrecorded), unrecorded.ID, rand.Text()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Removed", "No"} {
		if got, err := run("profile", "remove", "--profile", "old", "--collection", server.URL+"/Two/"); err != nil || !strings.HasPrefix(got, want+" keyring entry") {
			t.Fatalf("remove by collection = %q, %v", got, err)
		}
	}

	// A profile with a managed checkout stays until the checkout is restored,
	// also while a concurrent repo setup holds the registry.
	registry := setupRegistryForConfig(path)
	lock := flock.New(registry + ".lock")
	if locked, err := lock.TryLock(); err != nil || !locked {
		t.Fatalf("test registry lock: %v", err)
	}
	if _, err := run("profile", "remove", "--profile", "renamed"); err == nil || !strings.Contains(err.Error(), "repository setup transaction") {
		t.Fatalf("profile remove ignored a running repo setup: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	checkout := setupCheckout{repo: t.TempDir()}
	checkout.gitDir = filepath.Join(checkout.repo, ".git")
	record, err := makePreserveSetupRecord(cfg, checkout, renamed.CollectionURL+"/Project/_git/Repo", renamed.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Phase = setupPhaseActive
	if data, err = json.Marshal([]setupRecord{record}); err != nil {
		t.Fatal(err)
	}
	managerTestWrite(t, registry, string(data))
	if _, err := run("profile", "remove", "--profile", "renamed"); err == nil || !strings.Contains(err.Error(), "repo restore") {
		t.Fatalf("removed a profile with a managed checkout: %v", err)
	}
	if err := os.Remove(registry); err != nil {
		t.Fatal(err)
	}
	if got, err := run("profile", "remove", "--profile", "renamed"); err != nil || got != "Removed profile renamed\n" {
		t.Fatalf("remove = %q, %v", got, err)
	}
	if got, err := run("profile", "list"); err != nil || got != "No collection profiles configured\n" {
		t.Fatalf("after removing every profile: %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Dir(list)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an empty list left its directory: %v", err)
	}
}

func TestLegacyKeyringEntryStaysUntilExplicitRemoval(t *testing.T) {
	pat, legacyPAT := rand.Text(), rand.Text()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, sent, _ := r.BasicAuth(); sent != pat {
			http.Error(w, "invalid", http.StatusUnauthorized)
			return
		}
		serveTestf(w, `{"authenticatedUser":{"id":%q,"uniqueName":"fixture@example.invalid"}}`, repoTestViewerID)
	}))
	defer server.Close()
	legacy := clientTestProfile(t, server, "/Legacy")
	legacy.Organization = "https://legacy-example.visualstudio.com"
	moved := legacy
	moved.CredentialVersion = collectionCredentialVersion
	shared := profileEntry(legacy)
	for _, tc := range []struct {
		name    string
		args    []string
		version int  // credentialVersion of the profile afterwards; -1 when it is gone
		stored  bool // whether the per-collection entry holds the new PAT
	}{
		{"login", []string{"devops", "login", "--organization", legacy.Organization}, collectionCredentialVersion, true},
		{"logout", []string{"profile", "logout", "--profile", legacy.ID}, collectionCredentialVersion, false},
		{"remove", []string{"profile", "remove", "--profile", legacy.ID}, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setConfig(t, legacy)
			// The per-collection entry of an interrupted earlier login.
			movedKey := memoryCredentialKey(credentialService(moved), moved.ID)
			store := &memoryStore{values: map[string]string{legacy.ID: legacyPAT, movedKey: rand.Text()}}
			list := testKeyringList(t)
			var out, diag bytes.Buffer
			a := app{stdout: &out, stderr: &diag, stdin: profileTestInput(t, pat), store: store, keyringList: list}
			if err := a.run(t.Context(), []string{"profile", "remove", "--profile", legacy.ID, "--service", "tfsaz"}); err == nil {
				t.Fatal("removed the legacy entry while a legacy profile uses it")
			}
			if err := a.run(t.Context(), tc.args); err != nil {
				t.Fatal(err)
			}
			cfg, err := readConfigFile(os.Getenv("TFSAZ_CONFIG"))
			if err != nil || (tc.version < 0) != (len(cfg.Profiles) == 0) || (tc.version >= 0 && cfg.Profiles[0].CredentialVersion != tc.version) {
				t.Fatalf("profiles afterwards: %v, %v", cfg.Profiles, err)
			}
			wantList := []keyringEntry{shared}
			if tc.stored {
				wantList = append(wantList, profileEntry(moved))
			}
			if stored, ok := store.values[movedKey]; store.values[legacy.ID] != legacyPAT || (stored == pat) != tc.stored || ok != tc.stored ||
				!slices.Equal(readTestKeyringList(t, list), wantList) || !strings.Contains(diag.String(), "--service "+keyringService) {
				t.Fatalf("legacy entry not kept and shown: list %v, note %q", readTestKeyringList(t, list), diag.String())
			}
			out.Reset()
			if err := a.run(t.Context(), []string{"profile", "remove", "--profile", legacy.ID, "--service", "tfsaz"}); err != nil || !strings.HasPrefix(out.String(), "Removed") {
				t.Fatalf("explicit removal = %q, %v", out.String(), err)
			}
			if _, ok := store.values[legacy.ID]; ok || !slices.Equal(readTestKeyringList(t, list), wantList[1:]) {
				t.Fatal("explicit removal kept the legacy entry or its name")
			}
		})
	}
}
