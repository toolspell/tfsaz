package az

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/zalando/go-keyring"
	"golang.org/x/term"
)

const keyringService = "tfsaz"

var errNoCredential = keyring.ErrNotFound
var errStoreUnavailable = errors.New("credential store locked or unavailable")

type credentialStore interface {
	Get(profileID string) (string, error)
	Set(profileID, pat string) error
}

type systemStore struct{}

func (systemStore) Get(id string) (string, error) { return keyring.Get(keyringService, id) }
func (systemStore) Set(id, pat string) error      { return keyring.Set(keyringService, id, pat) }

func (a app) login(ctx context.Context, p Profile) error {
	var raw []byte
	var err error
	if term.IsTerminal(int(a.stdin.Fd())) {
		fmt.Fprintf(a.stderr, "PAT for %s: ", p.ID)
		raw, err = term.ReadPassword(int(a.stdin.Fd()))
		fmt.Fprintln(a.stderr)
	} else {
		raw, err = io.ReadAll(io.LimitReader(a.stdin, 1025))
	}
	if err != nil {
		return errors.New("could not read PAT from stdin")
	}
	defer clear(raw)
	pat := strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	if pat == "" || len(raw) > 1024 || strings.ContainsFunc(pat, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("expected one PAT on stdin")
	}
	client, err := newClient(p, pat)
	if err != nil {
		return err
	}
	if _, err := client.Identity(ctx); err != nil {
		return err
	}
	if err := a.store.Set(p.ID, pat); err != nil {
		return fmt.Errorf("%w for profile %s", errStoreUnavailable, p.ID)
	}
	return nil
}
