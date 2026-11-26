package az

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
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

// processStore is the store of a running binary. Test binaries replace it
// (credentials_testbinary.go).
var processStore credentialStore = systemStore{}

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

func (a app) profileCredential(p Profile) (string, error) {
	return a.store.Get(credentialService(p), p.ID)
}

func (a app) storeProfileCredential(p Profile, pat string) error {
	if err := a.store.Set(credentialService(p), p.ID, pat); err != nil {
		return fmt.Errorf("%w for profile %s", errStoreUnavailable, p.ID)
	}
	return nil
}

func (a app) login(ctx context.Context, p Profile) error {
	pat, err := a.verifyLogin(ctx, p)
	if err != nil {
		return err
	}
	return a.storeProfileCredential(p, pat)
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
	// The PAT is valid either way, so the caller still saves the profile.
	if _, err := checkAPIFloor(ctx, client); err != nil {
		_, _ = fmt.Fprintf(a.stderr, "WARNING: %v\n", err)
	}
	return pat, nil
}
