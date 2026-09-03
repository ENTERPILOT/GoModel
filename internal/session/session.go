// Package session identifies the client session a request belongs to, so the
// gateway can route one conversation consistently and group its audit entries.
package session

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Rule identifies where a client session id lives in a request.
type Rule struct {
	// Source is SourceHeader or SourceBody.
	Source string
	// Header is the HTTP header name to read (Source == SourceHeader).
	Header string
	// BodyPath is the gjson path into the JSON request body (Source == SourceBody).
	BodyPath string
	// Transform optionally post-processes the raw value; an empty result means
	// the rule did not match. Empty string uses the value as-is.
	Transform string
}

const (
	SourceHeader = "header"
	SourceBody   = "body"

	// TransformSessionUUID extracts a session UUID from Anthropic
	// metadata.user_id values. Claude Code sends either a JSON object string
	// with a "session_id" field or the legacy "user_<hash>_…_session_<uuid>"
	// form; both embed the session UUID this transform returns.
	TransformSessionUUID = "session-uuid"
)

// builtinRules is the default registry of session signals known coding tools
// and gateway conventions send, evaluated in order (headers before body
// fields). Sources: Claude Code documents x-claude-code-session-id for
// gateways; Codex CLI sends session-id (older releases session_id, which Roo
// Code also uses on Responses API providers); OpenCode and Kilo Code send
// x-session-id; clients speaking OpenCode Zen's dialect (OpenCode, pi) send
// x-opencode-session; Goose sends agent-session-id; x-litellm-session-id and
// helicone-session-id are gateway conventions. Body fields: metadata.user_id
// (Claude Code via /v1/messages), session_id (OpenRouter convention),
// litellm_session_id (LiteLLM), prompt_cache_key (Zed and other OpenAI
// clients reusing the cache key per conversation), and /v1/responses
// conversation references.
var builtinRules = []Rule{
	{Source: SourceHeader, Header: "X-Session-Id"},
	{Source: SourceHeader, Header: "X-Opencode-Session"},
	{Source: SourceHeader, Header: "X-Claude-Code-Session-Id"},
	{Source: SourceHeader, Header: "Session-Id"},
	{Source: SourceHeader, Header: "Session_id"},
	{Source: SourceHeader, Header: "X-Litellm-Session-Id"},
	{Source: SourceHeader, Header: "Helicone-Session-Id"},
	{Source: SourceHeader, Header: "Agent-Session-Id"},
	{Source: SourceBody, BodyPath: "metadata.user_id", Transform: TransformSessionUUID},
	{Source: SourceBody, BodyPath: "session_id"},
	{Source: SourceBody, BodyPath: "litellm_session_id"},
	{Source: SourceBody, BodyPath: "prompt_cache_key"},
	{Source: SourceBody, BodyPath: "conversation"},
	{Source: SourceBody, BodyPath: "conversation.id"},
}

// BuiltinRules returns a copy of the default registry.
func BuiltinRules() []Rule {
	rules := make([]Rule, len(builtinRules))
	copy(rules, builtinRules)
	return rules
}

const maxSessionIDLength = 200

var sessionUUIDRegex = regexp.MustCompile(`session_([0-9a-fA-F-]{36})`)

// cleanSessionID trims a candidate session id and rejects values that are
// empty, oversized, or carry control characters.
func cleanSessionID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxSessionIDLength {
		return ""
	}
	if strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	return value
}

// applyTransform post-processes a raw rule value. An empty result means the
// rule yielded no session id and the next rule should be tried.
func applyTransform(value, transform string) string {
	switch transform {
	case TransformSessionUUID:
		return extractSessionUUID(value)
	default:
		return value
	}
}

// extractSessionUUID pulls the session UUID out of an Anthropic
// metadata.user_id value in either of its known shapes.
func extractSessionUUID(value string) string {
	if strings.HasPrefix(value, "{") {
		var payload struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(value), &payload); err == nil {
			if id := strings.TrimSpace(payload.SessionID); id != "" {
				return id
			}
		}
	}
	if m := sessionUUIDRegex.FindStringSubmatch(value); m != nil {
		return m[1]
	}
	return ""
}
