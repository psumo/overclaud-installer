package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// claudeSetup is the release's sync/mcp-servers.json: the MCP servers and plugins an
// Overclaud install registers with Claude Code. It ships with each release, so changing a
// server needs a release, not a new installer.
type claudeSetup struct {
	// Servers are passed to `claude mcp add-json -s user`, with "{overclaud}" replaced by
	// this installer's path (for headersHelper).
	Servers map[string]json.RawMessage `json:"servers"`
	// Windows replaces a server's config on Windows (npx needs `cmd /c` there).
	Windows map[string]json.RawMessage `json:"windows"`
	// ReplaceIfContains: an existing user server whose config contains one of these
	// strings is replaced even though the customer may have set it (known-unsafe entries).
	ReplaceIfContains []string `json:"replace_if_contains"`
	Plugins           []struct {
		Marketplace string `json:"marketplace"` // marketplace name
		Source      string `json:"source"`      // pinned source for `claude plugin marketplace add`
		ID          string `json:"id"`          // plugin@marketplace
		// Commit is the reviewed commit. Claude Code pins marketplaces only by tag or branch,
		// and a tag can be moved, so the installed commit is checked against this one.
		Commit string `json:"commit"`
	} `json:"plugins"`
}

func loadClaudeSetup(release string) (claudeSetup, bool, error) {
	var c claudeSetup
	b, err := os.ReadFile(filepath.Join(release, "sync", "mcp-servers.json"))
	if os.IsNotExist(err) {
		return c, false, nil // releases before 1.4.1 have none
	}
	if err != nil {
		return c, false, err
	}
	return c, true, json.Unmarshal(b, &c)
}

// SetupClaude registers the release's MCP servers (user scope, so they work in every
// folder) and plugins. Customer-defined servers with the same name are kept, except
// known-unsafe ones. Failures warn; the install still completes.
func (a *App) SetupClaude(release, overclaudBin string) {
	cfg, ok, err := loadClaudeSetup(release)
	if err != nil {
		a.UI.Warn("Could not read the release's MCP server list: %v", err)
		return
	}
	if !ok {
		return
	}
	if _, err := a.Runner.LookPath("claude"); err != nil {
		a.UI.Warn("Claude Code is not on PATH, so MCP servers and plugins were not set up; run `overclaud install --repair` after installing it")
		return
	}
	a.cleanLegacyMCP(cfg.ReplaceIfContains)
	existing := a.userMCPServers()
	names := make([]string, 0, len(cfg.Servers))
	for n := range cfg.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := cfg.Servers[name]
		if w, ok := cfg.Windows[name]; ok && runtime.GOOS == "windows" {
			raw = w
		}
		want := strings.ReplaceAll(string(raw), "{overclaud}", jsonEscape(overclaudBin))
		if have, ok := existing[name]; ok {
			if sameJSON(have, []byte(want)) {
				continue
			}
			if !containsAny(string(have), cfg.ReplaceIfContains) {
				a.UI.Info("MCP server %s: keeping your own configuration", name)
				continue
			}
			a.UI.Warn("MCP server %s: replacing an unsafe configuration with the supported one", name)
			_, _ = a.Runner.Output("claude", "mcp", "remove", "-s", "user", name)
		}
		if _, err := a.Runner.Output("claude", "mcp", "add-json", "-s", "user", name, want); err != nil {
			a.UI.Warn("MCP server %s not added: %v", name, err)
			continue
		}
		a.UI.OK("MCP server %s added", name)
	}
	a.setupPlugins(cfg)
}

func (a *App) setupPlugins(cfg claudeSetup) {
	if len(cfg.Plugins) == 0 {
		return
	}
	markets := map[string]bool{}
	if out, err := a.Runner.Output("claude", "plugin", "marketplace", "list", "--json"); err == nil {
		var ms []struct{ Name string }
		if json.Unmarshal([]byte(out), &ms) == nil {
			for _, m := range ms {
				markets[m.Name] = true
			}
		}
	}
	installed := map[string]bool{}
	if out, err := a.Runner.Output("claude", "plugin", "list", "--json"); err == nil {
		var ps []struct{ ID string }
		if json.Unmarshal([]byte(out), &ps) == nil {
			for _, p := range ps {
				installed[p.ID] = true
			}
		}
	}
	for _, p := range cfg.Plugins {
		if installed[p.ID] {
			continue
		}
		if !markets[p.Marketplace] {
			if _, err := a.Runner.Output("claude", "plugin", "marketplace", "add", p.Source); err != nil {
				a.UI.Warn("Plugin source %s not added: %v", p.Marketplace, err)
				continue
			}
		}
		if _, err := a.Runner.Output("claude", "plugin", "install", p.ID); err != nil {
			a.UI.Warn("Plugin %s not installed: %v", p.ID, err)
			continue
		}
		if p.Commit != "" {
			if got := a.installedCommit(p.ID); got != p.Commit {
				_, _ = a.Runner.Output("claude", "plugin", "uninstall", p.ID)
				a.UI.Warn("Plugin %s removed: its source gave commit %q, not the reviewed %s", p.ID, got, p.Commit)
				continue
			}
		}
		a.UI.OK("Plugin %s installed", p.ID)
	}
}

// installedCommit is the commit Claude Code recorded for a user-scope plugin install.
func (a *App) installedCommit(id string) string {
	b, err := os.ReadFile(filepath.Join(a.Paths.ClaudeDir, "plugins", "installed_plugins.json"))
	if err != nil {
		return ""
	}
	var d struct {
		Plugins map[string][]struct {
			Scope        string `json:"scope"`
			GitCommitSha string `json:"gitCommitSha"`
		} `json:"plugins"`
	}
	if json.Unmarshal(b, &d) != nil {
		return ""
	}
	for _, r := range d.Plugins[id] {
		if r.Scope == "user" {
			return r.GitCommitSha
		}
	}
	return ""
}

// userMCPServers reads the user-scope servers from ~/.claude.json (read only; writes go
// through the claude CLI, which owns that file).
func (a *App) userMCPServers() map[string]json.RawMessage {
	b, err := os.ReadFile(filepath.Join(a.Paths.Home, ".claude.json"))
	if err != nil {
		return nil
	}
	var d struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	_ = json.Unmarshal(b, &d)
	return d.MCPServers
}

// cleanLegacyMCP removes known-unsafe servers from ~/.mcp.json, which the old install.sh
// wrote (and which only applied to sessions started in the home folder). Backed up first.
func (a *App) cleanLegacyMCP(unsafe []string) {
	path := filepath.Join(a.Paths.Home, ".mcp.json")
	b, err := os.ReadFile(path)
	if err != nil || len(unsafe) == 0 {
		return
	}
	var d map[string]json.RawMessage
	if json.Unmarshal(b, &d) != nil {
		return
	}
	var servers map[string]json.RawMessage
	if json.Unmarshal(d["mcpServers"], &servers) != nil {
		return
	}
	var removed []string
	for name, cfg := range servers {
		if containsAny(string(cfg), unsafe) {
			delete(servers, name)
			removed = append(removed, name)
		}
	}
	if len(removed) == 0 {
		return
	}
	backup := fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backup, b, 0o600); err != nil {
		a.UI.Warn("Could not back up %s, so its unsafe entries were left: %v", path, err)
		return
	}
	d["mcpServers"], _ = json.Marshal(servers)
	out, _ := json.MarshalIndent(d, "", "  ")
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		a.UI.Warn("Could not update %s: %v", path, err)
		return
	}
	sort.Strings(removed)
	a.UI.Warn("Removed unsafe MCP server(s) %s from %s (backup: %s)", strings.Join(removed, ", "), path, backup)
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if x != "" && strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ja, _ := json.Marshal(x)
	jb, _ := json.Marshal(y)
	return bytes.Equal(ja, jb)
}

// jsonEscape escapes s for use inside a JSON string literal (Windows paths have backslashes).
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}
