package schedule

import (
	"strings"
	"testing"
)

func TestCronLinesReplaceOldAutoSyncAndStayIdempotent(t *testing.T) {
	cur := "0 3 * * * backup.sh\n0 */6 * * * /home/u/claude-config/sync/auto-sync.sh # claude-config auto-sync\n"
	once := CronLines(cur, "/home/u/.local/bin/overclaud", 3, 17)
	twice := CronLines(once, "/home/u/.local/bin/overclaud", 3, 17)
	if once != twice {
		t.Fatalf("not idempotent:\n%s\n%s", once, twice)
	}
	if strings.Contains(once, "auto-sync") || !strings.Contains(once, "backup.sh") {
		t.Fatalf("old sync not removed or user line lost:\n%s", once)
	}
	if !strings.Contains(once, `17 3 * * * "/home/u/.local/bin/overclaud" update --quiet`) {
		t.Fatalf("missing update line:\n%s", once)
	}
}

func TestSlotIsStableAndAtNight(t *testing.T) {
	h1, m1 := Slot("device-a")
	h2, m2 := Slot("device-a")
	if h1 != h2 || m1 != m2 || h1 < 2 || h1 > 5 || m1 < 0 || m1 > 59 {
		t.Fatalf("%d:%d %d:%d", h1, m1, h2, m2)
	}
}

func TestLaunchdPlistEscapesPath(t *testing.T) {
	p := LaunchdPlist("/Users/a&b/overclaud", 3, 5)
	if !strings.Contains(p, "/Users/a&amp;b/overclaud") || !strings.Contains(p, "<integer>3</integer>") {
		t.Fatal(p)
	}
}
