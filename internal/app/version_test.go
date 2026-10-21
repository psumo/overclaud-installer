package app

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.4.0", "1.3.9", 1},
		{"1.3.9", "1.4.0", -1},
		{"1.4.0", "1.4.0", 0},
		{"1.4.0-rc1", "1.4.0", -1},
		{"1.4.0", "1.4.0-rc1", 1},
		{"1.4.0-rc.2", "1.4.0-rc.10", -1},
		{"1.10.0", "1.9.9", 1},
		{"v2.0.0", "1.99.99", 1},
	}
	for _, c := range cases {
		got, ok := compareVersions(c.a, c.b)
		if !ok || got != c.want {
			t.Fatalf("%s vs %s: got %d ok=%v, want %d", c.a, c.b, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "1.4", "latest", "1.4.0.1", "x1.4.0"} {
		if _, ok := compareVersions(bad, "1.0.0"); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}
