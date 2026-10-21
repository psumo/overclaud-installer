// overclaud installs and updates the Overclaud harness for Claude Code.
//
//	overclaud                    interactive install (what a double-click runs)
//	overclaud install [--key K]  install or reinstall
//	overclaud update [--quiet] [--repair]
//	overclaud doctor [--live] [--explain]
//	overclaud status
//	overclaud version
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/psumo/overclaud-installer/internal/app"
	"github.com/psumo/overclaud-installer/internal/ui"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := "install"
	doubleClick := len(args) == 0
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	key := fs.String("key", "", "licence key (GC-XXXX-XXXX-XXXX)")
	quiet := fs.Bool("quiet", false, "only print warnings and errors (for scheduled updates)")
	repair := fs.Bool("repair", false, "reinstall the current release even if up to date")
	live := fs.Bool("live", false, "doctor: also run a real `claude -p` session")
	explain := fs.Bool("explain", false, "doctor: ask Claude Code to diagnose failures")
	noExtras := fs.Bool("no-extras", false, "install: skip optional extras (browser automation, scanners)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	u := ui.New(*quiet)
	if doubleClick && runtime.GOOS == "windows" {
		defer u.Pause()
	}
	if cmd == "version" || cmd == "--version" {
		fmt.Println("overclaud installer", app.Version)
		return 0
	}
	a, err := app.New(u)
	if err != nil {
		u.Fail("%v", err)
		return 1
	}

	switch cmd {
	case "install":
		u.Title("Overclaud installer " + app.Version)
		err = a.Run(app.Options{Key: *key, Interactive: true, Extras: !*noExtras && u.Confirm("Also install optional extras (browser automation, security scanner)?", true)})
		if err == nil {
			fmt.Fprintln(u.Out, "\n  Overclaud is installed.")
			fmt.Fprintln(u.Out, "  Next: open a new terminal, run `claude`, and say: \"Read ~/overclaud/FIRST-RUN.md and get me set up.\"")
			fmt.Fprintln(u.Out, "  Check it any time with `overclaud doctor`.")
		}
	case "update":
		err = a.Run(app.Options{Interactive: !*quiet, Repair: *repair, Update: true})
	case "doctor":
		err = a.Doctor(*live, *explain)
	case "status":
		err = a.Status()
	default:
		u.Fail("unknown command %q (install, update, doctor, status, version)", cmd)
		return 2
	}
	if err != nil {
		u.Fail("%v", err)
		return 1
	}
	return 0
}
