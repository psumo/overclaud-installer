package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileStoreRoundTrip(t *testing.T) {
	s := FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	if _, err := s.Get("exa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty store: want ErrNotFound, got %v", err)
	}
	if err := s.Set("exa", "sk-test-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("exa", "sk-test-2"); err != nil { // overwrite
		t.Fatal(err)
	}
	v, err := s.Get("exa")
	if err != nil || v != "sk-test-2" {
		t.Fatalf("got %q, %v", v, err)
	}
	if err := s.Delete("exa"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("exa"); err != nil {
		t.Fatalf("deleting a missing key should be fine: %v", err)
	}
	if _, err := s.Get("exa"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: want ErrNotFound, got %v", err)
	}
}

func TestFileStorePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not meaningful on Windows")
	}
	s := FileStore{Dir: filepath.Join(t.TempDir(), "secrets")}
	if err := s.Set("gemini", "k"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(s.Dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v, want 0700", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(s.path("gemini")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, want 0600", fi.Mode().Perm())
	}
	left, _ := filepath.Glob(filepath.Join(s.Dir, ".*"))
	if len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestIDsAreValidated(t *testing.T) {
	s := FileStore{Dir: t.TempDir()}
	for _, id := range []string{"../escape", "a/b", "", "UPPER", ".hidden"} {
		if err := s.Set(id, "x"); err == nil {
			t.Fatalf("id %q accepted", id)
		}
	}
}

func TestOpenHonoursFileOverride(t *testing.T) {
	t.Setenv("OVERCLAUD_SECRETS", "file")
	home := t.TempDir()
	s := Open(home)
	if _, ok := s.(FileStore); !ok {
		t.Fatalf("OVERCLAUD_SECRETS=file should give the file store, got %T", s)
	}
	if s.(FileStore).Dir != filepath.Join(home, ".overclaud", "secrets") {
		t.Fatalf("unexpected dir %s", s.(FileStore).Dir)
	}
}

func TestOpenRemembersBackend(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".overclaud"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".overclaud", "secrets-backend"), []byte("file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Open(home).(FileStore); !ok {
		t.Fatal("a remembered file backend must be reused without probing")
	}
}
