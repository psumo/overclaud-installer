// Package manifest lists every file in a release with its SHA-256 and signs the list,
// so the installer can prove a download is exactly what the maintainer built.
package manifest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Paths of the manifest and its signature inside a release. They are not listed in
// the manifest itself.
const (
	FileName = "sync/manifest.json"
	SigName  = "sync/manifest.sig"
)

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Exec   bool   `json:"exec,omitempty"`
}

type Manifest struct {
	Version string `json:"version"`
	Files   []File `json:"files"`
}

// Build hashes every regular file under root (skipping .git and the manifest files).
func Build(root, version string) (Manifest, error) {
	m := Manifest{Version: version}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || rel == FileName || rel == SigName {
			return nil
		}
		sum, size, err := HashFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		m.Files = append(m.Files, File{Path: rel, SHA256: sum, Size: size, Exec: info.Mode()&0o111 != 0})
		return nil
	})
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return m, err
}

// HashFile returns the hex SHA-256 and size of a file.
func HashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Marshal gives the canonical bytes that are signed (files sorted, stable field order).
func (m Manifest) Marshal() ([]byte, error) {
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return json.MarshalIndent(m, "", " ")
}

// Parse decodes a manifest and rejects any path that could escape the install tree.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("manifest: %w", err)
	}
	for _, f := range m.Files {
		if err := SafeRel(f.Path); err != nil {
			return m, err
		}
	}
	return m, nil
}

// SafeRel rejects absolute paths, drive letters, backslashes and any ".." element.
func SafeRel(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.Contains(p, ":") {
		return fmt.Errorf("manifest: unsafe path %q", p)
	}
	if path.Clean(p) != p {
		return fmt.Errorf("manifest: unsafe path %q", p)
	}
	for _, el := range strings.Split(p, "/") {
		if el == ".." {
			return fmt.Errorf("manifest: unsafe path %q", p)
		}
	}
	return nil
}

func Sign(data []byte, priv ed25519.PrivateKey) []byte {
	return ed25519.Sign(priv, data)
}

func VerifySignature(data, sig []byte, pub ed25519.PublicKey) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, data, sig)
}

// VerifyTree compares root against m: every listed file present with the right hash,
// and no unlisted file (other than the manifest itself and .git).
func VerifyTree(root string, m Manifest) []string {
	var problems []string
	listed := map[string]bool{}
	for _, f := range m.Files {
		listed[f.Path] = true
		sum, _, err := HashFile(filepath.Join(root, filepath.FromSlash(f.Path)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			problems = append(problems, "missing: "+f.Path)
		case err != nil:
			problems = append(problems, "unreadable: "+f.Path)
		case sum != f.SHA256:
			problems = append(problems, "changed: "+f.Path)
		}
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel != FileName && rel != SigName && !listed[rel] {
			problems = append(problems, "unlisted: "+rel)
		}
		return nil
	})
	return problems
}
