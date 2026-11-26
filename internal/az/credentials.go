package az

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const (
	keyringService              = "tfsaz"
	collectionCredentialVersion = 2
)

var errNoCredential = keyring.ErrNotFound
var errStoreUnavailable = errors.New("credential store locked or unavailable")

type credentialStore interface {
	Get(service, profileID string) (string, error)
	Set(service, profileID, pat string) error
	Delete(service, profileID string) error
}

type systemStore struct{}

func (systemStore) Get(service, id string) (string, error) { return keyring.Get(service, id) }
func (systemStore) Set(service, id, pat string) error      { return keyring.Set(service, id, pat) }
func (systemStore) Delete(service, id string) error        { return keyring.Delete(service, id) }

func credentialService(p Profile) string {
	if p.CredentialVersion == collectionCredentialVersion {
		// The profile ID alone may be reused in a different config file.
		return fmt.Sprintf("%s/v2/%x", keyringService, sha256.Sum256([]byte(p.CollectionURL)))
	}
	return keyringService
}

// The services TFSaz writes PATs under: the legacy service and one per collection.
var credentialServiceName = regexp.MustCompile("^" + keyringService + `(?:/v2/[0-9a-f]{64})?$`)

// keyringEntry names an OS keyring entry. It never holds the secret.
type keyringEntry struct {
	Service string `json:"service"`
	Account string `json:"account"`
}

func profileEntry(p Profile) keyringEntry { return keyringEntry{credentialService(p), p.ID} }

func (a app) profileCredential(p Profile) (string, error) {
	return a.store.Get(credentialService(p), p.ID)
}

// credentialState reads an entry only to report whether it holds a PAT.
func (a app) credentialState(e keyringEntry) string {
	_, err := a.store.Get(e.Service, e.Account)
	switch {
	case err == nil:
		return "PAT stored"
	case errors.Is(err, errNoCredential):
		return "no PAT"
	default:
		return "keyring unavailable"
	}
}

// go-keyring cannot enumerate entries, so TFSaz lists the names it writes. The
// list lives outside the configuration directory and survives deleting it.
func keyringListPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".tfsaz", "keyring-entries.json"), nil
}

var errKeyringListUnknown = errors.New("could not locate the keyring entry list in the home directory")

func readKeyringList(path string) ([]keyringEntry, error) {
	if path == "" {
		return nil, errKeyringListUnknown
	}
	data, err := readSetupFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read keyring entry list: %w", err)
	}
	invalid := fmt.Errorf("invalid keyring entry list %s", path)
	var entries []keyringEntry
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("[")) || json.Unmarshal(data, &entries) != nil {
		return nil, invalid
	}
	for i, e := range entries {
		if e.Service == "" || e.Account == "" || slices.Contains(entries[:i], e) {
			return nil, invalid
		}
	}
	return entries, nil
}

type keyringList struct {
	path    string
	entries []keyringEntry
}

// withKeyringList serializes list updates across processes and configurations.
func (a app) withKeyringList(run func(*keyringList) error) (err error) {
	if a.keyringList == "" {
		return errKeyringListUnknown
	}
	dir := filepath.Dir(a.keyringList)
	// Runs after the unlock below. The directory stays while it holds the list
	// or route recovery files, so the removal error carries no information.
	defer func() { _ = os.Remove(dir) }()
	unlock, err := acquireLockInDir(dir, a.keyringList+".lock", errors.New("another TFSaz command is updating the keyring entry list; retry"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	entries, err := readKeyringList(a.keyringList)
	if err != nil {
		return err
	}
	return run(&keyringList{path: a.keyringList, entries: entries})
}

func (l *keyringList) save(entries []keyringEntry) error {
	if len(entries) == 0 {
		if err := removeIfPresent(l.path); err != nil {
			return err
		}
	} else {
		data, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return err
		}
		exists, err := setupFileExists(l.path)
		if err != nil {
			return err
		}
		if err := publishSetupFile(l.path, append(data, '\n'), exists); err != nil {
			return fmt.Errorf("save keyring entry list: %w", err)
		}
	}
	l.entries = entries
	return nil
}

func (l *keyringList) add(e keyringEntry) error {
	if slices.Contains(l.entries, e) {
		return nil
	}
	return l.save(append(slices.Clone(l.entries), e))
}

func (l *keyringList) remove(e keyringEntry) error {
	i := slices.Index(l.entries, e)
	if i < 0 {
		return nil
	}
	return l.save(slices.Delete(slices.Clone(l.entries), i, i+1))
}

// setCredential records the name before it writes the secret, so an
// interrupted write cannot leave an entry the list does not show.
func (a app) setCredential(list *keyringList, e keyringEntry, pat string) error {
	if err := list.add(e); err != nil {
		return err
	}
	if err := a.store.Set(e.Service, e.Account, pat); err != nil {
		return fmt.Errorf("%w for profile %s", errStoreUnavailable, e.Account)
	}
	return nil
}

// deleteCredential reports whether the keyring held the entry.
func (a app) deleteCredential(list *keyringList, e keyringEntry) (bool, error) {
	err := a.store.Delete(e.Service, e.Account)
	if err != nil && !errors.Is(err, errNoCredential) {
		return false, fmt.Errorf("%w for profile %s", errStoreUnavailable, e.Account)
	}
	return err == nil, list.remove(e)
}

func (a app) login(ctx context.Context, p Profile) error {
	pat, err := a.verifyLogin(ctx, p)
	if err != nil {
		return err
	}
	return withProfileConfigLock(false, func(path, target string) error {
		return a.saveLogin(path, target, p, pat)
	})
}

// saveLogin stores a verified PAT while the caller holds the configuration
// lock. A legacy profile moves to its per-collection service.
func (a app) saveLogin(path, target string, p Profile, pat string) error {
	original, err := readConfigFile(target)
	if err != nil {
		return err
	}
	cfg, err := validateConfig(original, path)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(cfg.Profiles, func(c Profile) bool { return c.ID == p.ID && c.CollectionURL == p.CollectionURL })
	if i < 0 {
		return fmt.Errorf("profile %s changed during login; retry", p.ID)
	}
	p = cfg.Profiles[i]
	return a.withKeyringList(func(list *keyringList) error {
		kept, err := a.keepLegacyEntry(list, p)
		if err != nil {
			return err
		}
		p.CredentialVersion = collectionCredentialVersion
		if err := a.setCredential(list, profileEntry(p), pat); err != nil {
			return err
		}
		if original.Profiles[i].CredentialVersion != collectionCredentialVersion {
			original.Profiles[i].CredentialVersion = collectionCredentialVersion
			if err := saveConfigFile(target, original, true); err != nil {
				return err
			}
		}
		return a.legacyEntryNote(kept, p.ID)
	})
}

// keepLegacyEntry records the shared legacy entry of a legacy profile that
// moves off it, so profile list shows the entry once no profile uses it.
// TFSaz deletes it only on explicit request, because another configuration
// may still use it.
func (a app) keepLegacyEntry(list *keyringList, p Profile) (bool, error) {
	if p.CredentialVersion == collectionCredentialVersion {
		return false, nil
	}
	e := profileEntry(p)
	if _, err := a.store.Get(e.Service, e.Account); errors.Is(err, errNoCredential) {
		return false, nil
	}
	return true, list.add(e)
}

// releaseCredential deletes the PAT of profile p and reports whether it kept
// a legacy entry. A legacy profile loses the per-collection entry it moves to,
// which an interrupted login may have written.
func (a app) releaseCredential(p Profile) (bool, error) {
	var kept bool
	err := a.withKeyringList(func(list *keyringList) (err error) {
		if kept, err = a.keepLegacyEntry(list, p); err != nil {
			return err
		}
		p.CredentialVersion = collectionCredentialVersion
		_, err = a.deleteCredential(list, profileEntry(p))
		return err
	})
	return kept, err
}

func (a app) legacyEntryNote(kept bool, id string) error {
	if !kept {
		return nil
	}
	_, err := fmt.Fprintf(a.stderr, "The legacy keyring entry of profile %s stays because another configuration may use it. tfsaz profile list shows it, and tfsaz profile remove --profile %s --service %s deletes it.\n", id, id, keyringService)
	return err
}

func (a app) stdinIsTerminal() bool {
	return a.stdin != nil && term.IsTerminal(int(a.stdin.Fd()))
}

func (a app) verifyLogin(ctx context.Context, p Profile) (string, error) {
	if a.stdin == nil {
		return "", errors.New("could not read PAT from stdin")
	}
	var raw []byte
	var err error
	if a.stdinIsTerminal() {
		// The prompt is a courtesy; ReadPassword reports a broken terminal.
		_, _ = fmt.Fprintf(a.stderr, "PAT for %s: ", p.ID)
		raw, err = term.ReadPassword(int(a.stdin.Fd()))
		_, _ = fmt.Fprintln(a.stderr)
	} else {
		raw, err = io.ReadAll(io.LimitReader(a.stdin, 1025))
	}
	if err != nil {
		return "", errors.New("could not read PAT from stdin")
	}
	defer clear(raw)
	pat := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if pat == "" || len(raw) > 1024 || strings.ContainsFunc(pat, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", errors.New("expected one PAT on stdin")
	}
	client, err := newClient(p, pat)
	if err != nil {
		return "", err
	}
	defer client.http.CloseIdleConnections()
	if _, err := client.Identity(ctx); err != nil {
		return "", err
	}
	return pat, nil
}
