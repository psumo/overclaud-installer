package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/psumo/overclaud-installer/internal/doctor"
	"github.com/psumo/overclaud-installer/internal/prereq"
	"github.com/psumo/overclaud-installer/internal/secrets"
	"github.com/psumo/overclaud-installer/internal/services"
)

// store opens the key store once per run.
func (a *App) store() secrets.Store {
	if a.secrets == nil {
		a.secrets = secrets.Open(a.Paths.Home)
	}
	return a.secrets
}

func (a *App) httpClient() services.Doer {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// ConnectServices is the install step: each sign-in and API key, with why it is needed.
// Non-interactive runs (updates) only report what is missing.
func (a *App) ConnectServices(interactive bool) {
	a.UI.Info("Keys are kept in %s, never in settings files or sent to Overclaud.", a.store().Where())
	for _, s := range services.Catalog {
		switch s.Kind {
		case services.SignIn:
			a.connectSignIn(s, interactive)
		case services.APIKey:
			a.connectKey(s, interactive)
		}
	}
	a.UI.Info("Change any of these later with `overclaud keys`.")
}

func (a *App) connectSignIn(s services.Service, interactive bool) {
	if services.SignedIn(s, a.Runner) == services.Valid {
		a.UI.OK("%s: signed in", s.Name)
		return
	}
	tool := ""
	if len(s.Login) > 0 {
		tool = s.Login[0]
		if _, err := a.Runner.LookPath(tool); err != nil {
			if s.Tier != services.Optional {
				a.UI.Warn("%s: `%s` is not installed, so it cannot sign in yet", s.Name, tool)
			}
			return
		}
	}
	// A browser sign-in needs a person at a terminal; piped or scripted runs only report.
	if !interactive || !a.UI.HasTerminal() {
		if s.Tier != services.Optional {
			a.UI.Warn("%s: not signed in. %s Connect it with `overclaud keys set %s`.", s.Name, s.Why, s.ID)
		}
		return
	}
	a.UI.Info("%s (%s): %s", s.Name, s.Tier, s.Why)
	if s.Manual != "" {
		a.UI.Info("  %s", s.Manual)
		return
	}
	if !a.UI.Confirm(fmt.Sprintf("Sign in to %s now? %s", s.Name, s.Where), s.Tier != services.Optional) {
		return
	}
	if err := a.Runner.Interactive(s.Login[0], s.Login[1:]...); err != nil {
		a.UI.Warn("%s sign-in did not finish: %v", s.Name, err)
		return
	}
	if services.SignedIn(s, a.Runner) == services.Invalid {
		a.UI.Warn("%s still reports not signed in", s.Name)
		return
	}
	a.UI.OK("%s: signed in", s.Name)
}

func (a *App) connectKey(s services.Service, interactive bool) {
	if v, err := a.store().Get(s.ID); err == nil && v != "" {
		switch services.VerifyKey(context.Background(), s, v, a.httpClient()) {
		case services.Invalid:
			a.UI.Warn("%s: the saved key was rejected; replace it with `overclaud keys set %s`", s.Name, s.ID)
		default:
			a.UI.OK("%s: key saved", s.Name)
		}
		return
	}
	if !interactive {
		return
	}
	a.UI.Info("%s (%s): %s", s.Name, s.Tier, s.Why)
	a.UI.Info("  Get a key at %s", s.Where)
	for attempt := 0; attempt < 2; attempt++ {
		key := a.UI.AskSecret(fmt.Sprintf("Paste your %s API key (Enter to skip): ", s.Name))
		if key == "" {
			return
		}
		if a.saveKey(s, key) {
			return
		}
	}
}

// saveKey checks a key with the service, then stores it; false if the service rejected it.
func (a *App) saveKey(s services.Service, key string) bool {
	verdict := services.VerifyKey(context.Background(), s, key, a.httpClient())
	if verdict == services.Invalid {
		a.UI.Warn("%s rejected that key; check you copied all of it", s.Name)
		return false
	}
	if err := a.store().Set(s.ID, key); err != nil {
		a.UI.Warn("Could not save the %s key: %v", s.Name, err)
		return true
	}
	if verdict == services.Valid {
		a.UI.OK("%s: key checked with %s and saved", s.Name, s.Name)
	} else {
		a.UI.OK("%s: key saved (%s cannot be checked without using it)", s.Name, s.Name)
	}
	return true
}

// Keys implements `overclaud keys [list|set ID|test [ID]|remove ID|session-env]`.
func (a *App) Keys(args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	one := func() (services.Service, error) {
		if len(args) != 1 {
			return services.Service{}, fmt.Errorf("usage: overclaud keys %s <service>; services: %s", sub, serviceIDs())
		}
		s, ok := services.ByID(args[0])
		if !ok {
			return s, fmt.Errorf("unknown service %q; services: %s", args[0], serviceIDs())
		}
		return s, nil
	}
	switch sub {
	case "list":
		a.listServices(false)
	case "test":
		if len(args) == 0 {
			a.listServices(true)
			return nil
		}
		s, err := one()
		if err != nil {
			return err
		}
		a.listOne(s, true)
	case "set":
		s, err := one()
		if err != nil {
			return err
		}
		if s.Kind == services.SignIn {
			a.connectSignIn(s, true)
			return nil
		}
		a.UI.Info("%s: %s Get a key at %s", s.Name, s.Why, s.Where)
		key := a.UI.AskSecret(fmt.Sprintf("Paste your %s API key: ", s.Name))
		if key == "" || !a.saveKey(s, key) {
			return errors.New("key not saved")
		}
	case "remove":
		s, err := one()
		if err != nil {
			return err
		}
		if err := a.store().Delete(s.ID); err != nil {
			return err
		}
		a.UI.OK("%s key removed", s.Name)
	case "session-env":
		return a.sessionEnv()
	default:
		return fmt.Errorf("unknown `keys` command %q (list, set, test, remove)", sub)
	}
	return nil
}

func serviceIDs() string {
	ids := make([]string, 0, len(services.Catalog))
	for _, s := range services.Catalog {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ", ")
}

func (a *App) listServices(verify bool) {
	a.UI.Info("Keys are kept in %s.", a.store().Where())
	for _, s := range services.Catalog {
		a.listOne(s, verify)
	}
}

func (a *App) listOne(s services.Service, verify bool) {
	r := a.serviceResult(s, verify)
	line := fmt.Sprintf("%-20s %-11s %s", s.Name, s.Tier, r.Detail)
	switch r.Level {
	case doctor.Pass:
		a.UI.OK("%s", line)
	case doctor.Warn:
		a.UI.Warn("%s", line)
	default:
		a.UI.Fail("%s", line)
	}
}

// serviceResult describes one service for `keys` and doctor. verify sends the probe.
func (a *App) serviceResult(s services.Service, verify bool) doctor.Result {
	r := doctor.Result{Check: "service " + s.ID}
	if s.Kind == services.SignIn {
		switch services.SignedIn(s, a.Runner) {
		case services.Valid:
			r.Detail = "signed in"
		case services.Invalid:
			r.Level, r.Detail = levelFor(s), "not signed in: overclaud keys set "+s.ID
		default:
			r.Detail = "not checked here" + manualHint(s)
		}
		return r
	}
	v, err := a.store().Get(s.ID)
	if err != nil || v == "" {
		r.Level, r.Detail = levelFor(s), "no key: overclaud keys set "+s.ID
		if s.Tier == services.Optional {
			r.Level = doctor.Pass
			r.Detail = "not set (optional)"
		}
		return r
	}
	r.Detail = "key saved"
	if verify {
		switch services.VerifyKey(context.Background(), s, v, a.httpClient()) {
		case services.Valid:
			r.Detail = "key working"
		case services.Invalid:
			r.Level, r.Detail = doctor.Warn, "key rejected: overclaud keys set "+s.ID
		}
	}
	return r
}

// levelFor is Warn even for required services: a doctor failure rolls an update back, and
// an expired sign-in or missing key is fixed by signing in, not by undoing the update.
func levelFor(services.Service) doctor.Level {
	return doctor.Warn
}

func manualHint(s services.Service) string {
	if s.Manual != "" {
		return " (" + s.Manual + ")"
	}
	return ""
}

// sessionEnv writes the skill keys to the file Claude Code names in CLAUDE_ENV_FILE, for a
// SessionStart hook. It prints nothing: hook output can reach the conversation.
func (a *App) sessionEnv() error {
	path := os.Getenv("CLAUDE_ENV_FILE")
	if path == "" {
		return errors.New("CLAUDE_ENV_FILE is not set; this command is for Claude Code's SessionStart hook")
	}
	var b strings.Builder
	for _, s := range services.Catalog {
		if s.Kind != services.APIKey || !s.Session || os.Getenv(s.Env) != "" {
			continue // a variable the customer set themselves wins
		}
		if v, err := a.store().Get(s.ID); err == nil && v != "" {
			fmt.Fprintf(&b, "export %s=%s\n", s.Env, services.ShellQuote(v))
		}
	}
	if b.Len() == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return err
}

// WithKeys runs argv with the keys of the named services in its environment only, for MCP
// servers: `overclaud with-keys github -- mcp-server-github`. Returns the child's exit code.
func (a *App) WithKeys(ids []string, argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: overclaud with-keys <service>[,<service>] -- <command> [args]")
		return 2
	}
	env := prereq.CleanEnv()
	for _, id := range ids {
		s, ok := services.ByID(id)
		if !ok {
			fmt.Fprintf(os.Stderr, "overclaud with-keys: unknown service %q\n", id)
			continue
		}
		name, val := s.Env, ""
		if len(s.TokenCmd) > 0 {
			name = s.TokenEnv
			if out, err := exec.Command(s.TokenCmd[0], s.TokenCmd[1:]...).Output(); err == nil {
				val = strings.TrimSpace(string(out))
			}
		}
		if val == "" {
			if v, err := a.store().Get(s.ID); err == nil {
				val = v
			}
		}
		if val == "" {
			fmt.Fprintf(os.Stderr, "overclaud with-keys: no %s key; connect it with `overclaud keys set %s`\n", s.Name, s.ID)
			continue
		}
		env = append(env, name+"="+val)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "overclaud with-keys: %v\n", err)
		return 127
	}
	return 0
}

// serviceResults is the doctor's view of every service; verify sends the key probes.
func (a *App) serviceResults(verify bool) []doctor.Result {
	out := make([]doctor.Result, 0, len(services.Catalog))
	for _, s := range services.Catalog {
		out = append(out, a.serviceResult(s, verify))
	}
	return out
}
