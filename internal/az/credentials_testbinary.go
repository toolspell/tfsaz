//go:build tfsaztest

package az

import "errors"

// Tests that run a built binary build it with -tags tfsaztest, so the binary
// never reads or writes the developer's OS keyring or its keyring entry list.
func init() {
	processStore = testBinaryStore{}
	processKeyringList = func() (string, error) { return "", errKeyringListUnknown }
}

type testBinaryStore struct{}

func (testBinaryStore) Get(string, string) (string, error) { return "", errNoCredential }
func (testBinaryStore) Set(string, string, string) error {
	return errors.New("test binaries have no credential store")
}
func (testBinaryStore) Delete(string, string) error { return errNoCredential }
