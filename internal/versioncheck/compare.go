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
	if currentPre == latestPre {
		return false
	}
	// Equal release numbers: a prerelease is superseded by the final release,
	// and by a later prerelease of the same one (rc1 -> rc2).
	if currentPre == "" || latestPre == "" {
		return currentPre != "" && latestPre == ""
	}
	return prereleaseLess(currentPre, latestPre)
}

// prereleaseLess compares two semantic-versioning prerelease strings. Fields
// are dot separated; numeric fields compare numerically and rank below
// alphanumeric ones, and a shorter prerelease ranks below a longer one that
// shares its prefix.
func prereleaseLess(a, b string) bool {
	aFields, bFields := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(aFields), len(bFields)) {
		if aFields[i] == bFields[i] {
			continue
		}
		aIsNum, bIsNum := isNumericField(aFields[i]), isNumericField(bFields[i])
		switch {
		case aIsNum && bIsNum:
			return numericFieldLess(aFields[i], bFields[i])
		case aIsNum != bIsNum:
			return aIsNum
		default:
			return aFields[i] < bFields[i]
		}
	}
	return len(aFields) < len(bFields)
}

// isNumericField reports whether a prerelease field is a numeric identifier.
// Deliberately not strconv.Atoi: semantic versioning allows numeric
// identifiers of any length, and converting to int would reject an oversized
// one as alphanumeric and rank it wrongly.
func isNumericField(field string) bool {
	if field == "" {
		return false
	}
	// A leading zero makes the identifier invalid under semantic versioning.
	// Treating it as alphanumeric keeps numericFieldLess exact, which relies
	// on a longer digit string always being the larger number.
	if len(field) > 1 && field[0] == '0' {
		return false
	}
	for i := range len(field) {
		if field[i] < '0' || field[i] > '9' {
			return false
		}
	}
	return true
}

// numericFieldLess compares two numeric identifiers as digit strings. Semantic
// versioning forbids leading zeroes, so the longer string is the larger
// number and equal lengths compare lexically.
func numericFieldLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
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
