package versioncheck

import (
	"strconv"
	"strings"
)

// IsNewer reports whether latest is a later release than current.
//
// It is deliberately conservative: an unparseable or non-release local
// version ("dev", a bare commit) never reports an update, and build metadata
// is ignored, so a Pro build stamped "1.0.0+core.0.1.81" compares as 1.0.0.
// Within one release number a prerelease ranks below the final release
// ("1.0.0-rc1" < "1.0.0"), matching semantic versioning.
func IsNewer(current, latest string) bool {
	currentNumbers, currentPre, ok := parseVersion(current)
	if !ok {
		return false
	}
	latestNumbers, latestPre, ok := parseVersion(latest)
	if !ok {
		return false
	}
	for i := range max(len(currentNumbers), len(latestNumbers)) {
		a, b := at(currentNumbers, i), at(latestNumbers, i)
		if a != b {
			return b > a
		}
	}
	// Equal release numbers: only a prerelease can still be superseded.
	return currentPre != "" && latestPre == ""
}

func at(numbers []int, i int) int {
	if i < len(numbers) {
		return numbers[i]
	}
	return 0
}

// parseVersion splits "v1.2.3-rc1+build" into ([1 2 3], "rc1"). It reports
// false when the leading component is not a release number.
func parseVersion(raw string) ([]int, string, bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "v")
	if raw == "" {
		return nil, "", false
	}
	if plus := strings.IndexByte(raw, '+'); plus >= 0 {
		raw = raw[:plus]
	}
	prerelease := ""
	if dash := strings.IndexByte(raw, '-'); dash >= 0 {
		prerelease = raw[dash+1:]
		raw = raw[:dash]
	}
	fields := strings.Split(raw, ".")
	numbers := make([]int, 0, len(fields))
	for _, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil || n < 0 {
			return nil, "", false
		}
		numbers = append(numbers, n)
	}
	if len(numbers) == 0 {
		return nil, "", false
	}
	return numbers, prerelease, true
}
