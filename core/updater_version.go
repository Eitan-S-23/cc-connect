package core

import (
	"regexp"
	"strconv"
	"strings"
)

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

type semver struct {
	major, minor, patch int
	pre                 string
	preNum              int
}

func parseSemver(v string) semver {
	m := semverRe.FindStringSubmatch(strings.TrimPrefix(strings.TrimSpace(v), "fork-"))
	if m == nil {
		return semver{}
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	patch, _ := strconv.Atoi(m[3])
	preNum := 0
	if idx := strings.LastIndex(m[4], "."); idx >= 0 {
		preNum, _ = strconv.Atoi(m[4][idx+1:])
	}
	return semver{major: major, minor: minor, patch: patch, pre: m[4], preNum: preNum}
}

func normalizeVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "fork-")
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// CompareUpdateVersions ignores build metadata. Fork publication order must
// be resolved from release metadata rather than guessed from feature names.
func CompareUpdateVersions(a, b string) int { return semverCompare(a, b) }

func semverCompare(a, b string) int {
	sa, sb := parseSemver(a), parseSemver(b)
	for _, pair := range [][2]int{{sa.major, sb.major}, {sa.minor, sb.minor}, {sa.patch, sb.patch}} {
		if pair[0] > pair[1] {
			return 1
		}
		if pair[0] < pair[1] {
			return -1
		}
	}
	if sa.pre == sb.pre {
		return 0
	}
	if sa.pre == "" {
		return 1
	}
	if sb.pre == "" {
		return -1
	}
	ap, bp := strings.Split(sa.pre, "."), strings.Split(sb.pre, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if ap[i] == bp[i] {
			continue
		}
		an, ae := strconv.ParseUint(ap[i], 10, 64)
		bn, be := strconv.ParseUint(bp[i], 10, 64)
		if ae == nil && be == nil {
			if an < bn {
				return -1
			}
			return 1
		}
		if ae == nil {
			return -1
		}
		if be == nil {
			return 1
		}
		return strings.Compare(ap[i], bp[i])
	}
	if len(ap) < len(bp) {
		return -1
	}
	return 1
}
