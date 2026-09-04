package pluginapi

import (
	"encoding/json"
	"strings"
)

// Role is the author of a [Message].
type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool marks a tool result message (chat "tool" role, Responses
	// function_call_output).
	RoleTool Role = "tool"
)

// PartKind is the type of a content [Part]. Plugins must treat unknown kinds
// as opaque: new kinds may be added in minor releases.
type PartKind string

const (
	// PartText is plain text; Text is set.
	PartText PartKind = "text"
	// PartImage is an image; URL or Data+MediaType is set. A data: URI stays
	// in URL undecoded, with MediaType set from the URI.
	PartImage PartKind = "image"
	// PartAudio is inline audio; Data holds the payload as received (base64
	// text for chat input_audio) and MediaType the format ("audio/wav").
	PartAudio PartKind = "audio"
	// PartFile is a file reference or inline file; URL, Data, or Raw is set.
	PartFile PartKind = "file"
	// PartToolCall is a tool invocation by the assistant; ToolCall is set.
	PartToolCall PartKind = "tool_call"
	// PartToolResult is the result of a tool call; ToolResult is set.
	PartToolResult PartKind = "tool_result"
	// PartReasoning is model reasoning text; Text is set.
	PartReasoning PartKind = "reasoning"
	// PartRefusal is a model refusal; Text is set.
	PartRefusal PartKind = "refusal"
	// PartOpaque is content GoModel does not model. Raw holds the original
	// encoding and the part round-trips unchanged.
	PartOpaque PartKind = "opaque"
)

// Part is one piece of message content.
type Part struct {
	Kind PartKind
	// Text is the content of text, reasoning, and refusal parts.
	Text string
	// MediaType is the MIME type of media parts, when known.
	MediaType string
	// Data is inline media as received; nil when URL-referenced.
	Data []byte
	// URL references remote or data-URI media.
	URL string
	// ToolCall is set for [PartToolCall].
	ToolCall *ToolCall
	// ToolResult is set for [PartToolResult].
	ToolResult *ToolResult
	// Raw is the original JSON encoding of parts GoModel keeps verbatim
	// (opaque parts, and media parts of the Responses dialect). Read-only.
	Raw json.RawMessage
}

// ToolCall is a tool invocation emitted by the model.
type ToolCall struct {
	// ID is the provider-visible call id (chat tool_calls[].id, Responses
	// call_id, Anthropic tool_use.id).
	ID string
	// Name is the tool name.
	Name string
	// Arguments is the argument JSON. When the wire format carried the
	// arguments as a string that parses as JSON, the parsed value is exposed;
	// otherwise a JSON string.
	Arguments json.RawMessage
	// Server is the MCP server name for calls routed through the MCP gateway.
	Server string
}

// ToolResult is the result returned for a tool call.
type ToolResult struct {
	// CallID matches ToolCall.ID.
	CallID string
	// Parts is the result content: text, image, or opaque parts.
	Parts []Part
	// IsError reports a failed tool execution (Anthropic is_error).
	IsError bool
}

// Message is one turn of the conversation.
type Message struct {
	// ID is stable within the exchange and survives edits. The host assigns
	// IDs; messages inserted by plugins get "new-N" IDs.
	ID   string
	Role Role
	// Parts is the content in order. Tool calls follow any text.
	Parts []Part
	// Name is the optional participant name (chat "name").
	Name string
	// ToolCallID links a [RoleTool] message to the call it answers.
	ToolCallID string
	// CacheBreakpoint reports an explicit prompt-cache marker on this
	// message (Anthropic cache_control).
	CacheBreakpoint bool
}

// TextMessage builds a message with a single text part.
func TextMessage(role Role, text string) Message {
	return Message{Role: role, Parts: []Part{{Kind: PartText, Text: text}}}
}

// Text returns the message's text parts concatenated in order. Tool result
// text is included for [RoleTool] messages.
func (m Message) Text() string {
	var b strings.Builder
	for _, part := range m.Parts {
		switch part.Kind {
		case PartText:
			b.WriteString(part.Text)
		case PartToolResult:
			if part.ToolResult == nil {
				continue
			}
			for _, inner := range part.ToolResult.Parts {
				if inner.Kind == PartText {
					b.WriteString(inner.Text)
				}
			}
		}
	}
	return b.String()
}
