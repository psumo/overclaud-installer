package app

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/psumo/overclaud-installer/internal/prereq"
)

// installSelf puts this binary where `overclaud` will be found later and returns its path.
// Package-manager installs (/usr/bin, /usr/local/bin, /opt) are left where they are.
// selfPath is where `overclaud` lives once installed: the copy installSelf makes, or in
// tests (NoSelf) the running binary.
func (a *App) selfPath() (string, error) {
	if a.NoSelf {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	}
	return a.installSelf()
}

func (a *App) installSelf() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	var dir string
	switch runtime.GOOS {
	case "windows":
		dir = filepath.Join(os.Getenv("LOCALAPPDATA"), "Overclaud")
	default:
		for _, managed := range []string{"/usr/bin/", "/usr/local/bin/", "/opt/"} {
			if strings.HasPrefix(exe, managed) {
				return exe, nil
			}
		}
		dir = filepath.Join(a.Paths.Home, ".local", "bin")
	}
	name := "overclaud"
	if runtime.GOOS == "windows" {
		name = "overclaud.exe"
	}
	dst := filepath.Join(dir, name)
	if !samePath(exe, dst) {
		if err := copyExecutable(exe, dst); err != nil {
			return "", err
		}
	}
	if runtime.GOOS == "windows" {
		if err := addWindowsUserPath(dir); err != nil {
			return dst, err
		}
	} else {
		addToShellPath(a.Paths.Home, dir)
	}
	a.UI.OK("`overclaud` installed at %s", dst)
	return dst, nil
}

func samePath(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}

func copyExecutable(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if runtime.GOOS == "windows" {
		// A running .exe cannot be overwritten but can be renamed out of the way.
		_ = os.Remove(dst + ".old")
		_ = os.Rename(dst, dst+".old")
	}
	return os.Rename(tmp, dst)
}

// addToShellPath makes dir available in new terminals by appending one marked line to the
// user's shell start-up files (only those that exist, plus ~/.profile).
func addToShellPath(home, dir string) {
	if strings.Contains(os.Getenv("PATH"), dir) && dir == filepath.Join(home, ".local", "bin") {
		return
	}
	line := fmt.Sprintf("\nexport PATH=\"%s:$PATH\"  # added by overclaud\n", dir)
	for _, rc := range []string{".profile", ".bashrc", ".zshrc"} {
		p := filepath.Join(home, rc)
		if rc != ".profile" {
			if _, err := os.Stat(p); err != nil {
				continue
			}
		}
		data, _ := os.ReadFile(p)
		if strings.Contains(string(data), dir) {
			continue
		}
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			continue
		}
		_, _ = f.WriteString(line)
		f.Close()
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// addWindowsUserPath appends dir to the user's PATH in the registry (no 1024-char setx limit).
func addWindowsUserPath(dir string) error {
	// The directory goes in as a single-quoted PowerShell literal ('' escapes a quote).
	lit := "'" + strings.ReplaceAll(dir, "'", "''") + "'"
	script := `& { param($d) $p = [Environment]::GetEnvironmentVariable('Path','User');` +
		` if (-not $p) { $p = '' }; if (($p -split ';') -notcontains $d) {` +
		` [Environment]::SetEnvironmentVariable('Path', ($p.TrimEnd(';') + ';' + $d).TrimStart(';'), 'User') } } ` + lit
	c := exec.Command("powershell", "-NoProfile", "-Command", script)
	c.Env = prereq.CleanEnv()
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("could not add %s to PATH: %s", dir, strings.TrimSpace(string(out)))
	}
	os.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+dir)
	return nil
}
