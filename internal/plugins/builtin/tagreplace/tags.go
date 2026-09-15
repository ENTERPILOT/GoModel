package tagreplace

import (
	"regexp"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// tagPattern matches {{gomodel.<tag>}}, with optional spaces inside the braces.
var tagPattern = regexp.MustCompile(`\{\{\s*gomodel\.([^{}\s]+)\s*\}\}`)

// labelPrefix selects a request label by key: {{gomodel.label.team}}.
const labelPrefix = "label."

// values resolves tag names for one request.
type values struct {
	fixed  map[string]string
	labels map[string]string
}

func newValues(meta pluginapi.Meta, now time.Time) values {
	return values{
		fixed: map[string]string{
			"requested_model": meta.RequestedModel,
			"resolved_model":  meta.Model,
			"virtual_model":   meta.VirtualModelSource,
			"provider_type":   meta.Provider,
			"provider_name":   meta.ProviderName,
			"user_path":       meta.UserPath,
			"request_id":      meta.RequestID,
			"session_id":      meta.SessionID,
			"date":            now.Format(time.DateOnly),
			"time":            now.Format(time.TimeOnly),
			"datetime":        now.Format(time.RFC3339),
			"weekday":         now.Weekday().String(),
		},
		labels: meta.Labels,
	}
}

// lookup returns the value of a tag and whether the tag is known. A known
// tag with no value for this request resolves to the empty string.
func (v values) lookup(name string) (string, bool) {
	if key, ok := strings.CutPrefix(name, labelPrefix); ok && key != "" {
		return v.labels[key], true
	}
	value, ok := v.fixed[name]
	return value, ok
}

// expand replaces every known tag in s and returns the result with the
// number of replacements. Unknown tags are left as written.
func expand(s string, v values) (string, int) {
	if !strings.Contains(s, "{{") {
		return s, 0
	}
	n := 0
	out := tagPattern.ReplaceAllStringFunc(s, func(match string) string {
		name := tagPattern.FindStringSubmatch(match)[1]
		value, ok := v.lookup(name)
		if !ok {
			return match
		}
		n++
		return value
	})
	return out, n
}
