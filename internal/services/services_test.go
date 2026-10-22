package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestCatalogIsComplete(t *testing.T) {
	seen := map[string]bool{}
	id := regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	for _, s := range Catalog {
		if seen[s.ID] {
			t.Fatalf("duplicate id %s", s.ID)
		}
		seen[s.ID] = true
		if !id.MatchString(s.ID) || s.Name == "" || s.Why == "" || s.Where == "" {
			t.Fatalf("%s: id, name, why and where are all required", s.ID)
		}
		switch s.Kind {
		case APIKey:
			if s.Env == "" {
				t.Fatalf("%s: an API key needs the variable its consumers read", s.ID)
			}
		case SignIn:
			if len(s.Login) == 0 && s.Manual == "" {
				t.Fatalf("%s: a sign-in needs a login command or manual steps", s.ID)
			}
		}
	}
	for _, want := range []string{"claude", "github", "exa", "gemini"} {
		if !seen[want] {
			t.Fatalf("catalog lacks %s", want)
		}
	}
}

func TestVerifyKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer good":
			w.WriteHeader(200)
		case "Bearer bad":
			w.WriteHeader(401)
		default:
			w.WriteHeader(503)
		}
	}))
	defer srv.Close()
	s := Service{ID: "x", Kind: APIKey, Env: "X", Verify: &Probe{URL: srv.URL, Header: "Authorization", Prefix: "Bearer "}}
	for key, want := range map[string]Verdict{"good": Valid, "bad": Invalid, "flaky": Unknown, "": Unknown} {
		if got := VerifyKey(context.Background(), s, key, srv.Client()); got != want {
			t.Fatalf("key %q: got %v, want %v", key, got, want)
		}
	}
	if got := VerifyKey(context.Background(), Service{ID: "y"}, "k", srv.Client()); got != Unknown {
		t.Fatalf("no probe should be Unknown, got %v", got)
	}
}

type fakeRunner struct {
	out   string
	err   error
	found bool
}

func (f fakeRunner) LookPath(string) (string, error) {
	if !f.found {
		return "", errors.New("not found")
	}
	return "/bin/x", nil
}
func (f fakeRunner) Output(string, ...string) (string, error) { return f.out, f.err }
func (f fakeRunner) Interactive(string, ...string) error      { return nil }

func TestSignedIn(t *testing.T) {
	claude, _ := ByID("claude")
	cases := []struct {
		r    fakeRunner
		want Verdict
	}{
		{fakeRunner{found: true, out: `{"loggedIn": true}`}, Valid},
		{fakeRunner{found: true, out: `{"loggedIn": false}`}, Invalid},
		{fakeRunner{found: true, err: errors.New("exit 1")}, Invalid},
		{fakeRunner{found: false}, Unknown},
	}
	for i, c := range cases {
		if got := SignedIn(claude, c.r); got != c.want {
			t.Fatalf("case %d: got %v, want %v", i, got, c.want)
		}
	}
	stripe, _ := ByID("stripe")
	if got := SignedIn(stripe, fakeRunner{found: true}); got != Unknown {
		t.Fatalf("a sign-in with no check should be Unknown, got %v", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := ShellQuote(`a'b c`); got != `'a'\''b c'` {
		t.Fatalf("got %s", got)
	}
}
