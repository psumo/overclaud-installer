package devicecheck

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psumo/overclaud-installer/internal/doctor"
)

// Real `claude mcp list` output (Claude Code 2.1.306).
const mcpList = `Checking MCP server health…

claude.ai Claude Docs: https://api.anthropic.com/v1/pages/mcp - ✔ Connected
plugin:context7:context7: https://mcp.context7.com/mcp?client=claude-code-plugin (HTTP) - ! Needs authentication
github: https://api.githubcopilot.com/mcp/ (HTTP) - ✗ Failed to connect
claude-flow: npx -y claude-flow@3.59.17 mcp start - ✔ Connected
`

func TestParseMCPList(t *testing.T) {
	got := map[string]Finding{}
	for _, f := range ParseMCPList(mcpList) {
		got[strings.TrimPrefix(f.Check, "MCP server ")] = f
	}
	if len(got) != 4 {
		t.Fatalf("want 4 servers, got %d: %+v", len(got), got)
	}
	if got["claude.ai Claude Docs"].Level != doctor.Pass || got["claude-flow"].Level != doctor.Pass {
		t.Fatal("connected servers must pass")
	}
	if f := got["plugin:context7:context7"]; f.Level != doctor.Warn || f.Repair != "" {
		t.Fatalf("needs-auth is a sign-in, not a repair: %+v", f)
	}
	if f := got["github"]; f.Level != doctor.Warn || f.Repair != RepairMCP {
		t.Fatalf("a failed server should offer the MCP repair: %+v", f)
	}
}

// Lines in the shape `claude -p --output-format stream-json --include-hook-events` writes.
const stream = `{"type":"system","subtype":"init"}
{"type":"system","subtype":"hook_started","hook_name":"SessionStart:startup","hook_event":"SessionStart"}
{"type":"system","subtype":"hook_response","hook_event":"SessionStart","exit_code":"0","outcome":"success"}
{"type":"system","subtype":"hook_started","hook_event":"UserPromptSubmit"}
{"type":"system","subtype":"hook_response","hook_event":"UserPromptSubmit","exit_code":"0","outcome":"success"}
{"type":"system","subtype":"hook_started","hook_event":"PreToolUse"}
{"type":"system","subtype":"hook_response","hook_event":"PreToolUse","exit_code":"2","outcome":"blocked"}
{"type":"system","subtype":"hook_started","hook_event":"PostToolUse"}
{"type":"system","subtype":"hook_response","hook_event":"PostToolUse","exit_code":"1","outcome":"error"}
{"type":"system","subtype":"hook_started","hook_event":"Stop"}
{"type":"system","subtype":"hook_response","hook_event":"Stop","exit_code":"0","outcome":"success"}
{"type":"result","subtype":"success"}
`

func TestParseHookEventsAndFindings(t *testing.T) {
	r := ParseHookEvents([]byte(stream))
	if !r.Answered || !r.SafetyBlocked || r.Started["PreToolUse"] != 1 || r.Failed["PostToolUse"] != 1 {
		t.Fatalf("parsed %+v", r)
	}
	fs := SessionFindings([]string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"}, r)
	if fs[0].Level != doctor.Warn || !strings.Contains(fs[0].Detail, "PostToolUse") {
		t.Fatalf("an errored hook should warn: %+v", fs[0])
	}
	if fs[1].Level != doctor.Pass {
		t.Fatalf("safety hook blocked, should pass: %+v", fs[1])
	}

	quiet := ParseHookEvents([]byte(`{"type":"system","subtype":"hook_started","hook_event":"SessionStart"}
{"type":"result"}`))
	fs = SessionFindings([]string{"SessionStart", "PreToolUse"}, quiet)
	if fs[0].Level != doctor.Fail || !strings.Contains(fs[0].Detail, "PreToolUse") {
		t.Fatalf("a registered event that never ran must fail: %+v", fs[0])
	}
	if fs[1].Level != doctor.Fail {
		t.Fatal("no block means the safety hook failed")
	}
}

func TestNotSignedIn(t *testing.T) {
	r := ParseHookEvents([]byte(`Invalid API key · Please run /login`))
	fs := SessionFindings(nil, r)
	if len(fs) != 1 || fs[0].Level != doctor.Warn {
		t.Fatalf("not signed in should be a single warning: %+v", fs)
	}
}

func TestParseDiagnosisKeepsOnlyKnownRepairs(t *testing.T) {
	out := []byte(`{"type":"result","result":"Here you go:\n{\"explanation\":\"python is the Store stub\",\"repairs\":[\"hook-interpreter\",\"rm -rf\",\"hook-interpreter\",\"daily-update\"]}"}`)
	d, err := ParseDiagnosis(out)
	if err != nil {
		t.Fatal(err)
	}
	if d.Explanation != "python is the Store stub" || strings.Join(d.Repairs, ",") != "hook-interpreter,daily-update" {
		t.Fatalf("got %+v", d)
	}
}

func TestConfigDirMismatch(t *testing.T) {
	e := Env{ClaudeDir: "/home/u/.claude", Getenv: func(k string) string {
		if k == "CLAUDE_CONFIG_DIR" {
			return "/somewhere/else"
		}
		return ""
	}}
	if f := configDir(e); f.Level != doctor.Fail {
		t.Fatalf("a different CLAUDE_CONFIG_DIR must fail: %+v", f)
	}
	e.Getenv = func(string) string { return "" }
	if f := configDir(e); f.Level != doctor.Pass {
		t.Fatalf("unset must pass: %+v", f)
	}
}

type shellRunner struct{ out string }

func (s shellRunner) LookPath(n string) (string, error) { return "/bin/" + n, nil }
func (s shellRunner) Output(name string, args ...string) (string, error) {
	if s.out == "" {
		return "", errors.New("exit 9009")
	}
	return s.out, nil
}
func (s shellRunner) Interactive(string, ...string) error { return nil }

func TestHookPython(t *testing.T) {
	dir := t.TempDir()
	settings := `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "python \"$HOME/.claude/hooks/scripts/x.py\""}]}]}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	e := Env{ClaudeDir: dir, GOOS: "linux", Runner: shellRunner{out: "Python 3.12.3"}}
	if f := hookPython(e); f.Level != doctor.Pass {
		t.Fatalf("working python should pass: %+v", f)
	}
	e.Runner = shellRunner{}
	if f := hookPython(e); f.Level != doctor.Fail || f.Repair != RepairInterpreter {
		t.Fatalf("a python that does not start must offer the interpreter repair: %+v", f)
	}
	if got := HookEvents(filepath.Join(dir, "settings.json")); strings.Join(got, ",") != "Stop" {
		t.Fatalf("events %v", got)
	}
}
