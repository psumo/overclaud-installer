// Package licence talks to the overclaud.dev licence API and keeps the same local files as
// the npm CLI (~/.overclaud/device.json, ~/.claude/store/.license.json), so customers who
// switch installers keep their one-device binding and their key.
package licence

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const DefaultBase = "https://overclaud.dev/api"

var keyPattern = regexp.MustCompile(`^GC-[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}$`)

type Layout struct {
	Home          string
	ClaudeDir     string // ~/.claude
	ProductDir    string // ~/overclaud (release root: docs, sync scripts)
	OverclaudHome string // ~/.overclaud (installer state, device id, backups, staging)
	DeviceFile    string
	LicenceFile   string
}

func Paths(home string) Layout {
	oc := filepath.Join(home, ".overclaud")
	claude := filepath.Join(home, ".claude")
	return Layout{
		Home:          home,
		ClaudeDir:     claude,
		ProductDir:    filepath.Join(home, "overclaud"),
		OverclaudHome: oc,
		DeviceFile:    filepath.Join(oc, "device.json"),
		LicenceFile:   filepath.Join(claude, "store", ".license.json"),
	}
}

// NormalizeKey upper-cases and trims a key and reports whether it has the right shape.
func NormalizeKey(k string) (string, bool) {
	k = strings.ToUpper(strings.TrimSpace(k))
	return k, keyPattern.MatchString(k)
}

// DeviceID returns the machine's opaque id, creating it on first use (random UUID, no
// hardware data).
func DeviceID(p Layout) string {
	var rec struct {
		DeviceID string `json:"deviceId"`
	}
	if data, err := os.ReadFile(p.DeviceFile); err == nil && json.Unmarshal(data, &rec) == nil && len(rec.DeviceID) >= 8 {
		return rec.DeviceID
	}
	id := uuid4()
	_ = os.MkdirAll(filepath.Dir(p.DeviceFile), 0o755)
	data, _ := json.MarshalIndent(map[string]string{"deviceId": id, "createdAt": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	_ = os.WriteFile(p.DeviceFile, data, 0o644)
	return id
}

func uuid4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type Client struct {
	Base    string
	Version string
	HTTP    *http.Client
}

type Download struct {
	Valid   bool   `json:"valid"`
	URL     string `json:"url"`
	SHA     string `json:"sha"`
	Email   string `json:"email"`
	Plan    string `json:"plan"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Upgrade bool   `json:"upgrade"`
}

// npmPlatform matches Node's process.platform values, which the server already records.
func npmPlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

// RequestDownload validates the key and returns a short-lived signed download URL.
func (c Client) RequestDownload(key, deviceID string) (Download, error) {
	var d Download
	httpc := c.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 20 * time.Second}
	}
	base := c.Base
	if base == "" {
		base = DefaultBase
	}
	body, _ := json.Marshal(map[string]string{"key": key, "deviceId": deviceID, "platform": npmPlatform()})
	req, err := http.NewRequest(http.MethodPost, base+"/download", bytes.NewReader(body))
	if err != nil {
		return d, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "overclaud-installer/"+c.Version)
	resp, err := httpc.Do(req)
	if err != nil {
		return d, errors.New("could not reach overclaud.dev. Check your internet connection and try again")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return d, errors.New("Too many attempts. Wait an hour and try again")
	case resp.StatusCode >= 500:
		return d, errors.New("Overclaud servers are temporarily unavailable. Try again in a few minutes")
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return d, fmt.Errorf("unexpected reply from overclaud.dev (HTTP %d)", resp.StatusCode)
	}
	if !d.Valid || d.URL == "" {
		msg := d.Message
		if msg == "" {
			msg = "License is not active"
		}
		return d, errors.New(msg)
	}
	return d, nil
}

// Record is ~/.claude/store/.license.json, field-compatible with the npm CLI.
type Record struct {
	Key         string `json:"key"`
	Email       string `json:"email"`
	Plan        string `json:"plan"`
	Version     string `json:"version"`
	InstalledAt string `json:"installedAt,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

func ReadLicence(p Layout) (Record, error) {
	var r Record
	data, err := os.ReadFile(p.LicenceFile)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(data, &r)
}

func WriteLicence(p Layout, r Record) error {
	now := time.Now().UTC().Format(time.RFC3339)
	r.UpdatedAt = now
	if r.InstalledAt == "" {
		r.InstalledAt = now
	}
	if err := os.MkdirAll(filepath.Dir(p.LicenceFile), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(r, "", "  ")
	return os.WriteFile(p.LicenceFile, data, 0o600)
}
