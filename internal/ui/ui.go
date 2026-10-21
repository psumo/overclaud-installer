// Package ui prints installer progress for a terminal or a double-clicked window. No
// colour when output is not a terminal, when NO_COLOR is set, or in an old Windows console.
package ui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

type UI struct {
	Out   io.Writer
	In    *bufio.Reader
	Quiet bool
	color bool
}

func New(quiet bool) *UI {
	color := os.Getenv("NO_COLOR") == "" && isTerminal(os.Stdout)
	if runtime.GOOS == "windows" && os.Getenv("WT_SESSION") == "" {
		color = false // classic conhost may not render ANSI
	}
	return &UI{Out: os.Stdout, In: bufio.NewReader(os.Stdin), Quiet: quiet, color: color}
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (u *UI) paint(code, s string) string {
	if !u.color {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func (u *UI) Title(s string) {
	if !u.Quiet {
		fmt.Fprintln(u.Out, "\n"+u.paint("1", s))
	}
}

func (u *UI) Step(n, total int, s string) {
	if !u.Quiet {
		fmt.Fprintf(u.Out, "\n%s %s\n", u.paint("36", fmt.Sprintf("[%d/%d]", n, total)), s)
	}
}

func (u *UI) OK(format string, a ...any) {
	if !u.Quiet {
		fmt.Fprintf(u.Out, "  %s %s\n", u.paint("32", "ok"), fmt.Sprintf(format, a...))
	}
}

func (u *UI) Info(format string, a ...any) {
	if !u.Quiet {
		fmt.Fprintf(u.Out, "  %s\n", fmt.Sprintf(format, a...))
	}
}

// Warn and Fail always print, even in quiet mode, so scheduled updates leave a trace.
func (u *UI) Warn(format string, a ...any) {
	fmt.Fprintf(u.Out, "  %s %s\n", u.paint("33", "!"), fmt.Sprintf(format, a...))
}

func (u *UI) Fail(format string, a ...any) {
	fmt.Fprintf(u.Out, "  %s %s\n", u.paint("31", "x"), fmt.Sprintf(format, a...))
}

// Ask prompts and returns the trimmed answer ("" on EOF).
func (u *UI) Ask(prompt string) string {
	fmt.Fprint(u.Out, "  "+prompt)
	line, _ := u.In.ReadString('\n')
	return strings.TrimSpace(line)
}

// Confirm asks a yes/no question; empty input takes def.
func (u *UI) Confirm(prompt string, def bool) bool {
	hint := " [Y/n] "
	if !def {
		hint = " [y/N] "
	}
	a := strings.ToLower(u.Ask(prompt + hint))
	if a == "" {
		return def
	}
	return a == "y" || a == "yes"
}

// Pause keeps a double-clicked window open until the user has read it.
func (u *UI) Pause() {
	fmt.Fprint(u.Out, "\n  Press Enter to close this window.")
	_, _ = u.In.ReadString('\n')
}
