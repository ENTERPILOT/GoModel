package auditlog

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/enterpilot/gomodel/internal/core"
)

// normalizeAuditPrefix trims a prefix filter value. Invalid UTF-8 is rejected:
// the range bounds below are built rune by rune, and PostgreSQL refuses
// invalid UTF-8 parameters anyway.
func normalizeAuditPrefix(field, raw string) (string, error) {
	prefix := strings.TrimSpace(raw)
	if !utf8.ValidString(prefix) {
		return "", core.NewInvalidRequestError("invalid "+field+": not valid UTF-8", nil)
	}
	return prefix, nil
}

// normalizeAuditUserPathPrefix adds the leading "/" every stored user path has,
// so typing "team" matches "/team/...".
func normalizeAuditUserPathPrefix(raw string) (string, error) {
	prefix, err := normalizeAuditPrefix("user_path_prefix", raw)
	if err != nil || prefix == "" {
		return prefix, err
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return prefix, nil
}

// auditPrefixUpperBound returns the smallest string that sorts after every
// string starting with prefix under byte-wise ordering (BINARY on SQLite,
// COLLATE "C" on PostgreSQL), so [prefix, upper) is exactly the strings with
// that prefix and a btree index can serve it as a range. UTF-8 byte order is
// code point order, so incrementing the last rune is enough; the result stays
// valid UTF-8 by skipping the surrogate range. ok is false when no upper
// bound exists (every rune is the maximum code point).
func auditPrefixUpperBound(prefix string) (upper string, ok bool) {
	runes := []rune(prefix)
	for i := len(runes) - 1; i >= 0; i-- {
		next := runes[i] + 1
		if next == 0xD800 {
			next = 0xE000
		}
		if next <= utf8.MaxRune {
			runes[i] = next
			return string(runes[:i+1]), true
		}
	}
	return "", false
}

// auditPrefixSQLPredicate matches column values starting with prefix through
// a range comparison an index can serve. column must compare bytes.
func auditPrefixSQLPredicate(column, prefix string) (string, []any) {
	upper, ok := auditPrefixUpperBound(prefix)
	if !ok {
		return column + " >= ?", []any{prefix}
	}
	return "(" + column + " >= ? AND " + column + " < ?)", []any{prefix, upper}
}

// auditPrefixRegex is an anchored, case-sensitive regex MongoDB turns into
// index bounds.
func auditPrefixRegex(prefix string) string {
	return "^" + regexp.QuoteMeta(prefix)
}
