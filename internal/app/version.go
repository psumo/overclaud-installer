package app

import (
	"regexp"
	"strconv"
	"strings"
)

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

// compareVersions orders two release versions by semantic-versioning rules (a pre-release
// sorts below its final release). ok is false when either cannot be parsed, so callers can
// fail closed.
func compareVersions(a, b string) (cmp int, ok bool) {
	ma, mb := semverRe.FindStringSubmatch(strings.TrimSpace(a)), semverRe.FindStringSubmatch(strings.TrimSpace(b))
	if ma == nil || mb == nil {
		return 0, false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return sign(x - y), true
		}
	}
	return comparePre(ma[4], mb[4]), true
}

func comparePre(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1 // final release outranks any pre-release
	case b == "":
		return -1
	}
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, errx := strconv.Atoi(pa[i])
		y, erry := strconv.Atoi(pb[i])
		switch {
		case errx == nil && erry == nil:
			if x != y {
				return sign(x - y)
			}
		case errx == nil:
			return -1 // numeric identifiers sort below alphanumeric ones
		case erry == nil:
			return 1
		default:
			if c := strings.Compare(pa[i], pb[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(pa) - len(pb))
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}
