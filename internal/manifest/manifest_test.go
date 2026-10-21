package manifest

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestBuildListsFilesSortedAndSkipsGit(t *testing.T) {
	root := writeTree(t, map[string]string{
		".claude/skills/b/SKILL.md": "b",
		".claude/skills/a/SKILL.md": "a",
		".git/config":               "secret",
		"README.md":                 "hi",
	})
	m, err := Build(root, "1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, f := range m.Files {
		got = append(got, f.Path)
	}
	want := []string{".claude/skills/a/SKILL.md", ".claude/skills/b/SKILL.md", "README.md"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if m.Version != "1.4.0" {
		t.Fatalf("version %q", m.Version)
	}
}

func TestSignVerifyRoundTripAndTamper(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	root := writeTree(t, map[string]string{".claude/x.md": "x"})
	m, _ := Build(root, "1.4.0")
	data, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	sig := Sign(data, priv)
	if !VerifySignature(data, sig, pub) {
		t.Fatal("valid signature rejected")
	}
	tampered := append([]byte{}, data...)
	tampered[len(tampered)-3] ^= 1
	if VerifySignature(tampered, sig, pub) {
		t.Fatal("tampered manifest accepted")
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if VerifySignature(data, sig, otherPub) {
		t.Fatal("signature accepted under the wrong key")
	}
}

func TestVerifyTreeFindsChangedMissingAndExtraFiles(t *testing.T) {
	root := writeTree(t, map[string]string{".claude/a.md": "a", ".claude/b.md": "b"})
	m, _ := Build(root, "1")
	os.WriteFile(filepath.Join(root, ".claude/a.md"), []byte("changed"), 0o644)
	os.Remove(filepath.Join(root, ".claude/b.md"))
	os.WriteFile(filepath.Join(root, ".claude/extra.md"), []byte("x"), 0o644)
	problems := VerifyTree(root, m)
	if len(problems) != 3 {
		t.Fatalf("want 3 problems (changed, missing, unlisted), got %v", problems)
	}
}

func TestParseRejectsPathsOutsideTheTree(t *testing.T) {
	for _, bad := range []string{"../evil", "/etc/passwd", ".claude/../../x", `C:\x`} {
		data := []byte(`{"version":"1","files":[{"path":"` + jsonEscape(bad) + `","sha256":"00","size":0}]}`)
		if _, err := Parse(data); err == nil {
			t.Fatalf("path %q accepted", bad)
		}
	}
}

func jsonEscape(s string) string {
	out := ""
	for _, r := range s {
		if r == '\\' || r == '"' {
			out += `\`
		}
		out += string(r)
	}
	return out
}
