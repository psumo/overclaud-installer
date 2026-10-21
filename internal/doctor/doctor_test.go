package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psumo/overclaud-installer/internal/deploy"
	"github.com/psumo/overclaud-installer/internal/manifest"
	"github.com/psumo/overclaud-installer/internal/prereq"
)

func TestResolveHookCommands(t *testing.T) {
	i, s, ok := resolve(`python "$HOME/.claude/hooks/scripts/pre-tool-use.py"`, "/h")
	if !ok || i != "python" || s != filepath.FromSlash("/h/.claude/hooks/scripts/pre-tool-use.py") {
		t.Fatalf("%q %q %v", i, s, ok)
	}
	if _, _, ok := resolve(`if [ -z "${HOME-}" ]; then printf '{}'; fi`, "/h"); ok {
		t.Fatal("inline shell treated as a script hook")
	}
}

func python(t *testing.T) string {
	for _, n := range []string{"python3", "python"} {
		if p, err := exec.LookPath(n); err == nil {
			return filepath.Base(p)
		}
	}
	t.Skip("no python on PATH")
	return ""
}

func setup(t *testing.T, py, guard string) Options {
	t.Helper()
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	scripts := filepath.Join(claude, "hooks", "scripts")
	os.MkdirAll(scripts, 0o755)
	os.WriteFile(filepath.Join(scripts, "pre-tool-use.py"), []byte(guard), 0o644)
	os.WriteFile(filepath.Join(scripts, "other.py"), []byte("print('ok')\n"), 0o644)
	settings := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"` + py + ` \"$HOME/.claude/hooks/scripts/pre-tool-use.py\""}]}],` +
		`"Stop":[{"hooks":[{"type":"command","command":"` + py + ` \"$HOME/.claude/hooks/scripts/other.py\""}]}]}}`
	os.WriteFile(filepath.Join(claude, "settings.json"), []byte(settings), 0o644)
	sum, _, _ := manifest.HashFile(filepath.Join(scripts, "other.py"))
	return Options{Home: home, ClaudeDir: claude, Runner: prereq.OSRunner{},
		State: deploy.State{Version: "t", Files: map[string]string{"hooks/scripts/other.py": sum}}}
}

func find(rs []Result, check string) Result {
	for _, r := range rs {
		if r.Check == check {
			return r
		}
	}
	return Result{Check: check, Level: -1}
}

func TestSafetyHookMustReallyBlock(t *testing.T) {
	py := python(t)
	blocking := "import sys\nsys.stdin.read()\nprint('BLOCKED', file=sys.stderr)\nsys.exit(2)\n"
	o := setup(t, py, blocking)
	rs := checkHooks(o, mustHooks(t, o))
	if r := find(rs, "Safety hook"); r.Level != Pass {
		t.Fatalf("blocking guard not recognised: %+v", r)
	}
	lazy := "import sys\nsys.stdin.read()\n"
	o = setup(t, py, lazy)
	rs = checkHooks(o, mustHooks(t, o))
	if r := find(rs, "Safety hook"); r.Level != Fail {
		t.Fatalf("guard that allows everything passed: %+v", r)
	}
}

func TestMissingInterpreterAndBrokenScriptFail(t *testing.T) {
	py := python(t)
	o := setup(t, "python-does-not-exist", "import sys\nsys.exit(2)\n")
	rs := checkHooks(o, mustHooks(t, o))
	if r := find(rs, "Hook interpreter"); r.Level != Fail || !strings.Contains(r.Detail, "python-does-not-exist") {
		t.Fatalf("%+v", rs)
	}
	o = setup(t, py, "def broken(:\n")
	rs = checkHooks(o, mustHooks(t, o))
	if r := find(rs, "Hook syntax"); r.Level != Fail {
		t.Fatalf("syntax error not caught: %+v", rs)
	}
}

func TestFilesCheckReportsMissingAndEdited(t *testing.T) {
	o := setup(t, "python3", "")
	if r := checkFiles(o); r.Level != Pass {
		t.Fatalf("%+v", r)
	}
	os.WriteFile(filepath.Join(o.ClaudeDir, "hooks/scripts/other.py"), []byte("edited"), 0o644)
	if r := checkFiles(o); r.Level != Pass || !strings.Contains(r.Detail, "edited by you") {
		t.Fatalf("%+v", r)
	}
	os.Remove(filepath.Join(o.ClaudeDir, "hooks/scripts/other.py"))
	if r := checkFiles(o); r.Level != Fail {
		t.Fatalf("%+v", r)
	}
}

func mustHooks(t *testing.T, o Options) []hookCmd {
	h, rs := checkSettings(o)
	if rs[0].Level != Pass {
		t.Fatalf("settings: %+v", rs)
	}
	return h
}
