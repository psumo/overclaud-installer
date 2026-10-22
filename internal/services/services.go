// Package services lists the outside services Overclaud's skills, plugins and MCP servers
// use, why each is needed, and how to connect it: a browser sign-in run by the service's
// own tool, or an API key kept in the OS key store (package secrets).
package services

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/psumo/overclaud-installer/internal/prereq"
)

type Kind int

const (
	SignIn Kind = iota // browser sign-in through the service's own tool
	APIKey             // a key the customer pastes; kept in the OS key store
)

type Tier int

const (
	Required Tier = iota
	Recommended
	Optional
)

func (t Tier) String() string {
	return [...]string{"required", "recommended", "optional"}[t]
}

// Probe is a harmless request that only succeeds with a valid key.
type Probe struct {
	URL    string
	Header string // header that carries the key
	Prefix string // e.g. "Bearer "
}

type Service struct {
	ID    string // key store id and `overclaud keys` name
	Name  string
	Why   string // one line, shown before asking
	Where string // where to get the key or how to sign in
	Kind  Kind
	Tier  Tier

	// Sign-ins.
	Check     []string // command that succeeds only when signed in
	CheckWant string   // if set, Check's output must contain this
	Login     []string // command that signs in (opens the browser)
	Manual    string   // sign-in with no command: what to do instead

	// API keys.
	Env     string // variable the consumers read
	Session bool   // exported to Claude Code sessions for skills (`keys session-env`)
	Verify  *Probe

	// Token from another tool instead of the key store (GitHub: `gh auth token`).
	TokenCmd []string
	TokenEnv string
}

// Catalog is every service, in the order the installer asks.
var Catalog = []Service{
	{ID: "claude", Name: "Claude", Kind: SignIn, Tier: Required,
		Why:   "Runs every session and the setup check.",
		Where: "Signs in through your browser with your Claude Pro or Max account.",
		Check: []string{"claude", "auth", "status"}, CheckWant: `"loggedIn": true`,
		Login: []string{"claude", "auth", "login"}},
	{ID: "github", Name: "GitHub", Kind: SignIn, Tier: Recommended,
		Why:      "Lets Claude read your repos and open pull requests and issues for you.",
		Where:    "Signs in through your browser with the GitHub CLI.",
		Check:    []string{"gh", "auth", "status"},
		Login:    []string{"gh", "auth", "login", "--web", "--hostname", "github.com", "--git-protocol", "https"},
		TokenCmd: []string{"gh", "auth", "token"}, TokenEnv: "GITHUB_PERSONAL_ACCESS_TOKEN"},
	{ID: "stripe", Name: "Stripe", Kind: SignIn, Tier: Optional,
		Why:    "Only if you take payments: the Stripe plugin reads your Stripe account.",
		Where:  "Signs in inside Claude Code.",
		Manual: "In Claude Code, run /mcp, choose stripe, then Authenticate."},
	{ID: "semgrep", Name: "Semgrep", Kind: SignIn, Tier: Optional,
		Why:   "Deeper security scans of your code.",
		Where: "Signs in through your browser with the Semgrep CLI.",
		Login: []string{"semgrep", "login"}},

	{ID: "exa", Name: "Exa", Kind: APIKey, Tier: Recommended, Env: "EXA_API_KEY", Session: true,
		Why:   "Web search for research requests.",
		Where: "https://dashboard.exa.ai/api-keys"},
	{ID: "gemini", Name: "Google Gemini", Kind: APIKey, Tier: Recommended, Env: "GEMINI_API_KEY", Session: true,
		Why:    "Lets Claude look at images, video and audio.",
		Where:  "https://aistudio.google.com/apikey",
		Verify: &Probe{URL: "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1", Header: "x-goog-api-key"}},
	{ID: "firecrawl", Name: "Firecrawl", Kind: APIKey, Tier: Optional, Env: "FIRECRAWL_API_KEY", Session: true,
		Why:    "Turns websites into clean text for Claude to read.",
		Where:  "https://www.firecrawl.dev/app/api-keys",
		Verify: &Probe{URL: "https://api.firecrawl.dev/v1/team/credit-usage", Header: "Authorization", Prefix: "Bearer "}},
	{ID: "openai", Name: "OpenAI", Kind: APIKey, Tier: Optional, Env: "OPENAI_API_KEY", Session: true,
		Why:    "Image generation.",
		Where:  "https://platform.openai.com/api-keys",
		Verify: &Probe{URL: "https://api.openai.com/v1/models", Header: "Authorization", Prefix: "Bearer "}},
	{ID: "openrouter", Name: "OpenRouter", Kind: APIKey, Tier: Optional, Env: "OPENROUTER_API_KEY",
		Why:    "Run Claude Code on free and other models with `claude --or`.",
		Where:  "https://openrouter.ai/settings/keys",
		Verify: &Probe{URL: "https://openrouter.ai/api/v1/key", Header: "Authorization", Prefix: "Bearer "}},
	{ID: "artificialanalysis", Name: "Artificial Analysis", Kind: APIKey, Tier: Optional, Env: "AA_API_KEY",
		Why:    "With OpenRouter: ranks the free models for coding and agent work.",
		Where:  "https://artificialanalysis.ai (account, API keys)",
		Verify: &Probe{URL: "https://artificialanalysis.ai/api/v2/data/llms/models", Header: "x-api-key"}},
	{ID: "context7", Name: "Context7", Kind: APIKey, Tier: Optional, Env: "CONTEXT7_API_KEY", Session: true,
		Why:   "Higher limits on up-to-date library docs (works without a key).",
		Where: "https://context7.com/dashboard"},
	{ID: "kie", Name: "KIE AI", Kind: APIKey, Tier: Optional, Env: "KIE_AI_API_KEY", Session: true,
		Why:    "AI video for scroll animations.",
		Where:  "https://kie.ai/api-key",
		Verify: &Probe{URL: "https://api.kie.ai/api/v1/chat/credit", Header: "Authorization", Prefix: "Bearer "}},
}

// ByID finds a service.
func ByID(id string) (Service, bool) {
	for _, s := range Catalog {
		if s.ID == id {
			return s, true
		}
	}
	return Service{}, false
}

// Verdict is the outcome of checking a key or sign-in.
type Verdict int

const (
	Unknown Verdict = iota // could not tell (no probe, network error, unexpected answer)
	Valid
	Invalid
)

func (v Verdict) String() string { return [...]string{"not checked", "working", "rejected"}[v] }

// Doer is the part of *http.Client the probes need.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// VerifyKey sends the service's harmless request with key. 2xx = Valid, 401/403 = Invalid,
// anything else (including no probe) = Unknown, so an outage never rejects a good key.
func VerifyKey(ctx context.Context, s Service, key string, c Doer) Verdict {
	if s.Verify == nil || key == "" {
		return Unknown
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Verify.URL, nil)
	if err != nil {
		return Unknown
	}
	req.Header.Set(s.Verify.Header, s.Verify.Prefix+key)
	resp, err := c.Do(req)
	if err != nil {
		return Unknown
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return Valid
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return Invalid
	}
	return Unknown
}

// SignedIn reports a sign-in's state; Unknown when there is no check or its tool is missing.
func SignedIn(s Service, r prereq.Runner) Verdict {
	if len(s.Check) == 0 {
		return Unknown
	}
	if _, err := r.LookPath(s.Check[0]); err != nil {
		return Unknown
	}
	out, err := r.Output(s.Check[0], s.Check[1:]...)
	if err != nil {
		return Invalid
	}
	if s.CheckWant != "" && !strings.Contains(out, s.CheckWant) {
		return Invalid
	}
	return Valid
}

// ShellQuote quotes v for a POSIX shell `export` line.
func ShellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}
