package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/psumo/overclaud-installer/internal/devicecheck"
	"github.com/psumo/overclaud-installer/internal/doctor"
	"github.com/psumo/overclaud-installer/internal/licence"
	"github.com/psumo/overclaud-installer/internal/schedule"
)

const maxRepairRounds = 3

// DeviceCheck runs the device check, asks Claude to explain any problems and pick repairs
// from the tested list, applies them (asking first when a person is there), and checks
// again, for at most three rounds. It returns the last findings. Its results never roll
// an update back: they describe this machine, not the release.
func (a *App) DeviceCheck(interactive bool, bin string) []devicecheck.Finding {
	if os.Getenv("OVERCLAUD_NO_DEVICE_CHECK") == "1" {
		return nil
	}
	env := devicecheck.Env{Home: a.Paths.Home, ClaudeDir: a.Paths.ClaudeDir, Runner: a.Runner}
	claude, claudeErr := a.Runner.LookPath("claude")
	var fs []devicecheck.Finding
	for round := 1; round <= maxRepairRounds; round++ {
		fs = devicecheck.Inspect(env)
		if claudeErr == nil {
			a.UI.Info("Running a short Claude Code session to watch the hooks work...")
			s := devicecheck.RunSession(context.Background(), claude)
			fs = append(fs, devicecheck.SessionFindings(devicecheck.HookEvents(filepath.Join(a.Paths.ClaudeDir, "settings.json")), s)...)
		}
		a.printFindings(fs)
		suggested := suggestedRepairs(fs)
		if !hasProblems(fs) {
			a.UI.OK("Overclaud works on this computer.")
			return fs
		}
		if len(suggested) == 0 || round == maxRepairRounds {
			break
		}
		chosen := suggested
		if claudeErr == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			d, err := devicecheck.Diagnose(ctx, claude, runtime.GOOS, fs)
			cancel()
			if err == nil {
				if d.Explanation != "" {
					a.UI.Info("Claude: %s", d.Explanation)
				}
				if picked := intersect(d.Repairs, suggested); len(picked) > 0 {
					chosen = picked
				}
			}
		}
		// Scheduled updates (not interactive) apply the tested fixes on their own. A person
		// is asked; a scripted run with no terminal only reports.
		if interactive {
			if !a.UI.HasTerminal() {
				a.UI.Info("Fixes available: %s. Run `overclaud doctor --device` in a terminal to apply them.", strings.Join(chosen, ", "))
				break
			}
			names := make([]string, len(chosen))
			for i, id := range chosen {
				names[i] = devicecheck.RepairHelp[id]
			}
			if !a.UI.Confirm("Apply these fixes? "+strings.Join(names, "; "), true) {
				break
			}
		}
		applied := 0
		for _, id := range chosen {
			if err := a.applyRepair(id, bin); err != nil {
				a.UI.Warn("Fix %q did not apply: %v", id, err)
				continue
			}
			a.UI.OK("Applied: %s", devicecheck.RepairHelp[id])
			applied++
		}
		if applied == 0 {
			break
		}
		a.UI.Info("Checking again (round %d of %d)...", round+1, maxRepairRounds)
	}
	if hasProblems(fs) {
		a.UI.Warn("Some problems remain (above). `overclaud doctor --device` runs this check again.")
	}
	return fs
}

func (a *App) printFindings(fs []devicecheck.Finding) {
	for _, f := range fs {
		line := fmt.Sprintf("%s: %s", f.Check, f.Detail)
		switch f.Level {
		case doctor.Pass:
			a.UI.OK("%s", line)
		case doctor.Warn:
			a.UI.Warn("%s", line)
		default:
			a.UI.Fail("%s", line)
		}
	}
}

func hasProblems(fs []devicecheck.Finding) bool {
	for _, f := range fs {
		if f.Level == doctor.Fail || (f.Level == doctor.Warn && f.Repair != "") {
			return true
		}
	}
	return false
}

func suggestedRepairs(fs []devicecheck.Finding) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fs {
		if f.Repair != "" && f.Level != doctor.Pass && !seen[f.Repair] {
			seen[f.Repair] = true
			out = append(out, f.Repair)
		}
	}
	sort.Strings(out)
	return out
}

func intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if in[x] {
			out = append(out, x)
		}
	}
	return out
}

// applyRepair runs one repair from devicecheck.RepairHelp. Nothing else can be run.
func (a *App) applyRepair(id, bin string) error {
	switch id {
	case devicecheck.RepairInterpreter:
		return a.repairInterpreter()
	case devicecheck.RepairGitBash:
		if runtime.GOOS != "windows" {
			return fmt.Errorf("only applies on Windows")
		}
		p, ok := devicecheck.FindGitBash(devicecheck.Env{})
		if !ok {
			return fmt.Errorf("Git Bash not found; install Git for Windows")
		}
		if _, err := a.Runner.Output("setx", "CLAUDE_CODE_GIT_BASH_PATH", p); err != nil {
			return err
		}
		_ = os.Setenv("CLAUDE_CODE_GIT_BASH_PATH", p)
		return nil
	case devicecheck.RepairMCP:
		a.SetupClaude(a.Paths.ProductDir, bin)
		return nil
	case devicecheck.RepairSchedule:
		if a.NoSched || bin == "" {
			return fmt.Errorf("scheduling is turned off for this install")
		}
		_, err := schedule.Install(bin, a.Paths.Home, licence.DeviceID(a.Paths))
		return err
	}
	return fmt.Errorf("unknown repair %q", id)
}

// repairInterpreter points every Python hook command at the first interpreter that starts
// Python 3 in the hook shell. settings.json is backed up first.
func (a *App) repairInterpreter() error {
	env := devicecheck.Env{Home: a.Paths.Home, ClaudeDir: a.Paths.ClaudeDir, Runner: a.Runner}
	shell, flag := devicecheck.HookShell(env)
	candidates := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		candidates = []string{"py -3", "python", "python3"}
	}
	use := ""
	for _, c := range candidates {
		if out, err := a.Runner.Output(shell, append(flag, c+" --version")...); err == nil && strings.Contains(out, "Python 3") {
			use = c
			break
		}
	}
	if use == "" {
		return fmt.Errorf("no Python 3 starts in the hook shell (%s); install Python and run `overclaud install --repair`", shell)
	}
	path := filepath.Join(a.Paths.ClaudeDir, "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if !rewriteInterpreters(s["hooks"], use) {
		return nil
	}
	backup := fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backup, b, 0o600); err != nil {
		return err
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileKeepMode(path, append(out, '\n'))
}

// rewriteInterpreters replaces the leading python/python3/py word of hook commands.
func rewriteInterpreters(v any, use string) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if k == "command" {
				if s, ok := x.(string); ok {
					if ns, ok := swapInterpreter(s, use); ok {
						t[k] = ns
						changed = true
					}
				}
				continue
			}
			changed = rewriteInterpreters(x, use) || changed
		}
	case []any:
		for _, x := range t {
			changed = rewriteInterpreters(x, use) || changed
		}
	}
	return changed
}

func swapInterpreter(cmd, use string) (string, bool) {
	for _, old := range []string{"py -3 ", "python3 ", "python "} {
		if strings.HasPrefix(cmd, old) {
			if old == use+" " {
				return cmd, false
			}
			return use + " " + strings.TrimPrefix(cmd, old), true
		}
	}
	return cmd, false
}

func writeFileKeepMode(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
