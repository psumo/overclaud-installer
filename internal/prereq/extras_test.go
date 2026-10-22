package prereq

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtrasAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, g := range Extras {
		if g.ID == "" || g.Name == "" || g.Why == "" || g.Size == "" || len(g.Tools) == 0 {
			t.Fatalf("group %+v is missing a field", g.ID)
		}
		if seen[g.ID] {
			t.Fatalf("duplicate group %s", g.ID)
		}
		seen[g.ID] = true
		for _, tl := range g.Tools {
			if tl.Present == nil && len(tl.Bins) == 0 {
				t.Fatalf("%s: no way to tell whether it is installed", tl.Name)
			}
		}
	}
	for _, banned := range []string{"gitnexus", "GitNexus"} {
		for _, g := range Extras {
			for _, tl := range g.Tools {
				if tl.Name == banned {
					t.Fatalf("%s must not be offered (licence)", banned)
				}
			}
		}
	}
}

func TestPlaywrightChromiumDetection(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", dir)
	if _, ok := playwrightChromium(); ok {
		t.Fatal("empty cache must not count as installed")
	}
	if err := os.Mkdir(filepath.Join(dir, "chromium-1187"), 0o755); err != nil {
		t.Fatal(err)
	}
	g, _ := ExtraByID("browser")
	st := Check(nil, g.Tools[0])
	if !st.OK || st.Version != "1187" {
		t.Fatalf("got %+v", st)
	}
}
