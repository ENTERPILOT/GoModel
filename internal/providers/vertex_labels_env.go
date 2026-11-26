package providers

import (
	"fmt"
	"strings"
)

// parseLabelList parses a VERTEX_LABELS value, "key=value,key=value", into
// the provider's vertex_labels. The syntax is unambiguous because Vertex AI
// labels never contain "," or "="; their format is left to Vertex AI, which
// rejects invalid labels.
//
// Requires: nothing; any string is accepted.
// Ensures:  a blank value yields nil; otherwise every non-blank entry becomes
// one label, trimmed, with an empty value allowed ("team="). An entry without
// "=", with an empty key, or with a key listed twice is an error, which never
// quotes the value: it may have come from a secret reference.
func parseLabelList(value string) (map[string]string, error) {
	labels := make(map[string]string)
	for i, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, labelValue, ok := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("entry %d is not key=value", i+1)
		}
		if _, dup := labels[key]; dup {
			return nil, fmt.Errorf("entry %d repeats a key", i+1)
		}
		labels[key] = strings.TrimSpace(labelValue)
	}
	if len(labels) == 0 {
		return nil, nil
	}
	return labels, nil
}
