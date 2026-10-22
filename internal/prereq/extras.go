package prereq

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Group is a set of optional tools the customer accepts or skips as one, with why it is
// useful and roughly what it costs in disk space.
type Group struct {
	ID    string
	Name  string
	Why   string
	Size  string
	Tools []Tool
}

// Extras are the optional groups, asked one by one. Everything Overclaud needs to work
// (memory, the nightly loop, GitHub) is in Tools, not here.
var Extras = []Group{
	{ID: "browser", Name: "Browser testing", Size: "about 300 MB",
		Why: "lets Claude open your pages, click through them and take screenshots",
		Tools: []Tool{{Name: "Playwright Chromium", Present: playwrightChromium,
			Script: Script{Unix: "npx -y playwright install chromium", Windows: "npx -y playwright install chromium"}}}},
	{ID: "security", Name: "Security scanning", Size: "about 200 MB",
		Why: "deeper security scans of your code (Semgrep)",
		Tools: []Tool{{Name: "Semgrep", Bins: []string{"semgrep"}, Args: []string{"--version"}, Brew: "semgrep",
			Script: Script{Unix: "uv tool install semgrep || python3 -m pip install --user semgrep",
				Windows: "uv tool install semgrep"}}}},
	{ID: "media", Name: "Media tools", Size: "about 150 MB",
		Why: "video, audio and image conversion for the media skills (ffmpeg, ImageMagick)",
		Tools: []Tool{
			{Name: "ffmpeg", Bins: []string{"ffmpeg"}, Args: []string{"-version"},
				Winget: "Gyan.FFmpeg", Brew: "ffmpeg", Apt: "ffmpeg", Dnf: "ffmpeg-free", Pacman: "ffmpeg", Zypper: "ffmpeg"},
			{Name: "ImageMagick", Bins: magickBins(), Args: []string{"-version"},
				Winget: "ImageMagick.ImageMagick", Brew: "imagemagick", Apt: "imagemagick", Dnf: "ImageMagick", Pacman: "imagemagick", Zypper: "ImageMagick"},
		}},
}

// ExtraByID finds a group.
func ExtraByID(id string) (Group, bool) {
	for _, g := range Extras {
		if g.ID == id {
			return g, true
		}
	}
	return Group{}, false
}

// magickBins: ImageMagick 7 is `magick`; 6 is `convert`, which on Windows is a system disk
// tool, so only `magick` counts there.
func magickBins() []string {
	if runtime.GOOS == "windows" {
		return []string{"magick"}
	}
	return []string{"magick", "convert"}
}

// playwrightChromium finds a Chromium that Playwright downloaded for this user.
func playwrightChromium() (string, bool) {
	home, _ := os.UserHomeDir()
	var cache string
	switch runtime.GOOS {
	case "windows":
		cache = filepath.Join(os.Getenv("LOCALAPPDATA"), "ms-playwright")
	case "darwin":
		cache = filepath.Join(home, "Library", "Caches", "ms-playwright")
	default:
		cache = filepath.Join(home, ".cache", "ms-playwright")
	}
	if v := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); v != "" && v != "0" {
		cache = v
	}
	entries, err := os.ReadDir(cache)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "chromium-") {
			return strings.TrimPrefix(e.Name(), "chromium-"), true
		}
	}
	return "", false
}
