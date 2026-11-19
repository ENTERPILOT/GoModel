package inferencehook

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Hook events, the top-level "type" of a frame.
const (
	eventPrompt   = "prompt"
	eventToolCall = "tool_call"
)

// source.application values. sourceTest is the one the protocol defines for
// connection tests; a server answers it like any other frame.
const (
	sourceGateway = "gomodel"
	sourceTest    = "config-test"
)

// frame is the request body of both hook events. Nullable fields are
// pointers without omitempty: the protocol sends null for a value that is
// not known, and a server may require the key.
type frame struct {
	Type      string            `json:"type"`
	RequestID string            `json:"request_id"`
	TenantID  *string           `json:"tenant_id"`
	Actor     actor             `json:"actor"`
	Source    source            `json:"source"`
	SessionID *string           `json:"session_id"`
	Model     *string           `json:"model"`
	Messages  []message         `json:"messages"`
	Metadata  map[string]string `json:"metadata"`
}

type actor struct {
	Type  string  `json:"type"`
	ID    *string `json:"id"`
	Email *string `json:"email_address"`
}

type source struct {
	Application string `json:"application"`
}

type message struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

type textBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolUseBlock struct {
	Type     string          `json:"type"`
	ID       *string         `json:"id"`
	ToolName *string         `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
	// ToolInfo is sent on tool call frames only, and then never null.
	ToolInfo *toolInfo `json:"tool_info,omitempty"`
}

type toolInfo struct {
	Type string `json:"type"`
}

type toolResultBlock struct {
	Type      string  `json:"type"`
	Content   string  `json:"content"`
	IsError   bool    `json:"is_error"`
	ToolName  *string `json:"tool_name"`
	ToolUseID *string `json:"tool_use_id"`
}

// attachmentBlock stands for a part GoModel cannot turn into text; see
// [attachment].
type attachmentBlock struct {
	Type      string  `json:"type"`
	FileName  *string `json:"file_name"`
	MediaType *string `json:"media_type"`
	SizeBytes *int64  `json:"size_bytes"`
	Text      *string `json:"text"`
}

// newFrame returns a frame of the given event with the request's identity
// and a fresh request_id, which is also the webhook-id of the delivery.
func (p *Plugin) newFrame(event string, meta pluginapi.Meta) frame {
	model := meta.Model
	if model == "" {
		model = meta.RequestedModel
	}
	f := frame{
		Type:      event,
		RequestID: uuid.NewString(),
		TenantID:  nullable(p.tenantID),
		Actor:     actor{Type: "user", ID: nullable(meta.AuthKeyID)},
		Source:    source{Application: sourceGateway},
		SessionID: nullable(meta.SessionID),
		Model:     nullable(model),
		Metadata:  map[string]string{},
	}
	// metadata is the protocol's extension map; a server tolerates any key.
	for key, value := range map[string]string{
		"gomodel_request_id": meta.RequestID,
		"gomodel_user_path":  meta.UserPath,
		"gomodel_provider":   meta.Provider,
	} {
		if value != "" {
			f.Metadata[key] = value
		}
	}
	return f
}

// testFrame is the synthetic frame of a connection test.
func testFrame(tenantID string) frame {
	return frame{
		Type:      eventPrompt,
		RequestID: uuid.NewString(),
		TenantID:  nullable(tenantID),
		Actor:     actor{Type: "user"},
		Source:    source{Application: sourceTest},
		Messages: []message{{Role: "user", Content: []any{
			textBlock{Type: "text", Text: "GoModel connection test."},
		}}},
		Metadata: map[string]string{},
	}
}

// promptMessages renders the prompt messages of the configured roles as
// the transcript of a prompt frame. The protocol knows the user and
// assistant roles only: tool results go under user, as in the Messages API,
// and so do system and developer messages, because a server skips a role
// it does not know. A message with nothing to send is left out.
func (p *Plugin) promptMessages(prompt *pluginapi.Prompt) []message {
	names := map[string]string{}
	for _, call := range prompt.ToolCalls() {
		names[call.Call.ID] = call.Call.Name
	}
	var out []message
	for _, m := range prompt.Messages {
		if !p.sends(m.Role) {
			continue
		}
		role := "user"
		if m.Role == pluginapi.RoleAssistant {
			role = "assistant"
		}
		if content := blocks(m.Parts, names, false); len(content) > 0 {
			out = append(out, message{Role: role, Content: content})
		}
	}
	return out
}

// sends reports whether messages of role go into the prompt frame: the
// configured choice for the roles GoModel models, and always for any other
// role, so a caller cannot move content past the hook by inventing one.
func (p *Plugin) sends(role pluginapi.Role) bool {
	switch role {
	case pluginapi.RoleSystem, pluginapi.RoleDeveloper, pluginapi.RoleUser, pluginapi.RoleAssistant, pluginapi.RoleTool:
		return p.roles[role]
	}
	return true
}

// toolCallMessage renders one completion choice as the assistant message of
// a tool call frame, or nil when the choice calls no tool.
func toolCallMessage(choice pluginapi.Choice) []message {
	calls := false
	for _, part := range choice.Message.Parts {
		calls = calls || (part.Kind == pluginapi.PartToolCall && part.ToolCall != nil)
	}
	if !calls {
		return nil
	}
	return []message{{Role: "assistant", Content: blocks(choice.Message.Parts, nil, true)}}
}

// blocks renders message parts as content blocks. On a tool call frame
// (toolCalls) each tool_use block says who runs the tool, and the model's
// reasoning is left out as the protocol leaves it out; in a prompt the
// caller wrote the reasoning it replays, so it is sent as text.
func blocks(parts []pluginapi.Part, names map[string]string, toolCalls bool) []any {
	var out []any
	for _, part := range parts {
		switch part.Kind {
		case pluginapi.PartText, pluginapi.PartRefusal:
			if part.Text != "" {
				out = append(out, textBlock{Type: "text", Text: part.Text})
			}
		case pluginapi.PartReasoning:
			if !toolCalls && part.Text != "" {
				out = append(out, textBlock{Type: "text", Text: part.Text})
			}
		case pluginapi.PartToolCall:
			if part.ToolCall != nil {
				out = append(out, toolUse(*part.ToolCall, toolCalls))
			}
		case pluginapi.PartToolResult:
			if part.ToolResult != nil {
				out = append(out, toolResult(*part.ToolResult, names))
			}
		default:
			out = append(out, attachment(part))
		}
	}
	return out
}

// attachment renders a part that is not text. Media is announced without
// its bytes, which is what Anthropic sends for an image. Content GoModel
// does not model (a Responses reasoning or hosted tool item, an unknown
// content block) is sent as the JSON the provider will receive, so the
// caller cannot use it to carry text past the hook.
func attachment(part pluginapi.Part) attachmentBlock {
	block := attachmentBlock{Type: "attachment", MediaType: nullable(part.MediaType)}
	if part.Kind == pluginapi.PartOpaque && len(part.Raw) > 0 {
		block.MediaType = nullable("application/json")
		block.Text = nullable(string(part.Raw))
	}
	return block
}

func toolUse(call pluginapi.ToolCall, withInfo bool) toolUseBlock {
	block := toolUseBlock{
		Type:     "tool_use",
		ID:       nullable(call.ID),
		ToolName: nullable(call.Name),
		Input:    json.RawMessage("null"),
	}
	switch {
	case len(call.Arguments) == 0:
	case json.Valid(call.Arguments):
		block.Input = call.Arguments
	default:
		// Arguments the model left unparseable still reach the server, as
		// the string they are.
		if quoted, err := json.Marshal(string(call.Arguments)); err == nil {
			block.Input = quoted
		}
	}
	if withInfo {
		// "client" is the protocol's kind for a tool the calling
		// application runs, which is every tool a gateway hands back.
		block.ToolInfo = &toolInfo{Type: "client"}
	}
	return block
}

// toolResult joins the result's text with newlines; other parts become a
// placeholder marker, as the protocol replaces binary parts.
func toolResult(result pluginapi.ToolResult, names map[string]string) toolResultBlock {
	lines := make([]string, 0, len(result.Parts))
	for _, part := range result.Parts {
		if part.Kind == pluginapi.PartText {
			lines = append(lines, part.Text)
			continue
		}
		lines = append(lines, "["+string(part.Kind)+"]")
	}
	return toolResultBlock{
		Type:      "tool_result",
		Content:   strings.Join(lines, "\n"),
		IsError:   result.IsError,
		ToolName:  nullable(names[result.CallID]),
		ToolUseID: nullable(result.CallID),
	}
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
