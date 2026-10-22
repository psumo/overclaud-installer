// Package devicecheck proves an Overclaud install actually works on this machine, beyond
// the files being in place: Claude Code reads the settings we installed, every hook event
// fires in a real session, the safety hook blocks inside that session, MCP servers connect,
// memory and the daily update are set up. Each problem names the repair (from a fixed,
// tested list) that fixes it; Claude only chooses among those, it never runs its own.
package devicecheck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/psumo/overclaud-installer/internal/doctor"
	"github.com/psumo/overclaud-installer/internal/prereq"
)

// Repair ids, implemented by the app package. Claude may only pick from these.
const (
	RepairInterpreter = "hook-interpreter" // point hook commands at a Python that works in the hook shell
	RepairGitBash     = "git-bash-path"    // tell Claude Code where Git Bash is (Windows)
	RepairMCP         = "mcp-servers"      // re-register Overclaud's MCP servers and plugins
	RepairSchedule    = "daily-update"     // re-register the daily update job
)

// RepairHelp describes each repair for the diagnosis prompt and the report.
var RepairHelp = map[string]string{
	RepairInterpreter: "rewrite Overclaud's hook commands to a Python that works in Claude Code's hook shell (python3, or py -3 on Windows)",
	RepairGitBash:     "set CLAUDE_CODE_GIT_BASH_PATH so Claude Code runs hooks in Git Bash instead of another bash",
	RepairMCP:         "re-register Overclaud's MCP servers and plugins with Claude Code (user scope)",
	RepairSchedule:    "re-register the daily `overclaud update` job with the system scheduler",
}

// Finding is one result of the check.
type Finding struct {
	Check  string
	Level  doctor.Level
	Detail string
	Repair string // a Repair id that fixes it, or ""
}

// Env is what the deterministic checks look at; tests fill it with fakes.
type Env struct {
	Home      string
	ClaudeDir string // where Overclaud installed (~/.claude)
	Runner    prereq.Runner
	Getenv    func(string) string
	GOOS      string
}

func (e Env) getenv(k string) string {
	if e.Getenv != nil {
		return e.Getenv(k)
	}
	return os.Getenv(k)
}

func (e Env) goos() string {
	if e.GOOS != "" {
		return e.GOOS
	}
	return runtime.GOOS
}

// Inspect runs the checks that need no AI and no session.
func Inspect(e Env) []Finding {
	var out []Finding
	out = append(out, configDir(e))
	out = append(out, managedSettings(e)...)
	if e.goos() == "windows" {
		out = append(out, gitBash(e))
	}
	out = append(out, hookPython(e))
	out = append(out, mcpServers(e)...)
	out = append(out, memoryPlugin(e))
	out = append(out, dailyUpdate(e))
	return out
}

// configDir: Claude Code reads CLAUDE_CONFIG_DIR when set; settings installed elsewhere
// are then silently ignored.
func configDir(e Env) Finding {
	f := Finding{Check: "Settings location"}
	cd := e.getenv("CLAUDE_CONFIG_DIR")
	if cd == "" || samePath(cd, e.ClaudeDir) {
		f.Detail = "Claude Code reads " + e.ClaudeDir
		return f
	}
	f.Level = doctor.Fail
	f.Detail = fmt.Sprintf("CLAUDE_CONFIG_DIR points Claude Code at %s, but Overclaud is installed in %s; unset that variable or reinstall with it set", cd, e.ClaudeDir)
	return f
}

// managedSettings are organisation policies that override user settings, hooks included.
func managedSettings(e Env) []Finding {
	var paths []string
	switch e.goos() {
	case "windows":
		paths = []string{`C:\ProgramData\ClaudeCode\managed-settings.json`}
	case "darwin":
		paths = []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}
	default:
		paths = []string{"/etc/claude-code/managed-settings.json"}
	}
	var out []Finding
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		detail := "an organisation policy at " + p + " can override Overclaud's settings"
		var m map[string]any
		if json.Unmarshal(b, &m) == nil {
			if v, _ := m["disableAllHooks"].(bool); v {
				detail = "an organisation policy at " + p + " turns hooks off, so Overclaud's hooks cannot run"
			} else if v, _ := m["allowManagedHooksOnly"].(bool); v {
				detail = "an organisation policy at " + p + " allows only its own hooks, so Overclaud's hooks cannot run"
			}
		}
		out = append(out, Finding{Check: "Organisation policy", Level: doctor.Warn, Detail: detail})
	}
	return out
}

// gitBash: on Windows Claude Code runs hooks in Git Bash. A `bash` from WSL in System32 is
// a different system without the user's tools.
func gitBash(e Env) Finding {
	f := Finding{Check: "Hook shell"}
	if p := e.getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			f.Detail = "Git Bash at " + p
			return f
		}
		f.Level, f.Repair = doctor.Fail, RepairGitBash
		f.Detail = "CLAUDE_CODE_GIT_BASH_PATH points at a missing file: " + p
		return f
	}
	if p, ok := FindGitBash(e); ok {
		f.Detail = "Git Bash at " + p
		return f
	}
	f.Level = doctor.Fail
	f.Detail = "Git Bash was not found; Claude Code needs it to run hooks on Windows (install Git for Windows)"
	return f
}

// FindGitBash looks for Git for Windows' bash.exe.
func FindGitBash(e Env) (string, bool) {
	for _, base := range []string{e.getenv("ProgramFiles"), e.getenv("ProgramFiles(x86)"), filepath.Join(e.getenv("LOCALAPPDATA"), "Programs")} {
		if base == "" {
			continue
		}
		p := filepath.Join(base, "Git", "bin", "bash.exe")
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// HookShell returns the shell and flag Claude Code uses to run hook commands.
func HookShell(e Env) (string, []string) {
	if e.goos() == "windows" {
		if p := e.getenv("CLAUDE_CODE_GIT_BASH_PATH"); p != "" {
			return p, []string{"-c"}
		}
		if p, ok := FindGitBash(e); ok {
			return p, []string{"-c"}
		}
		return "bash", []string{"-c"}
	}
	return "sh", []string{"-c"}
}

// hookPython: the interpreter named in the hook commands must start a real Python 3 in the
// hook shell (Windows' Store stub prints nothing useful and exits non-zero).
func hookPython(e Env) Finding {
	f := Finding{Check: "Hook interpreter"}
	interp := hookInterpreter(filepath.Join(e.ClaudeDir, "settings.json"))
	if interp == "" {
		f.Detail = "no Python hooks"
		return f
	}
	shell, flag := HookShell(e)
	out, err := e.Runner.Output(shell, append(flag, interp+" --version")...)
	if err == nil && strings.Contains(out, "Python 3") {
		f.Detail = fmt.Sprintf("`%s` starts %s", interp, strings.TrimSpace(out))
		return f
	}
	f.Level, f.Repair = doctor.Fail, RepairInterpreter
	f.Detail = fmt.Sprintf("hooks run `%s`, which does not start Python 3 in the hook shell", interp)
	return f
}

// hookInterpreter is the first word of the first Python hook command ("python",
// "python3", "py").
func hookInterpreter(settings string) string {
	for _, cmd := range HookCommands(settings) {
		w := strings.Fields(cmd)
		if len(w) > 0 && strings.HasPrefix(strings.TrimSuffix(w[0], ".exe"), "py") {
			return w[0]
		}
	}
	return ""
}

// HookCommands lists the command of every hook in a settings file.
func HookCommands(settings string) []string {
	b, err := os.ReadFile(settings)
	if err != nil {
		return nil
	}
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	events := make([]string, 0, len(s.Hooks))
	for ev := range s.Hooks {
		events = append(events, ev)
	}
	sort.Strings(events)
	var out []string
	for _, ev := range events {
		for _, g := range s.Hooks[ev] {
			for _, h := range g.Hooks {
				if h.Command != "" {
					out = append(out, h.Command)
				}
			}
		}
	}
	return out
}

// HookEvents lists the events a settings file registers hooks for.
func HookEvents(settings string) []string {
	b, err := os.ReadFile(settings)
	if err != nil {
		return nil
	}
	var s struct {
		Hooks map[string]json.RawMessage `json:"hooks"`
	}
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	out := make([]string, 0, len(s.Hooks))
	for ev := range s.Hooks {
		out = append(out, ev)
	}
	sort.Strings(out)
	return out
}

// mcpServers parses `claude mcp list`: "name: target - ✓ Connected" or "✗ Failed ...".
func mcpServers(e Env) []Finding {
	out, err := e.Runner.Output("claude", "mcp", "list")
	if err != nil {
		return []Finding{{Check: "MCP servers", Level: doctor.Warn, Detail: "`claude mcp list` failed: " + firstLine(out), Repair: RepairMCP}}
	}
	return ParseMCPList(out)
}

// ParseMCPList turns `claude mcp list` output into one finding per server.
func ParseMCPList(out string) []Finding {
	var fs []Finding
	for _, line := range strings.Split(out, "\n") {
		name, rest, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !ok || !strings.Contains(rest, " - ") {
			continue // the "Checking MCP server health…" banner and blank lines
		}
		f := Finding{Check: "MCP server " + name}
		status := rest[strings.LastIndex(rest, " - ")+3:]
		switch {
		case strings.Contains(status, "Connected"):
			f.Detail = "connected"
		case strings.Contains(strings.ToLower(status), "auth"):
			f.Level, f.Detail = doctor.Warn, "needs sign-in: "+status
		default:
			f.Level, f.Detail, f.Repair = doctor.Warn, status, RepairMCP
		}
		fs = append(fs, f)
	}
	return fs
}

// memoryPlugin: claude-mem installed and enabled.
func memoryPlugin(e Env) Finding {
	f := Finding{Check: "Memory (claude-mem)"}
	out, err := e.Runner.Output("claude", "plugin", "list", "--json")
	if err != nil {
		f.Level, f.Detail = doctor.Warn, "could not list plugins"
		return f
	}
	var ps []struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	_ = json.Unmarshal([]byte(out), &ps)
	for _, p := range ps {
		if p.ID == "claude-mem@thedotmack" {
			if p.Enabled {
				f.Detail = "installed and on"
			} else {
				f.Level, f.Detail = doctor.Warn, "installed but turned off: claude plugin enable claude-mem@thedotmack"
			}
			return f
		}
	}
	f.Level, f.Detail, f.Repair = doctor.Fail, "not installed", RepairMCP
	return f
}

// dailyUpdate looks for the job schedule.Install registers on this OS.
func dailyUpdate(e Env) Finding {
	f := Finding{Check: "Daily update"}
	found := false
	switch e.goos() {
	case "windows":
		_, err := e.Runner.Output("schtasks", "/Query", "/TN", "Overclaud Update")
		found = err == nil
	case "darwin":
		_, err := os.Stat(filepath.Join(e.Home, "Library", "LaunchAgents", "dev.overclaud.update.plist"))
		found = err == nil
	default:
		if _, err := os.Stat(filepath.Join(e.Home, ".config", "systemd", "user", "overclaud-update.timer")); err == nil {
			found = true
		} else if out, err := e.Runner.Output("crontab", "-l"); err == nil && strings.Contains(out, "overclaud update") {
			found = true
		}
	}
	if found {
		f.Detail = "scheduled"
		return f
	}
	f.Level, f.Detail, f.Repair = doctor.Warn, "not scheduled", RepairSchedule
	return f
}

func samePath(a, b string) bool {
	ca, _ := filepath.Abs(a)
	cb, _ := filepath.Abs(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
