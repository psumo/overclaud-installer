package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psumo/overclaud-installer/internal/devicecheck"
	"github.com/psumo/overclaud-installer/internal/doctor"
)

func TestSwapInterpreter(t *testing.T) {
	cases := []struct{ in, use, want string }{
		{`python "$HOME/x.py"`, "python3", `python3 "$HOME/x.py"`},
		{`python3 "$HOME/x.py"`, "py -3", `py -3 "$HOME/x.py"`},
		{`py -3 x.py`, "python3", `python3 x.py`},
		{`python3 x.py`, "python3", `python3 x.py`},
		{`node x.js`, "python3", `node x.js`},
		{`pythonic-tool run`, "python3", `pythonic-tool run`},
	}
	for _, c := range cases {
		got, _ := swapInterpreter(c.in, c.use)
		if got != c.want {
			t.Fatalf("swap(%q,%q) = %q, want %q", c.in, c.use, got, c.want)
		}
	}
}

func TestRepairChoice(t *testing.T) {
	fs := []devicecheck.Finding{
		{Check: "a", Level: doctor.Pass, Repair: devicecheck.RepairSchedule}, // passing: not suggested
		{Check: "b", Level: doctor.Fail, Repair: devicecheck.RepairInterpreter},
		{Check: "c", Level: doctor.Warn, Repair: devicecheck.RepairMCP},
		{Check: "d", Level: doctor.Warn}, // a warning with no repair is not a problem to loop on
	}
	if !hasProblems(fs) {
		t.Fatal("a failure is a problem")
	}
	if got := strings.Join(suggestedRepairs(fs), ","); got != "hook-interpreter,mcp-servers" {
		t.Fatalf("suggested %s", got)
	}
	if got := intersect([]string{"mcp-servers", "anything-else"}, suggestedRepairs(fs)); strings.Join(got, ",") != "mcp-servers" {
		t.Fatalf("Claude's picks must be limited to the suggested repairs, got %v", got)
	}
	if hasProblems([]devicecheck.Finding{{Level: doctor.Warn}}) {
		t.Fatal("an unrepairable warning alone should not keep the loop going")
	}
}

// pyRunner: only `python3` starts Python 3 in the hook shell.
type pyRunner struct{}

func (pyRunner) LookPath(n string) (string, error) { return "/bin/" + n, nil }
func (pyRunner) Output(name string, args ...string) (string, error) {
	if len(args) > 1 && strings.HasPrefix(args[len(args)-1], "python3 ") {
		return "Python 3.12.3", nil
	}
	return "", os.ErrNotExist
}
func (pyRunner) Interactive(string, ...string) error { return nil }

func TestRepairInterpreterRewritesHooks(t *testing.T) {
	a, _ := testApp(t, "")
	a.Runner = pyRunner{}
	if err := os.MkdirAll(a.Paths.ClaudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Paths.ClaudeDir, "settings.json")
	in := `{"env": {"X": "python keep"}, "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "python \"$HOME/.claude/hooks/scripts/x.py\""}]}]}}`
	if err := os.WriteFile(path, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.repairInterpreter(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	cmd := s["hooks"].(map[string]any)["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"]
	if cmd != `python3 "$HOME/.claude/hooks/scripts/x.py"` {
		t.Fatalf("hook command %v", cmd)
	}
	if s["env"].(map[string]any)["X"] != "python keep" {
		t.Fatal("only hook commands may change")
	}
	if backups, _ := filepath.Glob(path + ".bak-*"); len(backups) != 1 {
		t.Fatalf("want one backup, got %v", backups)
	}
}
