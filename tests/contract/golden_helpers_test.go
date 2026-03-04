//go:build contract

package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const goldenOutputDir = "golden"

func goldenPathForFixture(fixture string) string {
	return strings.TrimSuffix(fixture, filepath.Ext(fixture)) + ".golden.json"
}

func shouldRecordGoldenOutputs() bool {
	return os.Getenv("RECORD") == "1" || os.Getenv("UPDATE_GOLDEN") == "1"
}

func compareGoldenJSON(t *testing.T, path string, value any) {
	t.Helper()

	actual := mustMarshalNormalizedJSON(t, value)
	fullPath := filepath.Join(testdataDir, goldenOutputDir, path)

	if shouldRecordGoldenOutputs() {
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0755))
		require.NoError(t, os.WriteFile(fullPath, actual, 0644))
	}

	expected, err := os.ReadFile(fullPath)
	if os.IsNotExist(err) {
		t.Fatalf("missing golden file %s; run `make record-api` then `RECORD=1 go test -tags=contract -timeout=5m ./tests/contract/...`", filepath.Join(goldenOutputDir, path))
	}
	require.NoError(t, err)

	require.JSONEq(t, string(expected), string(actual), "golden mismatch for %s", filepath.Join(goldenOutputDir, path))
}

func mustMarshalNormalizedJSON(t *testing.T, value any) []byte {
	t.Helper()

	raw, err := json.Marshal(value)
	require.NoError(t, err)

	var generic any
	require.NoError(t, json.Unmarshal(raw, &generic))

	normalized := normalizeGoldenValue(generic)
	out, err := json.MarshalIndent(normalized, "", "  ")
	require.NoError(t, err)
	return append(out, '\n')
}

func normalizeGoldenValue(v any) any {
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for key, item := range val {
			lowerKey := strings.ToLower(key)
			if lowerKey == "created" || lowerKey == "created_at" {
				out[key] = int64(0)
				continue
			}

			normalizedItem := normalizeGoldenValue(item)
			if lowerKey == "id" || strings.HasSuffix(lowerKey, "_id") {
				if id, ok := normalizedItem.(string); ok {
					out[key] = normalizeGeneratedID(id)
					continue
				}
			}
			out[key] = normalizedItem
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = normalizeGoldenValue(item)
		}
		return out
	default:
		return v
	}
}

func normalizeGeneratedID(id string) string {
	if strings.HasPrefix(id, "resp_") {
		suffix := strings.TrimPrefix(id, "resp_")
		if isGeneratedIDSuffix(suffix) {
			return "resp_<generated>"
		}
	}
	if strings.HasPrefix(id, "msg_") {
		suffix := strings.TrimPrefix(id, "msg_")
		if isGeneratedIDSuffix(suffix) {
			return "msg_<generated>"
		}
	}
	if strings.HasPrefix(id, "req_") {
		suffix := strings.TrimPrefix(id, "req_")
		if isGeneratedIDSuffix(suffix) {
			return "req_<generated>"
		}
	}
	return id
}

func isGeneratedIDSuffix(s string) bool {
	if s == "" {
		return false
	}
	if _, err := uuid.Parse(s); err == nil {
		return true
	}
	// OpenAI response IDs often use long lowercase hex suffixes.
	if len(s) >= 24 && isHexString(s) {
		return true
	}
	// ULID-style request IDs.
	if len(s) == 26 && isCrockfordBase32String(s) {
		return true
	}
	// Provider-specific base62-like IDs (e.g. Anthropic/Groq/xAI IDs).
	if len(s) >= 20 && isBase62String(s) {
		return true
	}
	return false
}

func isHexString(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func isBase62String(s string) bool {
	for _, r := range s {
		switch {
		case unicode.IsDigit(r):
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '-':
		case r == '_':
		default:
			return false
		}
	}
	return true
}

func isCrockfordBase32String(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'A' || r > 'Z') {
			return false
		}
		switch r {
		case 'I', 'L', 'O', 'U':
			return false
		}
	}
	return true
}
