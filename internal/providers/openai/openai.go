// Package openai provides OpenAI API integration for the LLM gateway.
package openai

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// Registration provides factory registration for the OpenAI provider.
var Registration = providers.Registration{
	Type:                        "openai",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

const (
	defaultBaseURL = "https://api.openai.com/v1"
)

// Provider implements the core.Provider interface for OpenAI.
// Credentials and the realtime base URL are both read live from the embedded
// CompatibleProvider, so SetBaseURL overrides and key rotation are honored on
// the realtime websocket dial target too (see realtime.go).
type Provider struct {
	*CompatibleProvider
}

// New creates a new OpenAI provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	return &Provider{
		CompatibleProvider: NewCompatibleProvider(cfg.APIKey, opts, CompatibleProviderConfig{
			ProviderName:     "openai",
			BaseURL:          baseURL,
			SetHeaders:       setHeaders,
			AdaptChatRequest: adaptChatRequest,
		}),
	}
}

// setHeaders sets the required headers for OpenAI API requests.
// OpenAI requires the request ID to be ASCII-only and at most 512 bytes,
// otherwise it returns 400, so forwarding is gated by
// providers.IsValidClientRequestID.
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:        "Bearer ",
		RequestIDHeader:   "X-Client-Request-Id",
		ValidateRequestID: providers.IsValidClientRequestID,
	})
}

// isOSeriesModel reports whether the model is an OpenAI o-series model
// (o1, o3, o4) that requires max_completion_tokens instead of max_tokens
// and does not support the temperature parameter.
func isOSeriesModel(model string) bool {
	m := strings.ToLower(model)
	// Match o1, o3, o4 families (e.g. o3-mini, o4-mini, o3, o1-preview).
	// Non-reasoning models like gpt-4o start with "gpt-", not "o".
	return len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

// gptVersion parses the generation of an OpenAI GPT model name: gpt-5 is 5.0,
// gpt-5.6-terra is 5.6, gpt-6.1-sol is 6.1. ok is false for names outside the
// gpt-<major>[.<minor>][-<variant>] scheme, such as gpt-4o or gpt-5x.
func gptVersion(model string) (major, minor int, ok bool) {
	rest, found := strings.CutPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-")
	if !found {
		return 0, 0, false
	}
	if major, rest, ok = leadingInt(rest); !ok {
		return 0, 0, false
	}
	if after, dotted := strings.CutPrefix(rest, "."); dotted {
		if minor, rest, ok = leadingInt(after); !ok {
			return 0, 0, false
		}
	}
	if rest != "" && rest[0] != '-' {
		return 0, 0, false
	}
	return major, minor, true
}

// leadingInt splits the decimal digits at the start of s from the remainder.
func leadingInt(s string) (int, string, bool) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, s, false
	}
	return n, s[end:], true
}

// isGPT5PlusModel reports whether the model belongs to GPT-5 or a later
// generation (gpt-5-mini, gpt-5.6-terra, gpt-6-sol, gpt-6.1-sol, …). Every
// generation since GPT-5 follows the reasoning chat parameter rules, so a new
// one is covered without a code change.
func isGPT5PlusModel(model string) bool {
	major, _, ok := gptVersion(model)
	return ok && major >= 5
}

// isReasoningChatModel reports whether the model follows OpenAI's reasoning
// chat parameter rules for max_completion_tokens and temperature handling.
func isReasoningChatModel(model string) bool {
	return isOSeriesModel(model) || isGPT5PlusModel(model)
}

// restrictsChatTools reports whether OpenAI accepts function tools on Chat
// Completions for the model only with reasoning_effort "none", which holds from
// GPT-5.6 on.
func restrictsChatTools(model string) bool {
	major, minor, ok := gptVersion(model)
	return ok && (major > 5 || (major == 5 && minor >= 6))
}

// rejectsNoReasoning reports whether the model refuses reasoning_effort
// "none" (gpt-6-astra and gpt-6.1), so it can call tools only through
// /v1/responses.
func rejectsNoReasoning(model string) bool {
	major, minor, ok := gptVersion(model)
	if ok && major == 6 && minor == 1 {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-6-astra")
}

// chatToolsRequireNoReasoning reports whether a tool request that sets no
// effort must be sent with reasoning_effort "none" to stay on Chat Completions.
func chatToolsRequireNoReasoning(model string) bool {
	return restrictsChatTools(model) && !rejectsNoReasoning(model)
}

// adaptForReasoningChat rewrites a ChatRequest body for OpenAI reasoning chat
// models, mapping max_tokens -> max_completion_tokens and dropping temperature
// while preserving all unknown top-level JSON fields. It works on the typed
// request directly so the body is marshaled only once, by the HTTP client.
func adaptForReasoningChat(req *core.ChatRequest) (any, error) {
	adapted := *req
	adapted.Temperature = nil
	if req.MaxTokens != nil {
		adapted.MaxTokens = nil
		extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
			"max_completion_tokens": json.RawMessage(strconv.Itoa(*req.MaxTokens)),
		})
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to adapt reasoning request: "+err.Error(), err)
		}
		adapted.ExtraFields = extra
	}
	return &adapted, nil
}

// isNonReasoningChatModel reports whether the model belongs to an OpenAI
// family that rejects reasoning_effort on Chat Completions (gpt-3.5, gpt-4*,
// chatgpt-4o). Unknown models are not included: custom OpenAI-compatible
// endpoints serve arbitrary names and may accept the field.
func isNonReasoningChatModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "gpt-3.5") || strings.HasPrefix(m, "gpt-4") || strings.HasPrefix(m, "chatgpt-")
}

// adaptChatRequest maps GoModel's nested reasoning shape (set by the Messages
// API's thinking and by clients sending reasoning.effort) onto the flat
// reasoning_effort field: OpenAI Chat Completions rejects "reasoning".
// Models that cannot reason reject reasoning_effort too, so it is dropped.
//
// A tool request that asks for no reasoning effort gets reasoning_effort
// "none" on models that reject function tools otherwise: their default effort
// is not "none", so the request would fail as sent.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return req, nil
	}
	req = nameInlineFiles(req)
	effort := ""
	if req.Reasoning != nil {
		effort = strings.TrimSpace(req.Reasoning.Effort)
	}
	if effort == "" && len(req.Tools) > 0 && chatToolsRequireNoReasoning(req.Model) &&
		!req.ExtraFields.HasAny("reasoning_effort") {
		return providers.AdaptReasoningEffortRequest(req, "none")
	}
	if req.Reasoning == nil {
		return req, nil
	}
	if effort == "" || isNonReasoningChatModel(req.Model) {
		return providers.DropReasoning(req), nil
	}
	return providers.AdaptReasoningEffortRequest(req, effort)
}

// nameInlineFiles gives inline file parts that arrived without a filename the
// default name OpenAI requires alongside file_data. The caller's request is
// left unchanged; it is returned as-is when no part needs a name.
func nameInlineFiles(req *core.ChatRequest) *core.ChatRequest {
	adapted := req
	for i, msg := range req.Messages {
		parts, ok := msg.Content.([]core.ContentPart)
		if !ok || !slices.ContainsFunc(parts, unnamedInlineFile) {
			continue
		}
		named := slices.Clone(parts)
		for j, part := range named {
			if unnamedInlineFile(part) {
				file := *part.File
				file.Filename = core.DefaultFilename(file.FileData)
				named[j].File = &file
			}
		}
		if adapted == req {
			cloned := *req
			cloned.Messages = slices.Clone(req.Messages)
			adapted = &cloned
		}
		adapted.Messages[i].Content = named
	}
	return adapted
}

func unnamedInlineFile(part core.ContentPart) bool {
	return part.Type == "file" && part.File != nil && part.File.FileData != "" && part.File.Filename == ""
}

// chatRequestBody returns the appropriate request body for the model.
// Reasoning models get parameter adaptation; others pass through as-is.
func chatRequestBody(req *core.ChatRequest) (any, error) {
	if isReasoningChatModel(req.Model) {
		return adaptForReasoningChat(req)
	}
	return req, nil
}
