package payload

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/psumo/overclaud-installer/internal/manifest"
)

type entry struct {
	name, body string
	typ        byte
	link       string
	mode       int64
}

func tarball(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Size: int64(len(e.body)), Linkname: e.link}
		if typ != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractStripsTopDirAndKeepsExecBit(t *testing.T) {
	data := tarball(t, []entry{
		{name: "psumo-overclaud-abc/", typ: tar.TypeDir},
		{name: "psumo-overclaud-abc/.claude/a.md", body: "a"},
		{name: "psumo-overclaud-abc/sync/run.sh", body: "#!/bin/sh", mode: 0o755},
	})
	dir := t.TempDir()
	if err := Extract(bytes.NewReader(data), dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".claude/a.md")); string(b) != "a" {
		t.Fatal("file not extracted with top dir stripped")
	}
	info, _ := os.Stat(filepath.Join(dir, "sync/run.sh"))
	if runtime.GOOS != "windows" && (info == nil || info.Mode()&0o100 == 0) {
		t.Fatal("exec bit lost")
	}
}

func TestExtractRefusesTraversalAndLinks(t *testing.T) {
	for _, bad := range []entry{
		{name: "top/../../evil", body: "x"},
		{name: "top/a/../../../evil", body: "x"},
		{name: "top/link", typ: tar.TypeSymlink, link: "/etc/passwd"},
		{name: "top/hard", typ: tar.TypeLink, link: "top/a"},
	} {
		dir := t.TempDir()
		err := Extract(bytes.NewReader(tarball(t, []entry{bad})), dir)
		if err == nil {
			t.Fatalf("%s accepted", bad.name)
		}
	}
}

func signedRelease(t *testing.T, priv ed25519.PrivateKey, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	m, _ := manifest.Build(dir, "1.4.0")
	data, _ := m.Marshal()
	os.MkdirAll(filepath.Join(dir, "sync"), 0o755)
	os.WriteFile(filepath.Join(dir, manifest.FileName), data, 0o644)
	os.WriteFile(filepath.Join(dir, manifest.SigName), []byte(hex.EncodeToString(manifest.Sign(data, priv))+"\n"), 0o644)
	return dir
}

func TestVerifyAcceptsSignedReleaseAndRejectsTampering(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := signedRelease(t, priv, map[string]string{".claude/a.md": "a", "README.md": "r"})
	m, err := Verify(dir, pub)
	if err != nil {
		t.Fatalf("good release rejected: %v", err)
	}
	if m.Version != "1.4.0" {
		t.Fatalf("version %q", m.Version)
	}
	os.WriteFile(filepath.Join(dir, ".claude/a.md"), []byte("evil"), 0o644)
	if _, err := Verify(dir, pub); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("tampered file accepted: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	dir2 := signedRelease(t, priv, map[string]string{".claude/a.md": "a"})
	if _, err := Verify(dir2, other); err == nil {
		t.Fatal("release signed by another key accepted")
	}
}

func TestVerifyRejectsUnsignedRelease(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("r"), 0o644)
	if _, err := Verify(dir, pub); err == nil {
		t.Fatal("unsigned release accepted")
	}
}

func TestFetchWritesBodyAndFailsOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			http.Error(w, "expired", http.StatusForbidden)
			return
		}
		w.Write([]byte("payload"))
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "r.tgz")
	if err := Fetch(srv.URL+"/ok", dst, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "payload" {
		t.Fatal("body not written")
	}
	if err := Fetch(srv.URL+"/bad", dst, nil); err == nil {
		t.Fatal("HTTP 403 not reported")
	}
}

func TestExtractRefusesGitDirsAndOversizedArchives(t *testing.T) {
	for _, name := range []string{"top/.git/config", "top/sub/.GIT/hooks/x", "top/.git./config", "top/.git /config", "top/GIT~1/config"} {
		if err := Extract(bytes.NewReader(tarball(t, []entry{{name: name, body: "x"}})), t.TempDir()); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	old := MaxExtract
	MaxExtract = 10
	defer func() { MaxExtract = old }()
	big := tarball(t, []entry{{name: "top/a", body: "123456"}, {name: "top/b", body: "123456"}})
	if err := Extract(bytes.NewReader(big), t.TempDir()); err == nil {
		t.Fatal("archive over the size cap accepted")
	}
}

func TestFetchErrorNeverShowsTheDownloadToken(t *testing.T) {
	err := Fetch("http://127.0.0.1:1/api/fetch?t=SECRET-TOKEN", filepath.Join(t.TempDir(), "x"), nil)
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("token leaked or no error: %v", err)
	}
}
