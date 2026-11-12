package session

import (
	"strings"

	"github.com/tidwall/gjson"
)

// bodyRule is a Rule with its path split for the single-walk lookup. field is
// the index of the path's first component in Detector.fields, or -1 when the
// path must be resolved with a full gjson query against the body.
type bodyRule struct {
	Rule
	field int
	rest  string
}

// Content anchor members, at these indexes of Detector.fields.
const (
	fieldModel = iota
	fieldSystem
	fieldInstructions
	fieldTools
	fieldMessages
	fieldInput
)

var contentFields = []string{"model", "system", "instructions", "tools", "messages", "input"}

// maxBodyFields bounds the members collected in one walk so they fit a stack
// array; rules beyond it fall back to a full gjson query.
const maxBodyFields = 16

func (d *Detector) compileBodyRule(rule Rule) bodyRule {
	compiled := bodyRule{Rule: rule, field: -1}
	if !plainPath(rule.BodyPath) {
		return compiled
	}
	top, rest, _ := strings.Cut(rule.BodyPath, ".")
	for i, name := range d.fields {
		if name == top {
			compiled.field, compiled.rest = i, rest
			return compiled
		}
	}
	if len(d.fields) < maxBodyFields {
		d.fields = append(d.fields, top)
		compiled.field, compiled.rest = len(d.fields)-1, rest
	}
	return compiled
}

// plainPath reports whether a gjson path is only dot-separated literal keys,
// which resolve the same against a collected member as against the body.
func plainPath(path string) bool {
	for part := range strings.SplitSeq(path, ".") {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}

// bodyMember is the first occurrence of a top-level body member; dup records
// that the key appears again.
type bodyMember struct {
	value gjson.Result
	dup   bool
}

// collectMembers walks the top level of the body once, recording the first
// occurrence of every member in d.fields, the occurrence a gjson query for
// that key resolves to. It reports false when the body is not a JSON object.
func (d *Detector) collectMembers(raw string, members []bodyMember) bool {
	root := gjson.Parse(raw)
	if !root.IsObject() {
		return false
	}
	root.ForEach(func(key, value gjson.Result) bool {
		for i, name := range d.fields {
			if key.Str != name {
				continue
			}
			if members[i].value.Exists() {
				members[i].dup = true
			} else {
				members[i].value = value
			}
			break
		}
		return true
	})
	return true
}

// bodyRuleResult resolves a rule path exactly as gjson.Get on the whole body
// does. A nested path under a duplicated key needs the full query, because
// gjson moves on to later occurrences when the first lacks the nested key.
func bodyRuleResult(raw string, members []bodyMember, isObject bool, rule bodyRule) gjson.Result {
	if !isObject || rule.field < 0 {
		return gjson.Get(raw, rule.BodyPath)
	}
	member := members[rule.field]
	switch {
	case rule.rest == "":
		return member.value
	case member.dup:
		return gjson.Get(raw, rule.BodyPath)
	default:
		return member.value.Get(rule.rest)
	}
}
