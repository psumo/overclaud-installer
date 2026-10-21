// Package payload downloads a release tarball, extracts it safely, and verifies it against
// the signed manifest it carries.
package payload

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/psumo/overclaud-installer/internal/manifest"
)

// Caps on what a release may contain; the real payload is well under these. Extraction
// rejects (never truncates) anything larger.
var (
	MaxDownload int64 = 1 << 30
	MaxExtract  int64 = 2 << 30
	MaxEntries        = 200_000
)

// Fetch streams url into dst. progress, if set, is called with bytes written so far.
func Fetch(url, dst string, progress func(int64)) error {
	c := &http.Client{Timeout: 30 * time.Minute}
	resp, err := c.Get(url)
	if err != nil {
		// The URL carries a one-time download token: report the cause, never the URL.
		var ue *neturl.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("download failed: %v", ue.Err)
		}
		return errors.New("download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("download failed (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	var w io.Writer = f
	if progress != nil {
		w = &progressWriter{w: f, fn: progress}
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, MaxDownload+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > MaxDownload {
		err = errors.New("download larger than expected; refusing it")
	}
	return err
}

type progressWriter struct {
	w  io.Writer
	n  int64
	fn func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	p.fn(p.n)
	return n, err
}

// Extract unpacks a gzipped tarball into dir, stripping the top-level directory GitHub
// adds. It refuses links and any path that would land outside dir.
func Extract(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	tr := tar.NewReader(gz)
	var total int64
	entries := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("corrupt archive: %w", err)
		}
		if entries++; entries > MaxEntries {
			return errors.New("archive has more entries than any release; refusing it")
		}
		name := strings.TrimPrefix(h.Name, "./")
		i := strings.IndexByte(name, '/')
		if i < 0 {
			if h.Typeflag == tar.TypeDir || h.Typeflag == tar.TypeXGlobalHeader {
				continue
			}
			return fmt.Errorf("unexpected top-level entry %q", h.Name)
		}
		rel := strings.TrimSuffix(name[i+1:], "/")
		if rel == "" {
			continue
		}
		if err := safeComponents(rel); err != nil {
			return fmt.Errorf("%v (%q); refusing it", err, h.Name)
		}
		switch h.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue
		case tar.TypeDir:
			if err := manifest.SafeRel(rel); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := manifest.SafeRel(rel); err != nil {
				return err
			}
			if h.Size < 0 || total+h.Size > MaxExtract {
				return errors.New("archive unpacks larger than any release; refusing it")
			}
			total += h.Size
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, h.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive contains a link or special file (%q); refusing it", h.Name)
		}
	}
}

var shortName = regexp.MustCompile(`~[0-9]`)

// safeComponents rejects names Windows would treat as something else: a trailing dot or
// space (".git." is ".git"), 8.3 short names ("GIT~1"), and any .git directory, which the
// signed manifest does not cover.
func safeComponents(rel string) error {
	for _, el := range strings.Split(rel, "/") {
		if strings.HasSuffix(el, ".") || strings.HasSuffix(el, " ") || shortName.MatchString(el) {
			return errors.New("archive has a path Windows would rename")
		}
		if strings.EqualFold(el, ".git") {
			return errors.New("archive contains a .git entry")
		}
	}
	return nil
}

// Verify checks the signature on the release's manifest and every file against it.
func Verify(dir string, pub ed25519.PublicKey) (manifest.Manifest, error) {
	var m manifest.Manifest
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(manifest.FileName)))
	if err != nil {
		return m, errors.New("release is not signed (no manifest); refusing it")
	}
	sigHex, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(manifest.SigName)))
	if err != nil {
		return m, errors.New("release is not signed (no signature); refusing it")
	}
	sig, err := hex.DecodeString(strings.TrimSpace(string(sigHex)))
	if err != nil || !manifest.VerifySignature(data, sig, pub) {
		return m, errors.New("release signature does not match Overclaud's signing key; refusing it")
	}
	m, err = manifest.Parse(data)
	if err != nil {
		return m, err
	}
	if problems := manifest.VerifyTree(dir, m); len(problems) > 0 {
		shown := problems
		if len(shown) > 5 {
			shown = shown[:5]
		}
		return m, fmt.Errorf("release files do not match the signed manifest (%d problems): %s", len(problems), strings.Join(shown, "; "))
	}
	return m, nil
}
