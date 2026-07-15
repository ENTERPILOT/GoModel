package mcpgateway

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/enterpilot/gomodel/internal/usage"
)

// recordToolCall emits one usage entry per tools/call so MCP traffic shows up
// in the same usage pipeline (and by-label breakdowns) as model traffic.
// Identity comes from the HTTP headers of the POST that carried the call —
// tool handlers run on the session context, not the per-request context.
func (s *Service) recordToolCall(req *mcp.CallToolRequest, server, exposedTool, endpoint string, started time.Time, result *mcp.CallToolResult, callErr error) {
	if s.usageLogger == nil || !s.usageLogger.Config().Enabled {
		return
	}

	entry := &usage.UsageEntry{
		ID:           uuid.NewString(),
		Timestamp:    time.Now().UTC(),
		Model:        exposedTool,
		Provider:     "mcp",
		ProviderName: server,
		Endpoint:     endpoint,
		RawData: map[string]any{
			"method":      "tools/call",
			"duration_ms": time.Since(started).Milliseconds(),
		},
	}
	if callErr != nil {
		entry.RawData["error"] = "upstream_failure"
	} else if result != nil && result.IsError {
		entry.RawData["error"] = "tool_error"
	}

	if extra := req.GetExtra(); extra != nil && extra.Header != nil {
		entry.RequestID = strings.TrimSpace(extra.Header.Get("X-Request-ID"))
		entry.UserPath = strings.TrimSpace(extra.Header.Get(s.userPathHeader))
		entry.Labels = parseLabelsHeader(extra.Header.Get(labelsHeader))
	}
	if req.Params != nil && len(req.Params.Arguments) > 0 {
		entry.RawData["args_bytes"] = len(req.Params.Arguments)
	}
	if callErr == nil && result != nil {
		if encoded, err := json.Marshal(result); err == nil {
			entry.RawData["result_bytes"] = len(encoded)
		}
	}

	s.usageLogger.Write(entry)
}

func parseLabelsHeader(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	labels := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			labels = append(labels, trimmed)
		}
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}
