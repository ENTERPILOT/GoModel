package sqlx

import (
	"strconv"
	"strings"
)

// rebind rewrites `?` placeholders to PostgreSQL's positional $1, $2, ... form.
//
// Placeholders are numbered in the order they appear, so a value bound twice is
// passed twice — matching how the SQLite half of these stores already builds
// its argument lists.
//
// A query must use one placeholder style throughout. `?` is the portable one
// and is what stores write; a literal $n left in the same statement is not
// renumbered and would silently end up sharing an argument slot with a
// rewritten `?`.
//
// A `?` is only a placeholder outside quoted text and comments. The scanner
// therefore skips over single-quoted strings (where a doubled quote is an
// escaped quote rather than a terminator), double-quoted
// identifiers, dollar-quoted bodies ($$...$$ and $tag$...$tag$, used by the
// migration DO blocks), line comments and block comments. Without that, a
// question mark inside a string literal would silently shift every subsequent
// placeholder by one.
func rebind(query string) string {
	if !strings.ContainsRune(query, '?') {
		return query
	}

	var out strings.Builder
	out.Grow(len(query) + 8)

	next := 1
	for i := 0; i < len(query); {
		switch {
		case query[i] == '?':
			out.WriteByte('$')
			out.WriteString(strconv.Itoa(next))
			next++
			i++

		case query[i] == '\'':
			i = copyQuoted(&out, query, i, '\'')

		case query[i] == '"':
			i = copyQuoted(&out, query, i, '"')

		case strings.HasPrefix(query[i:], "--"):
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				out.WriteString(query[i:])
				return out.String()
			}
			out.WriteString(query[i : i+end+1])
			i += end + 1

		case strings.HasPrefix(query[i:], "/*"):
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				out.WriteString(query[i:])
				return out.String()
			}
			out.WriteString(query[i : i+2+end+2])
			i += 2 + end + 2

		case query[i] == '$':
			if tag, ok := dollarQuoteTag(query, i); ok {
				i = copyDollarQuoted(&out, query, i, tag)
				continue
			}
			out.WriteByte(query[i])
			i++

		default:
			out.WriteByte(query[i])
			i++
		}
	}
	return out.String()
}

// copyQuoted copies a quoted run starting at the opening quote at index start,
// honoring doubled quotes as escapes, and returns the index just past it.
func copyQuoted(out *strings.Builder, query string, start int, quote byte) int {
	out.WriteByte(query[start])
	i := start + 1
	for i < len(query) {
		if query[i] == quote {
			// A doubled quote is an escaped quote, not the end of the run.
			if i+1 < len(query) && query[i+1] == quote {
				out.WriteString(query[i : i+2])
				i += 2
				continue
			}
			out.WriteByte(query[i])
			return i + 1
		}
		out.WriteByte(query[i])
		i++
	}
	return i
}

// dollarQuoteTag reports whether a dollar-quote delimiter opens at index start,
// returning the full delimiter (for example "$$" or "$body$"). A tag holds only
// letters, digits and underscores, so an ordinary $1 placeholder is not one.
func dollarQuoteTag(query string, start int) (string, bool) {
	for i := start + 1; i < len(query); i++ {
		c := query[i]
		if c == '$' {
			return query[start : i+1], true
		}
		isTagChar := c == '_' ||
			(c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9')
		if !isTagChar {
			return "", false
		}
	}
	return "", false
}

// copyDollarQuoted copies a dollar-quoted body opening at start with the given
// delimiter, and returns the index just past its closing delimiter.
func copyDollarQuoted(out *strings.Builder, query string, start int, tag string) int {
	bodyStart := start + len(tag)
	end := strings.Index(query[bodyStart:], tag)
	if end < 0 {
		out.WriteString(query[start:])
		return len(query)
	}
	stop := bodyStart + end + len(tag)
	out.WriteString(query[start:stop])
	return stop
}
