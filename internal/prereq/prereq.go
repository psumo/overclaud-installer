// Package prereq finds the tools Overclaud needs and installs missing ones with the
// platform's package manager (winget, Homebrew, apt/dnf/pacman/zypper) or the tool's own
// official installer. Every install is followed by a fresh version check.
package prereq

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Tool is one prerequisite.
type Tool struct {
	Name     string
	Bins     []string // candidate executables, first found wins
	Args     []string // version flag
	Min      string   // minimum version, "" = any
	Required bool
	Winget   string // winget package id
	Brew     string // Homebrew formula
	Apt      string // Debian/Ubuntu package(s), space separated
	Dnf      string
	Pacman   string
	Zypper   string
	Script   Script   // official installer, used when no package manager entry fits
	OS       []string // limit to these GOOS values; empty = all
}

// For reports whether t applies on goos.
func (t Tool) For(goos string) bool {
	if len(t.OS) == 0 {
		return true
	}
	for _, o := range t.OS {
		if o == goos {
			return true
		}
	}
	return false
}

// Script is an official install command per OS ("" = none).
type Script struct{ Unix, Windows string }

// Tools in install order. Claude Code and uv use their vendors' official installers.
var Tools = []Tool{
	// curl fetches the official Claude Code and uv installers on macOS and Linux.
	{Name: "curl", Bins: []string{"curl"}, Args: []string{"--version"}, Required: true, OS: []string{"linux", "darwin"},
		Brew: "curl", Apt: "curl ca-certificates", Dnf: "curl ca-certificates", Pacman: "curl ca-certificates", Zypper: "curl ca-certificates"},
	{Name: "git", Bins: []string{"git"}, Args: []string{"--version"}, Required: true,
		Winget: "Git.Git", Brew: "git", Apt: "git", Dnf: "git", Pacman: "git", Zypper: "git"},
	{Name: "Node.js", Bins: []string{"node"}, Args: []string{"--version"}, Min: "18.0.0", Required: true,
		Winget: "OpenJS.NodeJS.LTS", Brew: "node", Apt: "nodejs npm", Dnf: "nodejs npm", Pacman: "nodejs npm", Zypper: "nodejs22 npm22"},
	{Name: "Python", Bins: pythonBins(), Args: []string{"--version"}, Min: "3.9.0", Required: true,
		Winget: "Python.Python.3.12", Brew: "python@3.12", Apt: "python3 python3-pip python3-venv", Dnf: "python3 python3-pip", Pacman: "python python-pip", Zypper: "python3 python3-pip"},
	{Name: "Claude Code", Bins: []string{"claude"}, Args: []string{"--version"}, Required: true,
		Script: Script{Unix: "curl -fsSL https://claude.ai/install.sh | bash",
			Windows: "irm https://claude.ai/install.ps1 | iex"}},
	// bun runs claude-mem's memory worker; claude-mem does not install it and fails without it.
	{Name: "bun", Bins: []string{"bun"}, Args: []string{"--version"}, Required: true,
		Winget: "Oven-sh.Bun", Brew: "oven-sh/bun/bun",
		Script: Script{Unix: "curl -fsSL https://bun.sh/install | bash",
			Windows: "irm bun.sh/install.ps1 | iex"}},
	// The GitHub CLI signs in to GitHub and supplies the GitHub MCP server's token.
	{Name: "GitHub CLI", Bins: []string{"gh"}, Args: []string{"--version"},
		Winget: "GitHub.cli", Brew: "gh", Apt: "gh", Dnf: "gh", Pacman: "github-cli", Zypper: "gh"},
	// PyYAML is read by the nightly self-improvement loop. Package managers first: newer
	// Debian/Ubuntu refuse `pip install` outside a virtual environment (PEP 668).
	{Name: "PyYAML", Bins: pythonBins(), Args: []string{"-c", "import yaml; print(yaml.__version__)"},
		Brew: "pyyaml", Apt: "python3-yaml", Dnf: "python3-pyyaml", Pacman: "python-yaml", Zypper: "python3-PyYAML",
		Script: Script{Unix: "python3 -m pip install --user pyyaml",
			Windows: "py -m pip install --user pyyaml"}},
	{Name: "uv", Bins: []string{"uv"}, Args: []string{"--version"},
		Winget: "astral-sh.uv", Brew: "uv",
		Script: Script{Unix: "curl -LsSf https://astral.sh/uv/install.sh | sh",
			Windows: "irm https://astral.sh/uv/install.ps1 | iex"}},
}

func pythonBins() []string {
	if runtime.GOOS == "windows" {
		return []string{"python", "python3", "py"}
	}
	return []string{"python3", "python"}
}

// Status is the result of checking one tool.
type Status struct {
	Tool    Tool
	Path    string
	Version string
	OK      bool
	Problem string
}

// Runner abstracts process execution so tests can fake it.
type Runner interface {
	LookPath(string) (string, error)
	Output(name string, args ...string) (string, error)
	Interactive(name string, args ...string) error
}

// CleanEnv is the environment for child processes. PSModulePath is dropped: when the
// installer runs from PowerShell 7, Windows PowerShell 5.1 would otherwise load PowerShell 7's
// modules and lose built-in cmdlets (seen in CI: Get-FileHash "not recognized" inside
// Anthropic's install.ps1). Windows PowerShell rebuilds its default path when it is unset.
func CleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "PSMODULEPATH=") {
			out = append(out, kv)
		}
	}
	return out
}

type OSRunner struct{}

func (OSRunner) LookPath(n string) (string, error) { return exec.LookPath(n) }

func (OSRunner) Output(name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Env = CleanEnv()
	out, err := c.CombinedOutput()
	return string(out), err
}

func (OSRunner) Interactive(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Env = CleanEnv()
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion pulls the first x.y[.z] out of a version banner.
func ParseVersion(s string) (string, bool) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	if m[3] == "" {
		m[3] = "0"
	}
	return m[1] + "." + m[2] + "." + m[3], true
}

// AtLeast compares dotted versions numerically.
func AtLeast(have, min string) bool {
	if min == "" {
		return true
	}
	h, m := strings.Split(have, "."), strings.Split(min, ".")
	for i := 0; i < 3; i++ {
		hv, mv := part(h, i), part(m, i)
		if hv != mv {
			return hv > mv
		}
	}
	return true
}

func part(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	n, _ := strconv.Atoi(p[i])
	return n
}

// Check finds a tool and its version.
func Check(r Runner, t Tool) Status {
	st := Status{Tool: t}
	for _, b := range t.Bins {
		p, err := r.LookPath(b)
		if err != nil {
			continue
		}
		args := t.Args
		if b == "py" {
			args = append([]string{"-3"}, args...)
		}
		out, err := r.Output(p, args...)
		v, ok := ParseVersion(out)
		if err != nil || !ok {
			// Windows ships a "python" stub that opens the Store; skip it and keep looking.
			continue
		}
		st.Path, st.Version = p, v
		st.OK = AtLeast(v, t.Min)
		if !st.OK {
			st.Problem = fmt.Sprintf("version %s is older than the required %s", v, t.Min)
		}
		return st
	}
	st.Problem = "not found"
	return st
}

// Manager is the package manager chosen for this machine.
type Manager string

const (
	Winget Manager = "winget"
	Brew   Manager = "brew"
	Apt    Manager = "apt"
	Dnf    Manager = "dnf"
	Pacman Manager = "pacman"
	Zypper Manager = "zypper"
	None   Manager = ""
)

// DetectManager picks the package manager for goos.
func DetectManager(r Runner, goos string) Manager {
	order := map[string][]Manager{
		"windows": {Winget},
		"darwin":  {Brew},
		"linux":   {Apt, Dnf, Pacman, Zypper, Brew},
	}[goos]
	bin := map[Manager]string{Winget: "winget", Brew: "brew", Apt: "apt-get", Dnf: "dnf", Pacman: "pacman", Zypper: "zypper"}
	for _, m := range order {
		if _, err := r.LookPath(bin[m]); err == nil {
			return m
		}
	}
	return None
}

// Plan returns the command that would install t on goos with manager m, or an error that
// tells the user what to do by hand.
func Plan(t Tool, goos string, m Manager) ([]string, error) {
	pkg := map[Manager]string{Winget: t.Winget, Brew: t.Brew, Apt: t.Apt, Dnf: t.Dnf, Pacman: t.Pacman, Zypper: t.Zypper}[m]
	if pkg != "" {
		fields := strings.Fields(pkg)
		switch m {
		case Winget:
			return []string{"winget", "install", "--id", pkg, "-e", "--silent", "--accept-package-agreements", "--accept-source-agreements"}, nil
		case Brew:
			return append([]string{"brew", "install"}, fields...), nil
		case Apt:
			return []string{"sudo", "sh", "-c", "apt-get update -qq && apt-get install -y --no-install-recommends " + pkg}, nil
		case Dnf:
			return append([]string{"sudo", "dnf", "install", "-y"}, fields...), nil
		case Pacman:
			return append([]string{"sudo", "pacman", "-S", "--needed", "--noconfirm"}, fields...), nil
		case Zypper:
			return append([]string{"sudo", "zypper", "--non-interactive", "install"}, fields...), nil
		}
	}
	if goos == "windows" && t.Script.Windows != "" {
		return []string{"powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", t.Script.Windows}, nil
	}
	if goos != "windows" && t.Script.Unix != "" {
		return []string{"bash", "-c", t.Script.Unix}, nil
	}
	return nil, errors.New(manualHint(t, goos))
}

func manualHint(t Tool, goos string) string {
	switch {
	case goos == "darwin" && (t.Name == "git" || t.Name == "Python"):
		return "install Apple's command line tools: run `xcode-select --install`, then rerun the installer"
	case goos == "darwin":
		return "install Homebrew from https://brew.sh, then rerun the installer"
	case goos == "windows":
		return "install winget (App Installer from the Microsoft Store), then rerun the installer"
	default:
		return fmt.Sprintf("install %s with your package manager, then rerun the installer", t.Name)
	}
}

// ExtendPath adds the directories installers commonly use, so a tool installed a moment
// ago is found without opening a new terminal.
func ExtendPath(home string) {
	dirs := []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, ".cargo", "bin"), filepath.Join(home, ".bun", "bin")}
	switch runtime.GOOS {
	case "darwin":
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	case "windows":
		la := os.Getenv("LOCALAPPDATA")
		pf := os.Getenv("ProgramFiles")
		dirs = append(dirs,
			filepath.Join(pf, "Git", "cmd"), filepath.Join(pf, "nodejs"),
			filepath.Join(la, "Programs", "Python", "Python312"), filepath.Join(la, "Programs", "Python", "Python312", "Scripts"),
			filepath.Join(la, "Microsoft", "WinGet", "Links"), filepath.Join(home, ".local", "bin"))
		if extra := windowsRegistryPath(); extra != "" {
			dirs = append(dirs, filepath.SplitList(extra)...)
		}
	}
	cur := os.Getenv("PATH")
	have := map[string]bool{}
	for _, d := range filepath.SplitList(cur) {
		have[d] = true
	}
	for _, d := range dirs {
		if d != "" && !have[d] {
			if _, err := os.Stat(d); err == nil {
				cur = cur + string(os.PathListSeparator) + d
				have[d] = true
			}
		}
	}
	os.Setenv("PATH", cur)
}

// windowsRegistryPath reads the machine and user PATH that installers just updated.
func windowsRegistryPath() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	c := exec.Command("powershell", "-NoProfile", "-Command",
		"[Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')")
	c.Env = CleanEnv()
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
