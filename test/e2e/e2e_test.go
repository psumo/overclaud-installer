//go:build e2e

// End-to-end test of the real binary on the current OS (CI runs it on Windows, macOS and
// Linux): fresh install with real prerequisite checks, doctor, a customer edit kept across
// an update, a downgrade refused, and a broken update rolled back.
//
//	go test -tags e2e ./test/e2e -v -timeout 40m
package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/psumo/overclaud-installer/internal/manifest"
)

const key = "GC-TEST-0000-0000"

var files = map[string]string{
	".claude/settings.json": `{"effortLevel":"medium","hooks":{` +
		`"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"python \"$HOME/.claude/hooks/scripts/pre-tool-use.py\""}]}],` +
		`"UserPromptSubmit":[{"hooks":[{"type":"command","command":"python \"$HOME/.claude/hooks/scripts/friction-sense.py\""}]}]}}`,
	".claude/hooks/scripts/pre-tool-use.py": "import json, re, sys\nevent = json.load(sys.stdin)\n" +
		"cmd = (event.get('tool_input') or {}).get('command', '')\n" +
		"if re.search(r'(curl|wget)[^|]*\\|\\s*(ba)?sh', cmd):\n    sys.exit(2)\n",
	".claude/hooks/scripts/friction-sense.py": "import sys\nsys.stdin.read()\n",
	".claude/rules/common/common-testing.md":  "# Testing\n",
	".claude/rules/common/common-patterns.md": "# Patterns\n",
	".claude/skills/hello/SKILL.md":           "---\nname: hello\ndescription: test\n---\nhi\n",
	"FIRST-RUN.md":                            "# First run\n",
	"sync/setup-prerequisites.sh":             "#!/usr/bin/env bash\necho none\n",
	"sync/setup-prerequisites.ps1":            "Write-Host none\n",
}

func release(t *testing.T, priv ed25519.PrivateKey, version string, change func(map[string]string)) []byte {
	t.Helper()
	fs := map[string]string{}
	for k, v := range files {
		fs[k] = v
	}
	if change != nil {
		change(fs)
	}
	dir := t.TempDir()
	for rel, body := range fs {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	m, err := manifest.Build(dir, version)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := m.Marshal()
	fs[manifest.FileName] = string(data)
	fs[manifest.SigName] = hex.EncodeToString(manifest.Sign(data, priv)) + "\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for rel, body := range fs {
		tw.WriteHeader(&tar.Header{Name: "psumo-overclaud-x/" + rel, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type server struct {
	mu      sync.Mutex
	sha     string
	tarball []byte
}

func (s *server) set(sha string, tb []byte) {
	s.mu.Lock()
	s.sha, s.tarball = sha, tb
	s.mu.Unlock()
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.URL.Path {
	case "/api/download":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["key"] != key || body["deviceId"] == "" {
			json.NewEncoder(w).Encode(map[string]any{"valid": false, "message": "License not active."})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"valid": true, "url": "http://" + r.Host + "/api/fetch?t=x", "sha": s.sha, "email": "ci@example.com", "plan": "lifetime"})
	case "/api/fetch":
		w.Write(s.tarball)
	default:
		http.NotFound(w, r)
	}
}

func TestInstallUpdateRollback(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	root, _ := filepath.Abs("../..")
	bin := filepath.Join(t.TempDir(), "overclaud")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "-ldflags",
		"-X github.com/psumo/overclaud-installer/internal/app.ReleasePublicKey="+hex.EncodeToString(pub)+
			" -X github.com/psumo/overclaud-installer/internal/app.Version=e2e", "./cmd/overclaud")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	srv := &server{}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	env := append(os.Environ(), "HOME="+home, "USERPROFILE="+home, "OVERCLAUD_API="+ts.URL+"/api", "NO_COLOR=1")
	run := func(args ...string) (string, error) {
		c := exec.Command(bin, args...)
		c.Env = env
		c.Stdin = strings.NewReader("")
		out, err := c.CombinedOutput()
		t.Logf("$ overclaud %s\n%s", strings.Join(args, " "), out)
		return string(out), err
	}
	read := func(rel string) string {
		b, _ := os.ReadFile(filepath.Join(claude, filepath.FromSlash(rel)))
		return string(b)
	}

	srv.set("r1", release(t, priv, "9.0.1", nil))
	if _, err := run("install", "--key", key, "--no-extras"); err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if read("skills/hello/SKILL.md") == "" || read("settings.json") == "" {
		t.Fatal("product files not installed")
	}
	if _, err := os.Stat(filepath.Join(home, "overclaud", "FIRST-RUN.md")); err != nil {
		t.Fatal("~/overclaud not in place")
	}
	if _, err := run("doctor"); err != nil {
		t.Fatal("doctor failed after install")
	}

	// A customer edit survives; an untouched file updates.
	os.WriteFile(filepath.Join(claude, "rules/common/common-testing.md"), []byte("my own rule\n"), 0o644)
	srv.set("r2", release(t, priv, "9.0.2", func(f map[string]string) {
		f[".claude/rules/common/common-testing.md"] += "v2\n"
		f[".claude/rules/common/common-patterns.md"] += "v2\n"
	}))
	if _, err := run("update"); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if read("rules/common/common-testing.md") != "my own rule\n" || !strings.Contains(read("rules/common/common-testing.md.overclaud-new"), "v2") {
		t.Fatal("customer edit not kept with the new version beside it")
	}
	if !strings.Contains(read("rules/common/common-patterns.md"), "v2") {
		t.Fatal("untouched file not updated")
	}

	// An older signed release is refused.
	srv.set("r1", release(t, priv, "9.0.1", nil))
	if out, err := run("update"); err == nil || !strings.Contains(out, "downgrade") {
		t.Fatal("downgrade accepted")
	}

	// A release whose safety hook does not block fails doctor and is rolled back.
	guard := read("hooks/scripts/pre-tool-use.py")
	srv.set("r3", release(t, priv, "9.0.3", func(f map[string]string) {
		f[".claude/hooks/scripts/pre-tool-use.py"] = "import sys\nsys.stdin.read()\n"
		f[".claude/rules/common/common-patterns.md"] += "v2\nv3\n"
	}))
	if _, err := run("update"); err == nil {
		t.Fatal("broken update accepted")
	}
	if read("hooks/scripts/pre-tool-use.py") != guard || strings.Contains(read("rules/common/common-patterns.md"), "v3") {
		t.Fatal("rollback did not restore the previous release")
	}

	// A release signed by another key is refused.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	srv.set("r4", release(t, other, "9.0.4", nil))
	if out, err := run("update"); err == nil || !strings.Contains(out, "signature") {
		t.Fatal("release with a foreign signature accepted")
	}
	if _, err := run("doctor"); err != nil {
		t.Fatal("doctor failed at the end")
	}
}
