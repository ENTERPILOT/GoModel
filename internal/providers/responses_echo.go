package providers

import (
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// ResponsesRequestEcho returns the request members OpenAI repeats on the
// Response object it returns. Providers reached through chat translation have
// no Response object upstream, so the gateway echoes them itself and both the
// streamed and the non-streamed answer carry the same members.
//
// Only what the caller actually supplied is echoed: a default invented here
// (parallel_tool_calls, store, truncation…) would describe OpenAI's behaviour,
// not the translated provider's.
func ResponsesRequestEcho(req *core.ResponsesRequest) map[string]json.RawMessage {
	if req == nil {
		return nil
	}

	echo := make(map[string]json.RawMessage, 12)
	add := func(name string, value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			return
		}
		echo[name] = raw
	}

	if req.Instructions != "" {
		add("instructions", req.Instructions)
	}
	if req.Metadata != nil {
		add("metadata", req.Metadata)
	}
	if req.Tools != nil {
		add("tools", req.Tools)
	}
	if req.ToolChoice != nil {
		add("tool_choice", req.ToolChoice)
	}
	if req.ParallelToolCalls != nil {
		add("parallel_tool_calls", req.ParallelToolCalls)
	}
	if req.Temperature != nil {
		add("temperature", req.Temperature)
	}
	if req.TopP != nil {
		add("top_p", req.TopP)
	}
	if req.MaxOutputTokens != nil {
		add("max_output_tokens", req.MaxOutputTokens)
	}
	if req.Store != nil {
		add("store", req.Store)
	}
	if req.Text != nil {
		add("text", req.Text)
	}
	if req.Reasoning != nil {
		add("reasoning", req.Reasoning)
	}
	// truncation is an enum, so the echo carries the canonical spelling the
	// validator accepted rather than the caller's padding.
	if truncation := strings.TrimSpace(req.Truncation); truncation != "" {
		add("truncation", truncation)
	}
	if req.User != "" {
		add("user", req.User)
	}
	if req.ServiceTier != "" {
		add("service_tier", req.ServiceTier)
	}

	if len(echo) == 0 {
		return nil
	}
	return echo
}

// ApplyResponsesRequestEcho adds the echoed request members to a response built
// by chat translation. Members the provider already produced win: the echo only
// fills what translation cannot carry.
func ApplyResponsesRequestEcho(resp *core.ResponsesResponse, req *core.ResponsesRequest) {
	if resp == nil {
		return
	}
	echo := ResponsesRequestEcho(req)
	for name := range echo {
		if len(resp.ExtraFields.Lookup(name)) > 0 {
			delete(echo, name)
		}
	}
	merged, err := core.MergeUnknownJSONFields(resp.ExtraFields, echo)
	if err != nil {
		return
	}
	resp.ExtraFields = merged
}
