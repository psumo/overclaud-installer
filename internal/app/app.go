// Package app runs the installer's commands: install, update, doctor and status.
package app

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/psumo/overclaud-installer/internal/deploy"
	"github.com/psumo/overclaud-installer/internal/doctor"
	"github.com/psumo/overclaud-installer/internal/licence"
	"github.com/psumo/overclaud-installer/internal/manifest"
	"github.com/psumo/overclaud-installer/internal/payload"
	"github.com/psumo/overclaud-installer/internal/prereq"
	"github.com/psumo/overclaud-installer/internal/schedule"
	"github.com/psumo/overclaud-installer/internal/secrets"
	"github.com/psumo/overclaud-installer/internal/services"
	"github.com/psumo/overclaud-installer/internal/ui"
)

// ReleasePublicKey verifies release manifests. Hex Ed25519 public key; the matching private
// key never leaves the maintainer's machine. Test builds override it with -ldflags -X.
var ReleasePublicKey = ""

// Version of this installer, set at build time.
var Version = "dev"

type App struct {
	UI      *ui.UI
	Paths   licence.Layout
	API     licence.Client
	Runner  prereq.Runner
	PubKey  ed25519.PublicKey
	NoSched bool          // tests and packagers: do not register the daily job
	NoSelf  bool          // tests: do not copy the binary into place
	HTTP    services.Doer // key checks; nil means a default client

	secrets secrets.Store // opened on first use
}

// New builds the App for the current user.
func New(u *ui.UI) (*App, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	pub, err := hex.DecodeString(ReleasePublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("this installer build has no release signing key; download it again from overclaud.dev")
	}
	base := licence.DefaultBase
	if v := os.Getenv("OVERCLAUD_API"); v != "" {
		// For tests and staging. Downloads are still checked against the embedded key, and
		// plain HTTP is only allowed to this machine.
		if !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://127.0.0.1:") && !strings.HasPrefix(v, "http://localhost:") {
			return nil, errors.New("OVERCLAUD_API must be an https:// URL")
		}
		base = v
	}
	return &App{UI: u, Paths: licence.Paths(home), API: licence.Client{Base: base, Version: Version},
		Runner: prereq.OSRunner{}, PubKey: ed25519.PublicKey(pub),
		// For packagers and tests: install without registering the daily update job.
		NoSched: os.Getenv("OVERCLAUD_NO_SCHEDULE") == "1"}, nil
}

// --- prerequisites ---

func (a *App) prerequisites(interactive bool) error {
	prereq.ExtendPath(a.Paths.Home)
	mgr := prereq.DetectManager(a.Runner, runtime.GOOS)
	var missing []string
	for _, t := range prereq.Tools {
		if !t.For(runtime.GOOS) {
			continue
		}
		st := prereq.Check(a.Runner, t)
		if st.OK {
			a.UI.OK("%s %s", t.Name, st.Version)
			continue
		}
		a.UI.Info("%s: %s", t.Name, st.Problem)
		if !interactive {
			if t.Required {
				missing = append(missing, t.Name)
			}
			continue
		}
		if err := a.installTool(t, mgr); err != nil {
			if t.Required {
				a.UI.Fail("%s could not be installed: %v", t.Name, err)
				missing = append(missing, t.Name)
			} else {
				a.UI.Warn("%s (optional) not installed: %v", t.Name, err)
			}
			continue
		}
		prereq.ExtendPath(a.Paths.Home)
		st = prereq.Check(a.Runner, t)
		if !st.OK && t.Name == "Node.js" && runtime.GOOS != "windows" {
			a.UI.Info("The packaged Node.js is too old; installing Node.js 22 LTS from nodejs.org")
			if bin, err := prereq.InstallNodeTarball(filepath.Join(a.Paths.OverclaudHome, "tools", "node"), payload.Extract); err == nil {
				addToShellPath(a.Paths.Home, bin)
				st = prereq.Check(a.Runner, t)
			} else {
				a.UI.Warn("Node.js download failed: %v", err)
			}
		}
		if st.OK {
			a.UI.OK("%s %s installed", t.Name, st.Version)
		} else if t.Required {
			a.UI.Fail("%s still missing after install: %s", t.Name, st.Problem)
			missing = append(missing, t.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required tools missing: %s. Install them, open a new terminal and run `overclaud install` again", strings.Join(missing, ", "))
	}
	return nil
}

func (a *App) installTool(t prereq.Tool, mgr prereq.Manager) error {
	cmd, err := prereq.Plan(t, runtime.GOOS, mgr)
	if err != nil {
		return err
	}
	a.UI.Info("Installing %s: %s", t.Name, strings.Join(cmd, " "))
	return a.Runner.Interactive(cmd[0], cmd[1:]...)
}

// --- licence and download ---

func (a *App) licenceKey(flagKey string, interactive bool) (string, error) {
	if flagKey != "" {
		if k, ok := licence.NormalizeKey(flagKey); ok {
			return k, nil
		}
		return "", errors.New("licence key format is GC-XXXX-XXXX-XXXX")
	}
	if rec, err := licence.ReadLicence(a.Paths); err == nil {
		if k, ok := licence.NormalizeKey(rec.Key); ok {
			return k, nil
		}
	}
	if !interactive {
		return "", errors.New("no licence key found; run `overclaud install` once interactively")
	}
	for i := 0; i < 3; i++ {
		k, ok := licence.NormalizeKey(a.UI.Ask("Licence key (from your purchase email, GC-XXXX-XXXX-XXXX): "))
		if ok {
			return k, nil
		}
		a.UI.Warn("That does not look like a key. It has the form GC-XXXX-XXXX-XXXX.")
	}
	return "", errors.New("no valid licence key entered")
}

// stage downloads, extracts and verifies the release, returning its directory.
func (a *App) stage(dl licence.Download) (string, manifest.Manifest, error) {
	var m manifest.Manifest
	staging := filepath.Join(a.Paths.OverclaudHome, "staging")
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return "", m, err
	}
	tgz := filepath.Join(staging, "release.tar.gz")
	last := int64(0)
	err := payload.Fetch(dl.URL, tgz, func(n int64) {
		if n-last >= 20<<20 {
			last = n
			a.UI.Info("%d MB downloaded", n>>20)
		}
	})
	if err != nil {
		return "", m, err
	}
	dir := filepath.Join(staging, "release")
	f, err := os.Open(tgz)
	if err != nil {
		return "", m, err
	}
	err = payload.Extract(f, dir)
	f.Close()
	_ = os.Remove(tgz)
	if err != nil {
		return "", m, err
	}
	man, err := payload.Verify(dir, a.PubKey)
	if err != nil {
		return "", m, err
	}
	return dir, man, nil
}

// --- install / update ---

type Options struct {
	Key         string
	Interactive bool
	Repair      bool     // redeploy even when already up to date
	Live        bool     // doctor runs a real claude -p session
	Extras      []string // optional groups to install (prereq.Extras ids)
	Update      bool
	// AllowDowngrade accepts a signed release older than the installed one. Without it an
	// old (validly signed) release cannot be used to roll a customer back.
	AllowDowngrade bool
}

// Run is install and update in one pipeline.
func (a *App) Run(o Options) error {
	total := 9
	a.UI.Step(1, total, "Checking prerequisites")
	if err := a.prerequisites(o.Interactive); err != nil {
		return err
	}

	a.UI.Step(2, total, "Checking your licence")
	key, err := a.licenceKey(o.Key, o.Interactive)
	if err != nil {
		return err
	}
	dl, err := a.API.RequestDownload(key, licence.DeviceID(a.Paths))
	if err != nil {
		return err
	}
	a.UI.OK("Licensed to %s (%s)", dl.Email, dl.Plan)

	prev := loadState(a.Paths.OverclaudHome)
	if o.Update && !o.Repair && dl.SHA != "" && dl.SHA == prev.Commit && len(prev.Deploy.Files) > 0 {
		a.UI.OK("Already up to date (%s, version %s)", dl.SHA, prev.Deploy.Version)
		return nil
	}

	a.UI.Step(3, total, "Downloading and verifying the release")
	dir, man, err := a.stage(dl)
	if err != nil {
		return err
	}
	if prev.Deploy.Version != "" && !o.AllowDowngrade {
		cmp, ok := compareVersions(man.Version, prev.Deploy.Version)
		switch {
		case !ok:
			return fmt.Errorf("cannot compare release version %q with installed %q; refusing it (use --allow-downgrade to force)", man.Version, prev.Deploy.Version)
		case cmp < 0:
			return fmt.Errorf("the server offered version %s, older than your installed %s; refusing to downgrade (use --allow-downgrade to force)", man.Version, prev.Deploy.Version)
		}
	}
	a.UI.OK("Version %s, %d files, signature verified", man.Version, len(man.Files))

	a.UI.Step(4, total, "Installing into ~/.claude (your own files and edits are kept)")
	python3 := needsPython3(a.Runner)
	st, rep, err := deploy.Deploy(deploy.Options{Release: dir, Manifest: man, ClaudeDir: a.Paths.ClaudeDir,
		Prev: prev.Deploy, BackupRoot: filepath.Join(a.Paths.OverclaudHome, "backups"), Python3: python3})
	if err != nil {
		if rep.Backup != "" {
			_ = deploy.Rollback(rep.Backup, a.Paths.ClaudeDir)
		}
		return fmt.Errorf("install failed and was undone: %w", err)
	}
	a.UI.OK("%d files written, %d unchanged, %d removed", len(rep.Written), rep.Unchanged, len(rep.Deleted))
	if python3 {
		a.UI.OK("Hooks set to run with python3 (no `python` on this machine)")
	}
	for _, c := range rep.Conflicts {
		a.UI.Warn("Kept your edited %s; the new version is beside it as %s%s", c, c, deploy.NewSuffix)
	}
	oldProduct, err := a.swapProductDir(dir)
	if err != nil {
		_ = deploy.Rollback(rep.Backup, a.Paths.ClaudeDir)
		return fmt.Errorf("install failed and was undone: %w", err)
	}

	bin, err := a.selfPath()
	if err != nil {
		a.UI.Warn("Could not put `overclaud` on your PATH: %v", err)
	}

	a.UI.Step(5, total, "Setting up MCP servers and plugins")
	a.SetupClaude(a.Paths.ProductDir, bin)

	if !o.Update {
		a.UI.Step(6, total, "Connecting your services")
		a.ConnectServices(o.Interactive)
	}

	a.UI.Step(7, total, "Checking the install")
	results := doctor.Run(doctor.Options{Home: a.Paths.Home, ClaudeDir: a.Paths.ClaudeDir, State: st, Runner: a.Runner, Live: o.Live})
	results = append(results, a.serviceResults(false)...)
	a.printResults(results)
	if doctor.Failed(results) && o.Update {
		a.UI.Fail("The update failed its checks; restoring the previous version")
		_ = deploy.Rollback(rep.Backup, a.Paths.ClaudeDir)
		a.restoreProductDir(oldProduct)
		return errors.New("update rolled back; your previous install is unchanged. Run `overclaud doctor --explain` for help")
	}

	rec := Record{Deploy: st, Commit: dl.SHA, Conflicts: rep.Conflicts, LastBackup: rep.Backup}
	if err := saveState(a.Paths.OverclaudHome, rec); err != nil {
		return err
	}
	prevLic, _ := licence.ReadLicence(a.Paths)
	if err := licence.WriteLicence(a.Paths, licence.Record{Key: key, Email: dl.Email, Plan: dl.Plan, Version: dl.SHA, InstalledAt: prevLic.InstalledAt}); err != nil {
		return err
	}

	if !doctor.Failed(results) {
		a.UI.Step(8, total, "Checking it works on this computer")
		a.DeviceCheck(o.Interactive, bin)
	}

	a.UI.Step(9, total, "Finishing")
	if !a.NoSelf && !a.NoSched && bin != "" {
		if how, err := schedule.Install(bin, a.Paths.Home, licence.DeviceID(a.Paths)); err != nil {
			a.UI.Warn("Automatic updates not scheduled: %v", err)
		} else {
			a.UI.OK("Automatic updates: %s", how)
		}
	}
	if len(o.Extras) > 0 {
		a.runExtras(o.Extras)
	}
	if doctor.Failed(results) {
		a.UI.Warn("Some checks failed (above). Run `overclaud doctor --explain` for a diagnosis.")
	}
	return nil
}

// swapProductDir makes the verified release the new ~/overclaud and keeps the old one so
// a failed update can put it back.
func (a *App) swapProductDir(release string) (string, error) {
	prev := filepath.Join(a.Paths.OverclaudHome, "previous-product")
	_ = os.RemoveAll(prev)
	if _, err := os.Stat(a.Paths.ProductDir); err == nil {
		if err := os.Rename(a.Paths.ProductDir, prev); err != nil {
			return "", err
		}
	} else {
		prev = ""
	}
	if err := os.Rename(release, a.Paths.ProductDir); err != nil {
		if prev != "" {
			_ = os.Rename(prev, a.Paths.ProductDir)
		}
		return "", err
	}
	return prev, nil
}

func (a *App) restoreProductDir(prev string) {
	if prev == "" {
		return
	}
	_ = os.RemoveAll(a.Paths.ProductDir)
	_ = os.Rename(prev, a.Paths.ProductDir)
}

// runExtras installs the optional groups the customer chose; failures only warn.
func (a *App) runExtras(ids []string) {
	mgr := prereq.DetectManager(a.Runner, runtime.GOOS)
	for _, id := range ids {
		g, ok := prereq.ExtraByID(id)
		if !ok {
			continue
		}
		for _, tl := range g.Tools {
			if !tl.For(runtime.GOOS) {
				continue
			}
			if st := prereq.Check(a.Runner, tl); st.OK {
				a.UI.OK("%s %s", tl.Name, st.Version)
				continue
			}
			if err := a.installTool(tl, mgr); err != nil {
				a.UI.Warn("%s (%s) not installed: %v", tl.Name, g.Name, err)
				continue
			}
			prereq.ExtendPath(a.Paths.Home)
			if st := prereq.Check(a.Runner, tl); st.OK {
				a.UI.OK("%s %s installed", tl.Name, st.Version)
			} else {
				a.UI.Warn("%s (%s) still missing after install", tl.Name, g.Name)
			}
		}
	}
}

func needsPython3(r prereq.Runner) bool {
	if p, err := r.LookPath("python"); err == nil {
		if out, err := r.Output(p, "--version"); err == nil && strings.Contains(out, "Python 3") {
			return false
		}
	}
	_, err := r.LookPath("python3")
	return err == nil
}

func (a *App) printResults(rs []doctor.Result) {
	for _, r := range rs {
		switch r.Level {
		case doctor.Pass:
			a.UI.OK("%s: %s", r.Check, r.Detail)
		case doctor.Warn:
			a.UI.Warn("%s: %s", r.Check, r.Detail)
		default:
			a.UI.Fail("%s: %s", r.Check, r.Detail)
		}
	}
}

// Doctor runs the checks on the current install.
func (a *App) Doctor(live, explain, device bool) error {
	if device {
		bin, _ := os.Executable()
		a.DeviceCheck(true, bin)
		return nil
	}
	prereq.ExtendPath(a.Paths.Home)
	st := loadState(a.Paths.OverclaudHome)
	o := doctor.Options{Home: a.Paths.Home, ClaudeDir: a.Paths.ClaudeDir, State: st.Deploy, Runner: a.Runner, Live: live}
	rs := doctor.Run(o)
	rs = append(rs, a.serviceResults(true)...)
	a.printResults(rs)
	if !doctor.Failed(rs) {
		a.UI.OK("Everything checks out.")
		return nil
	}
	if explain {
		a.UI.Info("Asking Claude Code to diagnose the failures...")
		advice, err := doctor.Explain(o, rs)
		if err != nil {
			a.UI.Warn("Could not get a diagnosis: %v", err)
		} else {
			fmt.Fprintln(a.UI.Out, "\n"+advice)
		}
	}
	return errors.New("some checks failed")
}

// Status prints what is installed.
func (a *App) Status() error {
	st := loadState(a.Paths.OverclaudHome)
	lic, lerr := licence.ReadLicence(a.Paths)
	if len(st.Deploy.Files) == 0 {
		a.UI.Warn("Overclaud is not installed by this installer yet. Run `overclaud install`.")
		return nil
	}
	a.UI.OK("Overclaud %s (release %s), installer %s", st.Deploy.Version, st.Commit, Version)
	if lerr == nil {
		a.UI.OK("Licensed to %s (%s)", lic.Email, lic.Plan)
	}
	a.UI.Info("Last installed: %s", st.UpdatedAt)
	var pending []string
	for _, c := range st.Conflicts {
		if _, err := os.Stat(filepath.Join(a.Paths.ClaudeDir, filepath.FromSlash(c)+deploy.NewSuffix)); err == nil {
			pending = append(pending, c)
		}
	}
	sort.Strings(pending)
	for _, c := range pending {
		a.UI.Warn("Your edited %s has a newer product version at %s%s", c, c, deploy.NewSuffix)
	}
	return nil
}
