package app

import (
	"bufio"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/psumo/overclaud-installer/internal/doctor"
	"github.com/psumo/overclaud-installer/internal/licence"
	"github.com/psumo/overclaud-installer/internal/secrets"
	"github.com/psumo/overclaud-installer/internal/services"
	"github.com/psumo/overclaud-installer/internal/ui"
)

type stubRunner struct{ signedIn bool }

func (s stubRunner) LookPath(name string) (string, error) {
	if name == "claude" || name == "gh" {
		return "/bin/" + name, nil
	}
	return "", errors.New("not found")
}
func (s stubRunner) Output(name string, args ...string) (string, error) {
	if !s.signedIn {
		return "", errors.New("exit 1")
	}
	return `{"loggedIn": true}`, nil
}
func (s stubRunner) Interactive(string, ...string) error { return nil }

func testApp(t *testing.T, input string) (*App, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	out := &bytes.Buffer{}
	u := &ui.UI{Out: out, In: bufio.NewReader(strings.NewReader(input))}
	return &App{UI: u, Paths: licence.Layout{Home: home, ClaudeDir: filepath.Join(home, ".claude")}, Runner: stubRunner{signedIn: true},
		secrets: secrets.FileStore{Dir: filepath.Join(home, ".overclaud", "secrets")}}, out
}

func TestSessionEnvWritesOnlySessionKeys(t *testing.T) {
	a, _ := testApp(t, "")
	for _, k := range []string{"EXA_API_KEY", "GEMINI_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(k, "") // a customer-set variable would win; make sure none leak in from the test machine
		os.Unsetenv(k)
	}
	_ = a.store().Set("exa", "exa-'quoted'")
	_ = a.store().Set("openrouter", "or-secret") // not a session key: only or-proxy reads it
	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	if err := a.Keys([]string{"session-env"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(envFile)
	got := string(b)
	if !strings.Contains(got, `export EXA_API_KEY='exa-'\''quoted'\'''`) {
		t.Fatalf("exa export missing or badly quoted:\n%s", got)
	}
	if strings.Contains(got, "or-secret") {
		t.Fatal("the OpenRouter key must not be exported to sessions")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(envFile); fi.Mode().Perm() != 0o600 {
			t.Fatalf("env file mode %v", fi.Mode().Perm())
		}
	}
}

func TestSessionEnvNeedsClaudeEnvFile(t *testing.T) {
	a, _ := testApp(t, "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	if err := a.Keys([]string{"session-env"}); err == nil {
		t.Fatal("want an error without CLAUDE_ENV_FILE")
	}
}

func TestSetRejectsBadKeyAndSavesGoodOne(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer good-key" {
			w.WriteHeader(200)
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	s := services.Service{ID: "openai", Name: "OpenAI", Kind: services.APIKey, Env: "OPENAI_API_KEY",
		Verify: &services.Probe{URL: srv.URL, Header: "Authorization", Prefix: "Bearer "}}
	a, _ := testApp(t, "")
	a.HTTP = srv.Client()
	if a.saveKey(s, "bad-key") {
		t.Fatal("a rejected key must not count as saved")
	}
	if _, err := a.store().Get("openai"); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("a rejected key was stored")
	}
	if !a.saveKey(s, "good-key") {
		t.Fatal("a working key was not saved")
	}
	if v, _ := a.store().Get("openai"); v != "good-key" {
		t.Fatalf("stored %q", v)
	}
}

func TestServiceResults(t *testing.T) {
	a, _ := testApp(t, "")
	a.Runner = stubRunner{signedIn: false}
	byID := map[string]doctor.Result{}
	for _, r := range a.serviceResults(false) {
		byID[strings.TrimPrefix(r.Check, "service ")] = r
	}
	if byID["claude"].Level != doctor.Warn || doctor.Failed(a.serviceResults(false)) {
		t.Fatalf("a missing sign-in must warn, never fail (a failure would roll an update back): %+v", byID["claude"])
	}
	if byID["exa"].Level != doctor.Warn {
		t.Fatalf("a missing recommended key must warn: %+v", byID["exa"])
	}
	if byID["kie"].Level != doctor.Pass {
		t.Fatalf("a missing optional key must pass: %+v", byID["kie"])
	}
	_ = a.store().Set("exa", "k")
	for _, r := range a.serviceResults(false) {
		if r.Check == "service exa" && (r.Level != doctor.Pass || r.Detail != "key saved") {
			t.Fatalf("saved key: %+v", r)
		}
	}
}

func TestWithKeysPassesOnlyNamedKeys(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	a, _ := testApp(t, "")
	_ = a.store().Set("firecrawl", "fc-key")
	_ = a.store().Set("gemini", "gm-key")
	out := filepath.Join(t.TempDir(), "env")
	code := a.WithKeys([]string{"firecrawl"}, []string{"sh", "-c", "env > " + out})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), "FIRECRAWL_API_KEY=fc-key") || strings.Contains(string(b), "gm-key") {
		t.Fatalf("child env wrong:\n%s", b)
	}
	if code := a.WithKeys([]string{"firecrawl"}, []string{"sh", "-c", "exit 7"}); code != 7 {
		t.Fatalf("exit code not passed through: %d", code)
	}
}
