package prereq

import (
	"errors"
	"strings"
	"testing"
)

type fake struct {
	paths map[string]string
	out   map[string]string
}

func (f fake) LookPath(n string) (string, error) {
	if p, ok := f.paths[n]; ok {
		return p, nil
	}
	return "", errors.New("not found")
}

func (f fake) Output(name string, args ...string) (string, error) {
	if o, ok := f.out[name]; ok {
		return o, nil
	}
	return "", errors.New("fail")
}

func (f fake) Interactive(string, ...string) error { return nil }

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]string{
		"git version 2.43.0":           "2.43.0",
		"v20.11.1":                     "20.11.1",
		"Python 3.12.3":                "3.12.3",
		"2.1.306 (Claude Code)":        "2.1.306",
		"uv 0.4.18 (Homebrew 2024-09)": "0.4.18",
		"node 18.0":                    "18.0.0",
	} {
		if got, ok := ParseVersion(in); !ok || got != want {
			t.Fatalf("%q -> %q", in, got)
		}
	}
}

func TestAtLeast(t *testing.T) {
	cases := []struct {
		have, min string
		want      bool
	}{{"20.11.1", "18.0.0", true}, {"18.0.0", "18.0.0", true}, {"16.20.2", "18.0.0", false}, {"3.10.0", "3.9.0", true}, {"3.8.18", "3.9.0", false}}
	for _, c := range cases {
		if AtLeast(c.have, c.min) != c.want {
			t.Fatalf("%v", c)
		}
	}
}

func TestCheckSkipsBrokenCandidatesAndFlagsOldVersions(t *testing.T) {
	py := Tool{Name: "Python", Bins: []string{"python3", "python"}, Args: []string{"--version"}, Min: "3.9.0"}
	r := fake{paths: map[string]string{"python3": "/x/python3", "python": "/x/python"},
		out: map[string]string{"/x/python": "Python 3.11.2"}} // python3 is a broken stub
	st := Check(r, py)
	if !st.OK || st.Path != "/x/python" {
		t.Fatalf("%+v", st)
	}
	node := Tool{Name: "Node.js", Bins: []string{"node"}, Args: []string{"--version"}, Min: "18.0.0"}
	st = Check(fake{paths: map[string]string{"node": "/n"}, out: map[string]string{"/n": "v16.20.2"}}, node)
	if st.OK || !strings.Contains(st.Problem, "older") {
		t.Fatalf("%+v", st)
	}
}

func TestDetectManagerAndPlan(t *testing.T) {
	r := fake{paths: map[string]string{"apt-get": "/usr/bin/apt-get"}}
	if m := DetectManager(r, "linux"); m != Apt {
		t.Fatalf("got %q", m)
	}
	git := Tools[1]
	cmd, err := Plan(git, "linux", Apt)
	if err != nil || cmd[0] != "sudo" || !strings.Contains(strings.Join(cmd, " "), "apt-get install -y --no-install-recommends git") {
		t.Fatalf("%v %v", cmd, err)
	}
	claude := Tools[4]
	cmd, _ = Plan(claude, "windows", Winget)
	if cmd[0] != "powershell" || !strings.Contains(cmd[len(cmd)-1], "claude.ai/install.ps1") {
		t.Fatalf("claude on windows should use the official installer: %v", cmd)
	}
	cmd, _ = Plan(claude, "darwin", Brew)
	if cmd[0] != "bash" || !strings.Contains(cmd[2], "claude.ai/install.sh") {
		t.Fatalf("%v", cmd)
	}
	if _, err := Plan(git, "darwin", None); err == nil || !strings.Contains(err.Error(), "xcode-select") {
		t.Fatalf("mac without brew should explain xcode-select: %v", err)
	}
}

func TestNodeArchivePicksThisPlatform(t *testing.T) {
	sums := "aaa  node-v22.11.0-darwin-arm64.tar.gz\nbbb  node-v22.11.0-linux-x64.tar.gz\nccc  node-v22.11.0-linux-x64.tar.xz\n"
	name, sum, err := NodeArchive(sums, "linux", "amd64")
	if err != nil || name != "node-v22.11.0-linux-x64.tar.gz" || sum != "bbb" {
		t.Fatalf("%q %q %v", name, sum, err)
	}
	if _, _, err := NodeArchive(sums, "windows", "amd64"); err == nil {
		t.Fatal("windows should use winget, not the tarball")
	}
}

func TestCleanEnvDropsPSModulePath(t *testing.T) {
	t.Setenv("PSModulePath", `C:\Program Files\PowerShell\7\Modules`)
	t.Setenv("OVERCLAUD_KEEP", "1")
	env := strings.Join(CleanEnv(), "\n")
	if strings.Contains(strings.ToUpper(env), "PSMODULEPATH=") || !strings.Contains(env, "OVERCLAUD_KEEP=1") {
		t.Fatalf("env not cleaned correctly")
	}
}
