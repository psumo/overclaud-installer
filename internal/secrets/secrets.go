// Package secrets keeps the customer's API keys in the operating system's key store:
// Windows Credential Manager, macOS Keychain, or the Linux Secret Service. A Linux machine
// without a working Secret Service (a headless server) gets a private directory instead:
// mode 0700, one 0600 file per key, written atomically.
//
// Keys are never written to settings files, logs or Overclaud's servers. Callers fetch a
// key only when a process needs it (see the `with-keys` and `keys session-env` commands).
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zalando/go-keyring"
)

// ErrNotFound means no key is stored under that id.
var ErrNotFound = errors.New("not stored")

// keyringService is the service name the OS key store files our entries under.
const keyringService = "overclaud"

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// Store holds keys by service id.
type Store interface {
	Get(id string) (string, error)
	Set(id, value string) error
	Delete(id string) error
	// Where describes the backing store for messages ("macOS Keychain", a directory, ...).
	Where() string
}

// Open returns the OS key store when it works on this machine, else the file store under
// home/.overclaud/secrets. The choice is probed once and remembered in
// home/.overclaud/secrets-backend, so keys stay where they were saved and session start
// never waits on a key store probe. OVERCLAUD_SECRETS=file forces the file store.
func Open(home string) Store {
	fs := FileStore{Dir: filepath.Join(home, ".overclaud", "secrets")}
	if os.Getenv("OVERCLAUD_SECRETS") == "file" {
		return fs
	}
	marker := filepath.Join(home, ".overclaud", "secrets-backend")
	if b, err := os.ReadFile(marker); err == nil {
		if strings.TrimSpace(string(b)) == "keyring" {
			return keyringStore{}
		}
		return fs
	}
	choice, store := "file", Store(fs)
	if keyringWorks(3 * time.Second) {
		choice, store = "keyring", keyringStore{}
	}
	if os.MkdirAll(filepath.Dir(marker), 0o700) == nil {
		_ = os.WriteFile(marker, []byte(choice+"\n"), 0o600)
	}
	return store
}

func checkID(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid key id %q", id)
	}
	return nil
}

// keyringWorks round-trips a probe entry. A Secret Service that is missing or locked can
// block on D-Bus, so the probe runs with a deadline.
func keyringWorks(timeout time.Duration) bool {
	done := make(chan bool, 1)
	go func() {
		const probe = "_probe"
		if keyring.Set(keyringService, probe, "ok") != nil {
			done <- false
			return
		}
		v, err := keyring.Get(keyringService, probe)
		_ = keyring.Delete(keyringService, probe)
		done <- err == nil && v == "ok"
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(timeout):
		return false
	}
}

type keyringStore struct{}

func (keyringStore) Get(id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	v, err := keyring.Get(keyringService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (keyringStore) Set(id, value string) error {
	if err := checkID(id); err != nil {
		return err
	}
	return keyring.Set(keyringService, id, value)
}

func (keyringStore) Delete(id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	err := keyring.Delete(keyringService, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

func (keyringStore) Where() string {
	switch {
	case isWindows():
		return "Windows Credential Manager"
	case isMac():
		return "macOS Keychain"
	default:
		return "the system key store (Secret Service)"
	}
}

// FileStore is the fallback: one private file per key.
type FileStore struct{ Dir string }

func (f FileStore) path(id string) string { return filepath.Join(f.Dir, id) }

func (f FileStore) Get(id string) (string, error) {
	if err := checkID(id); err != nil {
		return "", err
	}
	b, err := os.ReadFile(f.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	return strings.TrimSpace(string(b)), err
}

func (f FileStore) Set(id, value string) error {
	if err := checkID(id); err != nil {
		return err
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(f.Dir, 0o700)
	tmp, err := os.CreateTemp(f.Dir, "."+id+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path(id))
}

func (f FileStore) Delete(id string) error {
	if err := checkID(id); err != nil {
		return err
	}
	err := os.Remove(f.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (f FileStore) Where() string { return f.Dir + " (private files)" }
