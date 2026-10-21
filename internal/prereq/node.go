package prereq

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const nodeDist = "https://nodejs.org/dist/latest-v22.x/"

// NodeArchive returns the nodejs.org archive name for this platform from a SHASUMS256.txt
// body, with its checksum.
func NodeArchive(shasums, goos, goarch string) (name, sum string, err error) {
	osName := map[string]string{"linux": "linux", "darwin": "darwin"}[goos]
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[goarch]
	if osName == "" || arch == "" {
		return "", "", fmt.Errorf("no Node.js download for %s/%s", goos, goarch)
	}
	suffix := "-" + osName + "-" + arch + ".tar.gz"
	sc := bufio.NewScanner(strings.NewReader(shasums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.HasPrefix(f[1], "node-v") && strings.HasSuffix(f[1], suffix) {
			return f[1], f[0], nil
		}
	}
	return "", "", errors.New("Node.js archive not listed in SHASUMS256.txt")
}

// InstallNodeTarball installs the current Node 22 LTS into dir (e.g. ~/.overclaud/tools/node)
// after checking its SHA-256 against nodejs.org's published list. Used when the distro's
// package is missing or too old. extract unpacks a .tar.gz, stripping the top directory.
func InstallNodeTarball(dir string, extract func(r io.Reader, dir string) error) (string, error) {
	c := &http.Client{Timeout: 10 * time.Minute}
	resp, err := c.Get(nodeDist + "SHASUMS256.txt")
	if err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("could not read the Node.js release list: %v", err)
	}
	name, want, err := NodeArchive(string(body), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	resp, err = c.Get(nodeDist + name)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Node.js download failed (HTTP %d)", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "node-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		tmp.Close()
		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		tmp.Close()
		return "", errors.New("Node.js download checksum does not match nodejs.org; refusing it")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := extract(tmp, dir); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()
	bin := filepath.Join(dir, "bin")
	os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bin, nil
}
