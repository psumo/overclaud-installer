package devicecheck

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/psumo/overclaud-installer/internal/doctor"
	"github.com/psumo/overclaud-installer/internal/prereq"
)

// canaryCommand is what the session asks Claude to run. The safety hook must block it
// (it reads inside the key store folder). It is harmless if it ever ran: the file does
// not exist.
const canaryCommand = "cat ~/.overclaud/secrets/device-check-canary"

// sessionEvents are the hook events one tool-using session is guaranteed to fire.
var sessionEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"}

// SessionResult is what a real headless session showed.
type SessionResult struct {
	Started       map[string]int // hook runs per event
	Failed        map[string]int // runs that errored (exit code other than 0 or 2)
	SafetyBlocked bool           // a PreToolUse hook exited 2 (blocked)
	Answered      bool
	NotSignedIn   bool
	Err           string
}

// RunSession starts `claude -p` in an empty folder, asks it to run the canary command,
// and reads the hook lifecycle events from its stream.
func RunSession(ctx context.Context, claude string) SessionResult {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	// A blocked command never fires PostToolUse, so a harmless allowed command runs first.
	prompt := "This is an automated setup check. Use the Bash tool twice, one command per call, exactly as written: " +
		"first `echo overclaud-device-check`, then `" + canaryCommand + "`. " +
		"Then reply with the single word DONE whatever happened."
	c := exec.CommandContext(ctx, claude, "-p", prompt,
		"--output-format", "stream-json", "--verbose", "--include-hook-events",
		// Only the exact harmless echo is approved. The canary read stays unapproved: in -p
		// mode it is denied anyway, and PreToolUse hooks run before that decision.
		"--max-turns", "4", "--allowedTools", "Bash(echo overclaud-device-check)")
	c.Dir = prereq.NeutralDir()
	c.Env = append(prereq.CleanEnv(), "OVERCLAUD_DEVICE_CHECK=1")
	out, err := c.CombinedOutput()
	r := ParseHookEvents(out)
	if err != nil && !r.Answered {
		r.Err = firstLine(lastLines(string(out), 3))
	}
	return r
}

// ParseHookEvents reads a stream-json transcript.
func ParseHookEvents(stream []byte) SessionResult {
	r := SessionResult{Started: map[string]int{}, Failed: map[string]int{}}
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		low := strings.ToLower(string(line))
		if strings.Contains(low, "not logged in") || strings.Contains(low, "please run /login") || strings.Contains(low, "invalid api key") {
			r.NotSignedIn = true
		}
		var m struct {
			Type     string          `json:"type"`
			Subtype  string          `json:"subtype"`
			Event    string          `json:"hook_event"`
			ExitCode json.RawMessage `json:"exit_code"`
			Outcome  string          `json:"outcome"`
		}
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch {
		case m.Type == "system" && m.Subtype == "hook_started":
			r.Started[m.Event]++
		case m.Type == "system" && m.Subtype == "hook_response":
			code := strings.Trim(string(m.ExitCode), `"`)
			switch {
			case code == "2" || m.Outcome == "blocked":
				if m.Event == "PreToolUse" {
					r.SafetyBlocked = true
				}
			case code != "0" && code != "" && code != "null":
				r.Failed[m.Event]++
			}
		case m.Type == "result":
			r.Answered = true
		}
	}
	return r
}

// SessionFindings compares a session with the hook events the installed settings register.
func SessionFindings(registered []string, r SessionResult) []Finding {
	if r.NotSignedIn {
		return []Finding{{Check: "Real session", Level: doctor.Warn, Detail: "Claude Code is not signed in, so the session check was skipped: run `claude auth login`"}}
	}
	if !r.Answered && len(r.Started) == 0 {
		return []Finding{{Check: "Real session", Level: doctor.Fail, Detail: "a headless Claude Code session did not run: " + r.Err}}
	}
	reg := map[string]bool{}
	for _, e := range registered {
		reg[e] = true
	}
	var missing, failed []string
	for _, ev := range sessionEvents {
		if reg[ev] && r.Started[ev] == 0 {
			missing = append(missing, ev)
		}
	}
	for ev, n := range r.Failed {
		failed = append(failed, fmt.Sprintf("%s (%d)", ev, n))
	}
	sort.Strings(failed)
	var out []Finding
	hooks := Finding{Check: "Hooks in a real session"}
	switch {
	case len(missing) > 0:
		hooks.Level = doctor.Fail
		hooks.Detail = "these events never ran Overclaud's hooks: " + strings.Join(missing, ", ")
	case len(failed) > 0:
		hooks.Level, hooks.Repair = doctor.Warn, RepairInterpreter
		hooks.Detail = "some hooks errored: " + strings.Join(failed, ", ")
	default:
		total := 0
		for _, n := range r.Started {
			total += n
		}
		hooks.Detail = fmt.Sprintf("%d hook runs across %d events, none errored", total, len(r.Started))
	}
	out = append(out, hooks)
	safety := Finding{Check: "Safety hook in a real session"}
	if r.SafetyBlocked {
		safety.Detail = "blocked a read of the key store inside a real session"
	} else {
		safety.Level = doctor.Fail
		safety.Detail = "did not block a read of the key store inside a real session"
	}
	return append(out, safety)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " ")
}

// Diagnosis is Claude's reading of the findings: a plain explanation and repairs chosen
// only from RepairHelp.
type Diagnosis struct {
	Explanation string   `json:"explanation"`
	Repairs     []string `json:"repairs"`
}

// Diagnose asks Claude to explain the problems and pick repairs. Unknown repair ids are
// dropped, so Claude can never cause anything outside the tested list.
func Diagnose(ctx context.Context, claude, goos string, fs []Finding) (Diagnosis, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	var b strings.Builder
	for _, f := range fs {
		lvl := map[doctor.Level]string{doctor.Pass: "PASS", doctor.Warn: "WARN", doctor.Fail: "FAIL"}[f.Level]
		fmt.Fprintf(&b, "%s %s: %s\n", lvl, f.Check, f.Detail)
	}
	ids := make([]string, 0, len(RepairHelp))
	for id := range RepairHelp {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var rb strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&rb, "- %s: %s\n", id, RepairHelp[id])
	}
	prompt := "You are checking an Overclaud install for Claude Code on " + goos + ". " +
		"The device check found the results below. Explain in plain words, in at most four sentences, " +
		"what is wrong on this machine and why. Then choose repairs ONLY from the list; choose none if none fits. " +
		"Do not use tools. Reply with only a JSON object: {\"explanation\": \"...\", \"repairs\": [\"id\", ...]}.\n\n" +
		"<pasted_content source=\"device check\">\n" + b.String() + "</pasted_content>\n\nRepairs:\n" + rb.String()
	c := exec.CommandContext(ctx, claude, "-p", prompt, "--output-format", "json", "--max-turns", "1")
	c.Dir = prereq.NeutralDir()
	c.Env = prereq.CleanEnv()
	out, err := c.Output()
	if err != nil {
		return Diagnosis{}, fmt.Errorf("claude -p: %w", err)
	}
	return ParseDiagnosis(out)
}

// ParseDiagnosis reads `claude -p --output-format json` output whose result is the JSON
// object, keeping only known repair ids.
func ParseDiagnosis(out []byte) (Diagnosis, error) {
	var wrap struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(out, &wrap); err != nil {
		return Diagnosis{}, err
	}
	text := wrap.Result
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var d Diagnosis
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		return Diagnosis{Explanation: strings.TrimSpace(wrap.Result)}, nil
	}
	kept := d.Repairs[:0]
	seen := map[string]bool{}
	for _, id := range d.Repairs {
		if _, ok := RepairHelp[id]; ok && !seen[id] {
			kept = append(kept, id)
			seen[id] = true
		}
	}
	d.Repairs = kept
	return d, nil
}
