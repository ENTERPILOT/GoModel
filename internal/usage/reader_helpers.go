package usage

import (
	"strings"

	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
)

// usageGroupedProviderNameSQL returns a SQL expression that collapses blank
// provider_name values to the canonical provider before grouping.
func usageGroupedProviderNameSQL(providerNameColumn, providerColumn string) string {
	return "COALESCE(NULLIF(TRIM(" + providerNameColumn + "), ''), " + providerColumn + ")"
}

// usageGroupedUserPathSQL returns a SQL expression that collapses blank
// user_path values to the tracked root path before grouping.
func usageGroupedUserPathSQL(userPathColumn string) string {
	return "COALESCE(NULLIF(TRIM(" + userPathColumn + "), ''), '/')"
}

// clampLimitOffset applies the usage reader pagination policy:
// limit defaults to 50 and is capped at 200; offset floors at 0.
func clampLimitOffset(limit, offset int) (int, int) {
	return sqlutil.ClampLimitOffset(limit, offset, 50, 200)
}

// providerSessionCostSQL sums recorded provider spend while excluding local
// response-cache rows, whose stored cost represents avoided cost. A session
// served entirely from the local cache has known zero provider spend; sessions
// with provider requests but no pricing retain a NULL cost.
func providerSessionCostSQL(column string) string {
	providerRow := "(cache_type IS NULL OR cache_type = '')"
	return "CASE WHEN COUNT(CASE WHEN " + providerRow + " THEN 1 END) = 0 THEN 0 " +
		"ELSE SUM(CASE WHEN " + providerRow + " THEN " + column + " END) END"
}

// promptCacheRawKeys are the raw_data fields EntryInputSegments reads. The
// dashboard folds extract only these instead of decoding all of raw_data.
var promptCacheRawKeys = []string{
	"cache_read_input_tokens",
	"prompt_cached_tokens",
	"cached_tokens",
	"cache_creation_input_tokens",
	"cache_write_input_tokens",
}

// postgresPromptCacheRawDataSQL projects raw_data down to promptCacheRawKeys
// server-side, so PostgreSQL sends a few bytes per row instead of the whole
// document. SQLite reads raw_data in-process, where its JSON functions are
// slower than promptCacheRawData, so it selects raw_data as is.
func postgresPromptCacheRawDataSQL() string {
	parts := make([]string, 0, len(promptCacheRawKeys))
	for _, key := range promptCacheRawKeys {
		parts = append(parts, "'"+key+"', raw_data->'"+key+"'")
	}
	return "jsonb_strip_nulls(jsonb_build_object(" + strings.Join(parts, ", ") + "))::text"
}

// promptCacheRawData decodes only promptCacheRawKeys from a raw_data JSON
// document, skipping the allocations of a full map decode. Non-numeric values
// are dropped, which EntryInputSegments would read as zero anyway.
func promptCacheRawData(rawDataJSON *string) map[string]any {
	if rawDataJSON == nil || *rawDataJSON == "" {
		return nil
	}
	var out map[string]any
	for i, value := range gjson.GetMany(*rawDataJSON, promptCacheRawKeys...) {
		if value.Type != gjson.Number {
			continue
		}
		if out == nil {
			out = make(map[string]any, len(promptCacheRawKeys))
		}
		out[promptCacheRawKeys[i]] = value.Num
	}
	return out
}
