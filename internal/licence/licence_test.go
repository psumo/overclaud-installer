package licence

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeAndValidateKey(t *testing.T) {
	if k, ok := NormalizeKey("  gc-ab12-cd34-ef56 "); !ok || k != "GC-AB12-CD34-EF56" {
		t.Fatalf("got %q %v", k, ok)
	}
	for _, bad := range []string{"", "GC-AB12-CD34", "XX-AB12-CD34-EF56", "GC-AB12-CD34-EF5!"} {
		if _, ok := NormalizeKey(bad); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestDeviceIDIsStableAndCompatibleWithTheNpmCLI(t *testing.T) {
	home := t.TempDir()
	p := Paths(home)
	os.MkdirAll(filepath.Dir(p.DeviceFile), 0o755)
	os.WriteFile(p.DeviceFile, []byte(`{"deviceId":"existing-device-1234","createdAt":"x"}`), 0o644)
	if id := DeviceID(p); id != "existing-device-1234" {
		t.Fatalf("did not reuse the npm CLI device id: %q", id)
	}
	fresh := Paths(t.TempDir())
	a, b := DeviceID(fresh), DeviceID(fresh)
	if a == "" || a != b {
		t.Fatalf("new device id not stable: %q %q", a, b)
	}
}

func TestRequestDownloadSendsKeyDeviceAndPlatform(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/download" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"valid":true,"url":"https://x/api/fetch?t=1","sha":"abc123","email":"a@b.c","plan":"lifetime"}`))
	}))
	defer srv.Close()
	c := Client{Base: srv.URL + "/api", Version: "1.4.0"}
	d, err := c.RequestDownload("GC-AB12-CD34-EF56", "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if got["key"] != "GC-AB12-CD34-EF56" || got["deviceId"] != "dev-1" || got["platform"] == "" {
		t.Fatalf("request body %v", got)
	}
	if d.URL == "" || d.SHA != "abc123" || d.Email != "a@b.c" {
		t.Fatalf("response %+v", d)
	}
}

func TestRequestDownloadExplainsRefusals(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{200, `{"valid":false,"message":"License not active."}`, "License not active"},
		{403, `{"valid":false,"error":"device_limit","message":"already active on another device"}`, "another device"},
		{429, `{"error":"rate_limited"}`, "Too many attempts"},
		{503, `oops`, "temporarily unavailable"},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			w.Write([]byte(c.body))
		}))
		_, err := (Client{Base: srv.URL + "/api"}).RequestDownload("GC-AB12-CD34-EF56", "d")
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("status %d: got %v, want %q", c.status, err, c.want)
		}
	}
}

func TestLicenceFileRoundTripKeepsInstalledAt(t *testing.T) {
	p := Paths(t.TempDir())
	if err := WriteLicence(p, Record{Key: "GC-AB12-CD34-EF56", Email: "a@b.c", Plan: "lifetime", Version: "s1"}); err != nil {
		t.Fatal(err)
	}
	first, _ := ReadLicence(p)
	if err := WriteLicence(p, Record{Key: first.Key, Email: first.Email, Plan: first.Plan, Version: "s2", InstalledAt: first.InstalledAt}); err != nil {
		t.Fatal(err)
	}
	second, _ := ReadLicence(p)
	if second.Version != "s2" || second.InstalledAt != first.InstalledAt || second.InstalledAt == "" {
		t.Fatalf("first %+v second %+v", first, second)
	}
}

func TestLicenceFileIsOwnerOnlyEvenOverAWiderOldFile(t *testing.T) {
	p := Paths(t.TempDir())
	os.MkdirAll(filepath.Dir(p.LicenceFile), 0o755)
	os.WriteFile(p.LicenceFile, []byte("{}"), 0o644)
	if err := WriteLicence(p, Record{Key: "GC-AB12-CD34-EF56"}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(p.LicenceFile)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("licence file is %v", info.Mode().Perm())
	}
}
