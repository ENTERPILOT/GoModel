// Package vllm provides vLLM OpenAI-compatible API integration for the LLM gateway.
package vllm

import (
	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// adaptChatRequest renames the legacy reasoning_content field to reasoning on
// replayed assistant messages.
//
// vLLM >=0.20 (vllm-project/vllm RFC #27755) reads only the "reasoning"
// field on incoming assistant messages and silently discards
// "reasoning_content" before its chat template ever runs. Clients built
// against the older field name (e.g. OpenCode) intend to replay a prior
// turn's thinking so Qwen3-family "preserve thinking" behavior keeps
// working across turns, but that content is dropped on the floor with no
// error, degrading multi-turn/agentic quality silently.
//
// Renaming here restores that continuity without requiring a client change.
// It only acts when reasoning_content is present and reasoning is absent, so
// it never overrides a client already using the current field name and never
// touches requests that carry neither field.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return req, nil
	}

	var adapted *core.ChatRequest
	for i, message := range req.Messages {
		if message.Role != "assistant" {
			continue
		}

		legacy := message.ExtraFields.Lookup("reasoning_content")
		if legacy == nil || message.ExtraFields.Lookup("reasoning") != nil {
			continue
		}

		extra, err := core.MergeUnknownJSONFields(message.ExtraFields.Without("reasoning_content"), map[string]json.RawMessage{
			"reasoning": legacy,
		})
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to adapt vLLM reasoning field: "+err.Error(), err)
		}
		if adapted == nil {
			copy := *req
			copy.Messages = append([]core.Message(nil), req.Messages...)
			adapted = &copy
		}
		adapted.Messages[i].ExtraFields = extra
	}

	if adapted == nil {
		return req, nil
	}
	return adapted, nil
}
