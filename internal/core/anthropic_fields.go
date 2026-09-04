package core

// Message-level extra fields that carry Anthropic Messages history through the
// canonical chat request. They are set by the Anthropic ingress translator,
// consumed by the Anthropic provider, and stripped before any other provider
// sees the request (see providers.adaptAnthropicCacheControl).
const (
	// ThinkingBlocksField holds the raw thinking/redacted_thinking blocks of an
	// assistant turn as a JSON array. Anthropic requires them back verbatim
	// (with signatures) when a thinking-enabled tool-use turn continues.
	ThinkingBlocksField = "thinking_blocks"
	// ToolResultIsErrorField marks a tool message as a failed tool call
	// (Anthropic tool_result.is_error).
	ToolResultIsErrorField = "is_error"
)
