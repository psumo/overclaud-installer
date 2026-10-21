package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/psumo/overclaud-installer/internal/manifest"
)

type env struct {
	t       *testing.T
	release string
	claude  string
	backups string
}

func newEnv(t *testing.T) *env {
	return &env{t: t, release: t.TempDir(), claude: t.TempDir(), backups: t.TempDir()}
}

func put(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

// release writes a release tree (product files under .claude/) and returns its manifest.
func (e *env) releaseWith(files map[string]string) manifest.Manifest {
	os.RemoveAll(e.release)
	os.MkdirAll(e.release, 0o755)
	for rel, body := range files {
		put(e.t, e.release, rel, body)
	}
	m, err := manifest.Build(e.release, "test")
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) deploy(prev State, m manifest.Manifest) (State, Report) {
	e.t.Helper()
	st, rep, err := Deploy(Options{Release: e.release, Manifest: m, ClaudeDir: e.claude, Prev: prev, BackupRoot: e.backups})
	if err != nil {
		e.t.Fatal(err)
	}
	return st, rep
}

func TestFirstInstallWritesProductAndLeavesCustomerFilesAlone(t *testing.T) {
	e := newEnv(t)
	put(t, e.claude, "projects/x/memory/MEMORY.md", "mine")
	put(t, e.claude, "skills/a/SKILL.md", "old local copy")
	m := e.releaseWith(map[string]string{".claude/skills/a/SKILL.md": "A1", ".claude/rules/r.md": "R1", "README.md": "root"})
	st, _ := e.deploy(State{}, m)
	if read(t, e.claude, "skills/a/SKILL.md") != "A1" || read(t, e.claude, "rules/r.md") != "R1" {
		t.Fatal("product files not deployed")
	}
	if read(t, e.claude, "projects/x/memory/MEMORY.md") != "mine" {
		t.Fatal("customer file touched")
	}
	if read(t, e.claude, "README.md") != "<missing>" {
		t.Fatal("root release file deployed into ~/.claude")
	}
	if len(st.Files) != 2 {
		t.Fatalf("state should track 2 product files, got %v", st.Files)
	}
}

func TestUpdateReplacesUntouchedKeepsCustomerEditsAndDeletesRemoved(t *testing.T) {
	e := newEnv(t)
	m1 := e.releaseWith(map[string]string{".claude/a.md": "a1", ".claude/b.md": "b1", ".claude/gone.md": "g1", ".claude/gone-edited.md": "e1"})
	st1, _ := e.deploy(State{}, m1)
	put(t, e.claude, "b.md", "customer edit")
	put(t, e.claude, "gone-edited.md", "customer edit")
	m2 := e.releaseWith(map[string]string{".claude/a.md": "a2", ".claude/b.md": "b2", ".claude/new.md": "n"})
	_, rep := e.deploy(st1, m2)
	if read(t, e.claude, "a.md") != "a2" {
		t.Fatal("untouched file not updated")
	}
	if read(t, e.claude, "b.md") != "customer edit" || read(t, e.claude, "b.md.overclaud-new") != "b2" {
		t.Fatal("customer edit not preserved with the new version beside it")
	}
	if read(t, e.claude, "gone.md") != "<missing>" {
		t.Fatal("file removed from product not deleted")
	}
	if read(t, e.claude, "gone-edited.md") != "customer edit" {
		t.Fatal("customer-edited file deleted")
	}
	if read(t, e.claude, "new.md") != "n" {
		t.Fatal("new file not written")
	}
	if !equal(rep.Conflicts, []string{"b.md"}) || !equal(rep.KeptRemoved, []string{"gone-edited.md"}) {
		t.Fatalf("report %+v", rep)
	}
}

func TestSettingsAreMergedNotOverwritten(t *testing.T) {
	e := newEnv(t)
	m1 := e.releaseWith(map[string]string{".claude/settings.json": `{"effortLevel":"high","hooks":{"Stop":[{"hooks":[{"type":"command","command":"python a.py"}]}]}}`})
	st1, _ := e.deploy(State{}, m1)
	var s map[string]any
	json.Unmarshal([]byte(read(t, e.claude, "settings.json")), &s)
	s["model"] = "opus"
	b, _ := json.Marshal(s)
	put(t, e.claude, "settings.json", string(b))
	m2 := e.releaseWith(map[string]string{".claude/settings.json": `{"effortLevel":"medium","hooks":{"Stop":[{"hooks":[{"type":"command","command":"python a.py"}]},{"hooks":[{"type":"command","command":"python b.py"}]}]}}`})
	_, rep := e.deploy(st1, m2)
	var got map[string]any
	json.Unmarshal([]byte(read(t, e.claude, "settings.json")), &got)
	if got["model"] != "opus" || got["effortLevel"] != "medium" {
		t.Fatalf("merge lost a value: %v", got)
	}
	if n := len(got["hooks"].(map[string]any)["Stop"].([]any)); n != 2 {
		t.Fatalf("want 2 Stop hooks, got %d", n)
	}
	if len(rep.Conflicts) != 0 {
		t.Fatalf("settings reported as conflict: %v", rep.Conflicts)
	}
}

func TestRollbackRestoresThePreviousState(t *testing.T) {
	e := newEnv(t)
	m1 := e.releaseWith(map[string]string{".claude/a.md": "a1", ".claude/old.md": "o"})
	st1, _ := e.deploy(State{}, m1)
	m2 := e.releaseWith(map[string]string{".claude/a.md": "a2", ".claude/added.md": "x"})
	_, rep := e.deploy(st1, m2)
	if err := Rollback(rep.Backup, e.claude); err != nil {
		t.Fatal(err)
	}
	if read(t, e.claude, "a.md") != "a1" || read(t, e.claude, "old.md") != "o" || read(t, e.claude, "added.md") != "<missing>" {
		t.Fatalf("rollback incomplete: a=%q old=%q added=%q", read(t, e.claude, "a.md"), read(t, e.claude, "old.md"), read(t, e.claude, "added.md"))
	}
}

func TestPython3NormalizationAppliesWhenRequested(t *testing.T) {
	e := newEnv(t)
	m := e.releaseWith(map[string]string{".claude/settings.json": `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"python \"$HOME/.claude/x.py\""}]}]}}`})
	_, _, err := Deploy(Options{Release: e.release, Manifest: m, ClaudeDir: e.claude, BackupRoot: e.backups, Python3: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, e.claude, "settings.json"); !contains(got, `python3 \"$HOME`) {
		t.Fatalf("hooks not pointed at python3: %s", got)
	}
}

func equal(a, b []string) bool {
	sort.Strings(a)
	sort.Strings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || index(s, sub) >= 0) }

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
