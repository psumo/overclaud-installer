package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recRunner records claude CLI calls and answers the list commands.
type recRunner struct {
	calls   [][]string
	markets string
	plugins string
}

func (r *recRunner) LookPath(name string) (string, error) { return "/bin/" + name, nil }
func (r *recRunner) Output(name string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	joined := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(joined, "plugin marketplace list"):
		return r.markets, nil
	case strings.HasPrefix(joined, "plugin list"):
		return r.plugins, nil
	}
	return "", nil
}
func (r *recRunner) Interactive(string, ...string) error { return nil }

func (r *recRunner) ran(prefix ...string) []string {
	for _, c := range r.calls {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], "\x00") == strings.Join(prefix, "\x00") {
			return c
		}
	}
	return nil
}

const setupJSON = `{
  "servers": {
    "github": {"type": "http", "url": "https://api.githubcopilot.com/mcp/", "headersHelper": "\"{overclaud}\" keys header github"},
    "claude-flow": {"type": "stdio", "command": "npx", "args": ["-y", "claude-flow@3.59.17", "mcp", "start"]},
    "fresh": {"type": "stdio", "command": "x"}
  },
  "replace_if_contains": ["github-mcp-server"],
  "plugins": [{"marketplace": "thedotmack", "source": "https://github.com/thedotmack/claude-mem.git#v13.24.23", "id": "claude-mem@thedotmack", "commit": "ed57a511f5dbf84e75c9a785df818c43b66b5849"}]
}`

func setupFixture(t *testing.T) (*App, *recRunner, string) {
	t.Helper()
	a, _ := testApp(t, "")
	release := t.TempDir()
	if err := os.MkdirAll(filepath.Join(release, "sync"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "sync", "mcp-servers.json"), []byte(setupJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeJSON := `{"mcpServers": {
	  "github": {"command": "npx", "args": ["--yes", "-p", "github-mcp-server", "github-mcp-server-mcp"]},
	  "claude-flow": {"command": "my-own-flow"}
	}}`
	if err := os.WriteFile(filepath.Join(a.Paths.Home, ".claude.json"), []byte(claudeJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &recRunner{markets: `[]`, plugins: `[]`}
	a.Runner = r
	return a, r, release
}

func TestSetupClaudeServers(t *testing.T) {
	a, r, release := setupFixture(t)
	bin := `C:\Users\Sam Smith\AppData\Local\Overclaud\overclaud.exe`
	a.SetupClaude(release, bin)

	if r.ran("claude", "mcp", "remove", "-s", "user", "github") == nil {
		t.Fatal("the unsafe github server was not removed")
	}
	add := r.ran("claude", "mcp", "add-json", "-s", "user", "github")
	if add == nil {
		t.Fatal("github was not re-added")
	}
	var cfg map[string]string
	if err := json.Unmarshal([]byte(add[len(add)-1]), &cfg); err != nil {
		t.Fatalf("github config is not valid JSON after path substitution: %v\n%s", err, add[len(add)-1])
	}
	if cfg["headersHelper"] != `"`+bin+`" keys header github` {
		t.Fatalf("headersHelper = %q", cfg["headersHelper"])
	}
	if r.ran("claude", "mcp", "add-json", "-s", "user", "claude-flow") != nil || r.ran("claude", "mcp", "remove", "-s", "user", "claude-flow") != nil {
		t.Fatal("a customer's own claude-flow config must be kept")
	}
	if r.ran("claude", "mcp", "add-json", "-s", "user", "fresh") == nil {
		t.Fatal("a new server was not added")
	}
}

func writeInstalled(t *testing.T, a *App, sha string) {
	t.Helper()
	dir := filepath.Join(a.Paths.ClaudeDir, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := `{"version": 2, "plugins": {"claude-mem@thedotmack": [{"scope": "user", "gitCommitSha": "` + sha + `"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "installed_plugins.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPluginWithWrongCommitIsRemoved(t *testing.T) {
	a, r, release := setupFixture(t)
	writeInstalled(t, a, "0000000000000000000000000000000000000000") // the tag was moved
	a.SetupClaude(release, "/bin/overclaud")
	if r.ran("claude", "plugin", "uninstall", "claude-mem@thedotmack") == nil {
		t.Fatal("a plugin from an unreviewed commit must be removed")
	}
}

func TestSetupClaudePlugins(t *testing.T) {
	a, r, release := setupFixture(t)
	writeInstalled(t, a, "ed57a511f5dbf84e75c9a785df818c43b66b5849")
	a.SetupClaude(release, "/bin/overclaud")
	if r.ran("claude", "plugin", "uninstall") != nil {
		t.Fatal("the reviewed commit must be kept")
	}
	if r.ran("claude", "plugin", "marketplace", "add", "https://github.com/thedotmack/claude-mem.git#v13.24.23") == nil {
		t.Fatal("pinned marketplace not added")
	}
	if r.ran("claude", "plugin", "install", "claude-mem@thedotmack") == nil {
		t.Fatal("plugin not installed")
	}

	a2, r2, release2 := setupFixture(t)
	r2.markets = `[{"name": "thedotmack"}]`
	r2.plugins = `[{"id": "claude-mem@thedotmack"}]`
	a2.SetupClaude(release2, "/bin/overclaud")
	if r2.ran("claude", "plugin", "install") != nil || r2.ran("claude", "plugin", "marketplace", "add") != nil {
		t.Fatal("an installed plugin must not be reinstalled")
	}
}

func TestLegacyMCPCleaned(t *testing.T) {
	a, _, release := setupFixture(t)
	legacy := filepath.Join(a.Paths.Home, ".mcp.json")
	orig := `{"mcpServers": {"github": {"args": ["github-mcp-server"]}, "mine": {"command": "keep-me"}}, "other": 1}`
	if err := os.WriteFile(legacy, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	a.SetupClaude(release, "/bin/overclaud")
	b, _ := os.ReadFile(legacy)
	if strings.Contains(string(b), "github-mcp-server") || !strings.Contains(string(b), "keep-me") || !strings.Contains(string(b), `"other"`) {
		t.Fatalf("legacy file not cleaned correctly:\n%s", b)
	}
	backups, _ := filepath.Glob(legacy + ".bak-*")
	if len(backups) != 1 {
		t.Fatalf("want one backup, got %v", backups)
	}
	if bb, _ := os.ReadFile(backups[0]); string(bb) != orig {
		t.Fatal("backup does not match the original")
	}
}

func TestSetupClaudeWithoutFileIsNoop(t *testing.T) {
	a, _ := testApp(t, "")
	r := &recRunner{}
	a.Runner = r
	a.SetupClaude(t.TempDir(), "/bin/overclaud")
	if len(r.calls) != 0 {
		t.Fatalf("an older release must not trigger setup: %v", r.calls)
	}
}
