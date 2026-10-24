package usage

import (
	"fmt"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
)

// EndpointUsage holds per-endpoint token usage aggregates. Operation names the
// gateway operation the endpoint belongs to (core.DescribeEndpointPath), so
// clients can fold endpoints that mean the same thing: /v1/chat/completions
// and /v1/messages are both chat_completions. It is empty for endpoints the
// gateway does not classify.
type EndpointUsage struct {
	Endpoint     string   `json:"endpoint"`
	Operation    string   `json:"operation,omitempty"`
	Requests     int      `json:"requests"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	TotalTokens  int64    `json:"total_tokens"`
	InputCost    *float64 `json:"input_cost" extensions:"x-nullable"`
	OutputCost   *float64 `json:"output_cost" extensions:"x-nullable"`
	TotalCost    *float64 `json:"total_cost" extensions:"x-nullable"`
	GroupCacheFields
}

// endpointOperation classifies a stored endpoint into its gateway operation.
func endpointOperation(endpoint string) string {
	return string(core.DescribeEndpointPath(endpoint).Operation)
}

// endpointGroupKeys returns the grouping key for a usage row's endpoint.
func endpointGroupKeys(row usageCacheStatRow) []string {
	return []string{row.Endpoint}
}

// applyEndpointCacheStats merges the fold onto matching by-endpoint rows,
// materializing local-only groups like applyLabelCacheStats, and tags every
// row with its operation.
func applyEndpointCacheStats(rows []EndpointUsage, stats map[string]*GroupCacheStats) []EndpointUsage {
	seen := map[string]bool{}
	for i := range rows {
		seen[rows[i].Endpoint] = true
		rows[i].assign(stats[rows[i].Endpoint])
	}
	for key, s := range stats {
		if seen[key] || !s.hasLocalTokens() {
			continue
		}
		row := EndpointUsage{Endpoint: key, Requests: s.LocalRequests}
		row.assign(s)
		rows = append(rows, row)
	}
	for i := range rows {
		rows[i].Operation = endpointOperation(rows[i].Endpoint)
	}
	return rows
}

// operationEndpoints lists the endpoint values the operations cover: exact
// values compare by equality and each prefix also matches anything under
// "prefix/". Exact paths also match with one trailing slash, as
// core.DescribeEndpointPath does. Prefixes are listed in exact too, so exact
// is empty only when no operation has paths; the builders below then match
// nothing.
func operationEndpoints(ops []core.Operation) (exact, prefixes []string) {
	for _, op := range ops {
		paths, _ := core.PathsForOperation(op)
		for _, path := range paths.Exact {
			exact = append(exact, path, path+"/")
		}
		for _, prefix := range paths.Prefixes {
			exact = append(exact, prefix)
			prefixes = append(prefixes, prefix)
		}
	}
	return exact, prefixes
}

// sqliteOperationCondition matches rows whose endpoint belongs to any of ops.
func sqliteOperationCondition(ops []core.Operation) (string, []any) {
	exact, prefixes := operationEndpoints(ops)
	if len(exact) == 0 {
		return "1 = 0", nil
	}
	clauses := []string{"endpoint IN (" + strings.TrimSuffix(strings.Repeat("?,", len(exact)), ",") + ")"}
	args := make([]any, 0, len(exact)+len(prefixes))
	for _, path := range exact {
		args = append(args, path)
	}
	for _, prefix := range prefixes {
		clauses = append(clauses, "endpoint LIKE ? ESCAPE '\\'")
		args = append(args, sqlutil.EscapeLikeWildcards(prefix)+"/%")
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

// pgOperationCondition matches rows whose endpoint belongs to any of ops,
// numbering placeholders from argIdx.
func pgOperationCondition(ops []core.Operation, argIdx int) (string, []any, int) {
	exact, prefixes := operationEndpoints(ops)
	if len(exact) == 0 {
		return "1 = 0", nil, argIdx
	}
	clauses := []string{fmt.Sprintf("endpoint = ANY($%d)", argIdx)}
	args := []any{exact}
	argIdx++
	for _, prefix := range prefixes {
		clauses = append(clauses, fmt.Sprintf("endpoint LIKE $%d ESCAPE '\\'", argIdx))
		args = append(args, sqlutil.EscapeLikeWildcards(prefix)+"/%")
		argIdx++
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args, argIdx
}

// mongoOperationFilter matches documents whose endpoint belongs to any of ops.
func mongoOperationFilter(ops []core.Operation) bson.D {
	exact, prefixes := operationEndpoints(ops)
	if len(exact) == 0 {
		// A nil slice would encode as BSON null, which $in rejects.
		return bson.D{{Key: "endpoint", Value: bson.D{{Key: "$in", Value: bson.A{}}}}}
	}
	clauses := bson.A{bson.D{{Key: "endpoint", Value: bson.D{{Key: "$in", Value: exact}}}}}
	for _, prefix := range prefixes {
		clauses = append(clauses, bson.D{{Key: "endpoint", Value: bson.D{
			{Key: "$regex", Value: "^" + regexp.QuoteMeta(prefix+"/")},
		}}})
	}
	return bson.D{{Key: "$or", Value: clauses}}
}
