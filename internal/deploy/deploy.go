// Package deploy copies a verified release into ~/.claude while keeping a customer layer:
// files the customer changed are kept (the new product version goes beside them as
// <file>.overclaud-new), files the product never owned are never touched, and
// settings.json is merged three ways. Every run backs up what it is about to change so
// Rollback can undo it.
package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/psumo/overclaud-installer/internal/manifest"
	"github.com/psumo/overclaud-installer/internal/settingsmerge"
)

const (
	productPrefix = ".claude/"
	settingsRel   = "settings.json"
	NewSuffix     = ".overclaud-new"
)

// State is what the last deploy wrote; it is the "base" of the next three-way decision.
type State struct {
	Version      string            `json:"version"`
	Files        map[string]string `json:"files"`         // path under ~/.claude -> product sha256 we deployed
	SettingsBase json.RawMessage   `json:"settings_base"` // product settings.json as last deployed (before merge)
	Python3      bool              `json:"python3,omitempty"`
}

type Options struct {
	Release    string            // extracted, verified release root
	Manifest   manifest.Manifest // its manifest
	ClaudeDir  string            // ~/.claude
	Prev       State             // previous State (zero value on first install)
	BackupRoot string            // where to keep backups
	Python3    bool              // rewrite hook commands from python to python3
}

type Report struct {
	Written     []string
	Unchanged   int
	Conflicts   []string // customer-edited product files; new version written beside them
	KeptRemoved []string // dropped from the product but customer-edited, so kept
	Deleted     []string
	Backup      string
}

// Deploy applies the release and returns the new State.
func Deploy(o Options) (State, Report, error) {
	var rep Report
	st := State{Version: o.Manifest.Version, Files: map[string]string{}, Python3: o.Python3}
	bk, err := newBackup(o.BackupRoot)
	if err != nil {
		return st, rep, err
	}
	rep.Backup = bk.dir

	inNew := map[string]bool{}
	for _, f := range o.Manifest.Files {
		if !strings.HasPrefix(f.Path, productPrefix) {
			continue
		}
		rel := strings.TrimPrefix(f.Path, productPrefix)
		inNew[rel] = true
		src := filepath.Join(o.Release, filepath.FromSlash(f.Path))
		if rel == settingsRel {
			if err := deploySettings(o, src, bk, &st, &rep); err != nil {
				return st, rep, err
			}
			continue
		}
		st.Files[rel] = f.SHA256
		dst := filepath.Join(o.ClaudeDir, filepath.FromSlash(rel))
		cur, _, statErr := manifest.HashFile(dst)
		prevSum, hadPrev := o.Prev.Files[rel]
		switch {
		case errors.Is(statErr, fs.ErrNotExist):
			if err := bk.created(rel); err != nil {
				return st, rep, err
			}
			if err := copyFile(src, dst, f.Exec); err != nil {
				return st, rep, err
			}
			rep.Written = append(rep.Written, rel)
		case statErr != nil:
			return st, rep, statErr
		case cur == f.SHA256:
			rep.Unchanged++
		case !hadPrev || cur == prevSum:
			if err := bk.save(o.ClaudeDir, rel); err != nil {
				return st, rep, err
			}
			if err := copyFile(src, dst, f.Exec); err != nil {
				return st, rep, err
			}
			rep.Written = append(rep.Written, rel)
		default:
			if err := bk.created(rel + NewSuffix); err != nil {
				return st, rep, err
			}
			if err := copyFile(src, dst+NewSuffix, f.Exec); err != nil {
				return st, rep, err
			}
			rep.Conflicts = append(rep.Conflicts, rel)
		}
	}

	for rel, prevSum := range o.Prev.Files {
		if inNew[rel] {
			continue
		}
		dst := filepath.Join(o.ClaudeDir, filepath.FromSlash(rel))
		cur, _, err := manifest.HashFile(dst)
		if err != nil {
			continue // already gone
		}
		if cur != prevSum {
			rep.KeptRemoved = append(rep.KeptRemoved, rel)
			continue
		}
		if err := bk.save(o.ClaudeDir, rel); err != nil {
			return st, rep, err
		}
		if err := os.Remove(dst); err != nil {
			return st, rep, err
		}
		removeEmptyParents(filepath.Dir(dst), o.ClaudeDir)
		rep.Deleted = append(rep.Deleted, rel)
	}
	for _, l := range [][]string{rep.Written, rep.Conflicts, rep.KeptRemoved, rep.Deleted} {
		sort.Strings(l)
	}
	return st, rep, bk.finish()
}

func deploySettings(o Options, src string, bk *backup, st *State, rep *Report) error {
	productRaw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var product, base, customer map[string]any
	if err := json.Unmarshal(productRaw, &product); err != nil {
		return fmt.Errorf("release settings.json: %w", err)
	}
	if len(o.Prev.SettingsBase) > 0 {
		_ = json.Unmarshal(o.Prev.SettingsBase, &base)
	}
	dst := filepath.Join(o.ClaudeDir, settingsRel)
	var norm settingsmerge.Normalize
	if o.Python3 {
		norm = settingsmerge.PythonToPython3
	}
	var merged map[string]any
	if raw, err := os.ReadFile(dst); err == nil && json.Unmarshal(raw, &customer) == nil {
		if err := bk.save(o.ClaudeDir, settingsRel); err != nil {
			return err
		}
		merged = settingsmerge.Merge(base, product, customer, norm)
	} else {
		if errors.Is(err, fs.ErrNotExist) {
			if err := bk.created(settingsRel); err != nil {
				return err
			}
		} else if err := bk.save(o.ClaudeDir, settingsRel); err != nil {
			return err
		}
		merged = settingsmerge.Merge(nil, product, map[string]any{}, norm)
	}
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(dst, append(out, '\n'), 0o600); err != nil { // may hold API keys in env
		return err
	}
	st.SettingsBase = productRaw
	rep.Written = append(rep.Written, settingsRel)
	return nil
}

// --- backup and rollback ---

type backup struct {
	dir   string
	added []string // files this deploy creates, removed again on rollback
}

func newBackup(root string) (*backup, error) {
	dir := filepath.Join(root, time.Now().UTC().Format("20060102-150405.000000000"))
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o700); err != nil {
		return nil, err
	}
	return &backup{dir: dir}, nil
}

func (b *backup) save(claudeDir, rel string) error {
	src := filepath.Join(claudeDir, filepath.FromSlash(rel))
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return copyFile(src, filepath.Join(b.dir, "files", filepath.FromSlash(rel)), info.Mode()&0o111 != 0)
}

func (b *backup) created(rel string) error {
	b.added = append(b.added, rel)
	return nil
}

func (b *backup) finish() error {
	data, err := json.Marshal(b.added)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(b.dir, "created.json"), data, 0o600)
}

// Rollback undoes one Deploy: removes files it created and restores files it changed.
func Rollback(backupDir, claudeDir string) error {
	var created []string
	if data, err := os.ReadFile(filepath.Join(backupDir, "created.json")); err == nil {
		_ = json.Unmarshal(data, &created)
	}
	for _, rel := range created {
		if err := manifest.SafeRel(rel); err != nil {
			return err
		}
		p := filepath.Join(claudeDir, filepath.FromSlash(rel))
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		removeEmptyParents(filepath.Dir(p), claudeDir)
	}
	files := filepath.Join(backupDir, "files")
	return filepath.WalkDir(files, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(files, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(p, filepath.Join(claudeDir, rel), info.Mode()&0o111 != 0)
	})
}

// --- file helpers ---

func copyFile(src, dst string, exec bool) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := fs.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	return writeAtomic(dst, data, mode)
}

// writeAtomic replaces dst via a temp file and rename. An existing file keeps its permission
// bits (plus exec if the product needs it), so a customer's 0600 file never becomes readable
// by others; mode applies only to new files.
func writeAtomic(dst string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info, err := os.Stat(dst); err == nil {
		mode = info.Mode().Perm() | (mode & 0o111)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".overclaud-tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, dst)
}

func removeEmptyParents(dir, stop string) {
	stop = filepath.Clean(stop)
	for d := filepath.Clean(dir); d != stop && strings.HasPrefix(d, stop); d = filepath.Dir(d) {
		if os.Remove(d) != nil {
			return
		}
	}
}
