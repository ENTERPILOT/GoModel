package geminiapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// DecodeGenerateContentRequest parses a generateContent request body.
func DecodeGenerateContentRequest(body []byte) (*GenerateContentRequest, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("request body is empty")
	}
	var req GenerateContentRequest
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&req); err != nil {
		return nil, err
	}
	// The body must hold exactly one JSON value, so trailing bytes cannot
	// make the logged request disagree with the parsed one.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, fmt.Errorf("request body must contain a single JSON object")
	}
	return &req, nil
}

// ToChatRequest translates a generateContent request into the canonical chat
// request. model comes from the request path and stream from the method
// (streamGenerateContent). The translation is provider-agnostic. A member the
// canonical request cannot represent is rejected with a 400 that names it
// when dropping it would change what the model is asked (cached content,
// built-in tools, file references); tuning members without a portable
// equivalent (topK, safetySettings, …) are ignored.
func ToChatRequest(req *GenerateContentRequest, model string, stream bool) (*core.ChatRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("generateContent request is required", nil)
	}
	if strings.TrimSpace(model) == "" {
		return nil, core.NewInvalidRequestError("model is required", nil).WithParam("model")
	}
	if len(req.Contents) == 0 {
		return nil, core.NewInvalidRequestError("contents must not be empty", nil).WithParam("contents")
	}
	if req.cachedContent() != "" {
		return nil, unsupported("cachedContent", "cachedContent")
	}

	messages, err := convertContents(req)
	if err != nil {
		return nil, err
	}
	chat := &core.ChatRequest{
		Model:    model,
		Messages: messages,
		Stream:   stream,
	}
	if stream {
		chat.StreamOptions = &core.StreamOptions{IncludeUsage: true}
	}

	if chat.Tools, err = convertTools(req.Tools); err != nil {
		return nil, err
	}
	toolChoice, err := convertToolConfig(req.toolConfig().functionCallingConfig())
	if err != nil {
		return nil, err
	}
	// A tool choice without tools is an error on OpenAI-family providers,
	// while Gemini accepts a toolConfig on its own.
	if len(chat.Tools) > 0 {
		chat.ToolChoice = toolChoice
	}

	if err := applyGenerationConfig(chat, req.generationConfig()); err != nil {
		return nil, err
	}
	return chat, nil
}

// IncludeThoughts reports whether the request asked for the model's thoughts
// (generationConfig.thinkingConfig.includeThoughts).
func IncludeThoughts(req *GenerateContentRequest) bool {
	if req == nil {
		return false
	}
	cfg := req.generationConfig()
	if cfg == nil || cfg.thinkingConfig() == nil {
		return false
	}
	return cfg.thinkingConfig().includeThoughts()
}

// unsupported is the 400 for a request member the translation cannot carry.
func unsupported(member, param string) *core.GatewayError {
	return core.NewInvalidRequestError(member+" is not supported on this endpoint", nil).WithParam(param)
}

// pendingCall is a function call of the latest model turn that still waits
// for its functionResponse.
type pendingCall struct {
	id       string
	name     string
	answered bool
}

// contentConverter flattens Gemini contents into canonical messages. Chat
// matches a tool result to its call by ID, while Gemini 2.5 function calls
// carry none and are matched by name and order, so every call gets an ID:
// its own when the client sent one, otherwise a generated call_<n> that no
// call of the request uses.
type contentConverter struct {
	messages []core.Message
	pending  []pendingCall
	// supplied holds the function call IDs the client sent, anywhere in the
	// request, so a generated ID never repeats one of them.
	supplied map[string]struct{}
	nextID   int
}

func convertContents(req *GenerateContentRequest) ([]core.Message, error) {
	conv := &contentConverter{
		messages: make([]core.Message, 0, len(req.Contents)+1),
		supplied: suppliedCallIDs(req.Contents),
	}
	if system := req.systemInstruction(); system != nil {
		text, err := systemText(system)
		if err != nil {
			return nil, err
		}
		if text != "" {
			conv.messages = append(conv.messages, core.Message{Role: "system", Content: text})
		}
	}
	for i, content := range req.Contents {
		var err error
		switch strings.TrimSpace(content.Role) {
		case "model":
			err = conv.modelTurn(content.Parts)
		case "", "user", "function":
			err = conv.userTurn(content.Parts)
		default:
			err = fmt.Errorf("role must be \"user\" or \"model\"")
		}
		if err != nil {
			if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
				return nil, gatewayErr
			}
			return nil, core.NewInvalidRequestError(fmt.Sprintf("contents[%d]: %v", i, err), nil).WithParam("contents")
		}
	}
	return conv.messages, nil
}

// systemText joins the text parts of the system instruction, which holds
// text only.
func systemText(system *Content) (string, error) {
	texts := make([]string, 0, len(system.Parts))
	for _, part := range system.Parts {
		if part.inlineData() != nil || part.functionCall() != nil || part.functionResponse() != nil || part.unsupportedMember() != "" {
			return "", core.NewInvalidRequestError("systemInstruction supports text parts only", nil).WithParam("systemInstruction")
		}
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n"), nil
}

// modelTurn converts a model turn into one assistant message. Thought parts
// are reasoning the model already produced and are not replayed; thought
// signatures travel under extra_content.google, which the router drops for
// every provider but Gemini.
func (c *contentConverter) modelTurn(parts []Part) error {
	var (
		texts     []string
		toolCalls []core.ToolCall
		signature string
	)
	c.pending = c.pending[:0]
	for _, part := range parts {
		if member := part.unsupportedMember(); member != "" {
			return unsupported(member, "contents")
		}
		if part.inlineData() != nil {
			return unsupported("inlineData in a model turn", "contents")
		}
		if part.functionResponse() != nil {
			return fmt.Errorf("functionResponse belongs in a user turn")
		}
		if call := part.functionCall(); call != nil {
			if strings.TrimSpace(call.Name) == "" {
				return fmt.Errorf("functionCall.name is required")
			}
			id := c.callID(call.ID)
			c.pending = append(c.pending, pendingCall{id: id, name: call.Name})
			toolCalls = append(toolCalls, core.ToolCall{
				ID:          id,
				Type:        "function",
				Function:    core.FunctionCall{Name: call.Name, Arguments: rawToArguments(call.Args)},
				ExtraFields: thoughtSignatureFields(part.thoughtSignature()),
			})
			continue
		}
		if part.Thought {
			continue
		}
		if part.Text != "" {
			texts = append(texts, part.Text)
		}
		if sig := part.thoughtSignature(); sig != "" {
			signature = sig
		}
	}
	if len(texts) == 0 && len(toolCalls) == 0 {
		return nil
	}
	msg := core.Message{
		Role:        "assistant",
		ToolCalls:   toolCalls,
		ExtraFields: thoughtSignatureFields(signature),
	}
	if len(texts) > 0 {
		msg.Content = strings.Join(texts, "")
	} else {
		// A tool-call-only turn carries content: null, as in OpenAI's shape.
		msg.ContentNull = true
	}
	c.messages = append(c.messages, msg)
	return nil
}

// userTurn converts a user turn. Function responses become role:"tool"
// messages placed before the turn's other content, the order chat expects:
// tool results directly after the assistant tool calls, then any follow-up.
func (c *contentConverter) userTurn(parts []Part) error {
	var content []core.ContentPart
	for _, part := range parts {
		if member := part.unsupportedMember(); member != "" {
			return unsupported(member, "contents")
		}
		if part.functionCall() != nil {
			return fmt.Errorf("functionCall belongs in a model turn")
		}
		if response := part.functionResponse(); response != nil {
			id, err := c.answer(response)
			if err != nil {
				return err
			}
			c.messages = append(c.messages, core.Message{
				Role:       "tool",
				ToolCallID: id,
				Content:    rawToArguments(response.Response),
			})
			continue
		}
		if blob := part.inlineData(); blob != nil {
			media, err := mediaPart(blob)
			if err != nil {
				return err
			}
			content = append(content, media)
			continue
		}
		if part.Text != "" {
			content = append(content, core.ContentPart{Type: "text", Text: part.Text})
		}
	}
	if collapsed := collapseParts(content); collapsed != nil {
		c.messages = append(c.messages, core.Message{Role: "user", Content: collapsed})
	}
	return nil
}

// suppliedCallIDs collects the function call IDs the client sent.
func suppliedCallIDs(contents []Content) map[string]struct{} {
	supplied := make(map[string]struct{})
	for _, content := range contents {
		for _, part := range content.Parts {
			if call := part.functionCall(); call != nil {
				if id := strings.TrimSpace(call.ID); id != "" {
					supplied[id] = struct{}{}
				}
			}
		}
	}
	return supplied
}

// callID returns the ID a function call travels under: the client's, or the
// next generated one the client did not use itself.
func (c *contentConverter) callID(id string) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	for {
		id = "call_" + strconv.Itoa(c.nextID)
		c.nextID++
		if _, taken := c.supplied[id]; !taken {
			return id
		}
	}
}

// answer finds the pending call a function response belongs to: the one with
// its ID, or else the first unanswered call of the same name.
func (c *contentConverter) answer(response *FunctionResponse) (string, error) {
	if id := strings.TrimSpace(response.ID); id != "" {
		if call := c.unanswered(func(call pendingCall) bool { return call.id == id }); call != nil {
			return call.id, nil
		}
	}
	if call := c.unanswered(func(call pendingCall) bool { return call.name == response.Name }); call != nil {
		return call.id, nil
	}
	return "", fmt.Errorf("functionResponse %q does not answer a functionCall of the preceding model turn", response.Name)
}

// unanswered marks the first pending call that matches as answered and
// returns it, or nil when none does.
func (c *contentConverter) unanswered(matches func(pendingCall) bool) *pendingCall {
	for i := range c.pending {
		if call := &c.pending[i]; !call.answered && matches(*call) {
			call.answered = true
			return call
		}
	}
	return nil
}

// mediaPart converts inline media. Images become image_url parts, which every
// vision provider accepts; any other type travels as a file part, which the
// Gemini provider sends on as inline data again.
func mediaPart(blob *Blob) (core.ContentPart, error) {
	mimeType := strings.TrimSpace(blob.mimeType())
	if mimeType == "" || blob.Data == "" {
		return core.ContentPart{}, fmt.Errorf("inlineData requires mimeType and data")
	}
	dataURL := "data:" + mimeType + ";base64," + blob.Data
	if strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return core.ContentPart{Type: "image_url", ImageURL: &core.ImageURLContent{URL: dataURL}}, nil
	}
	return core.ContentPart{
		Type: "file",
		File: &core.FileContent{FileData: dataURL, Filename: core.DefaultFilename(dataURL)},
	}, nil
}

// collapseParts reduces content parts to a plain string when they are all
// text, and otherwise returns the typed parts. It returns nil for no content.
func collapseParts(parts []core.ContentPart) core.MessageContent {
	if len(parts) == 0 {
		return nil
	}
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type != "text" {
			return parts
		}
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, "\n")
}

// rawToArguments renders a JSON object member (function call args, function
// response) as the compact JSON string chat carries it in.
func rawToArguments(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || core.IsJSONNull(trimmed) {
		return "{}"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "{}"
	}
	return compact.String()
}

// googleExtraContent is the extra_content.google object the Gemini provider
// reads thought signatures from.
type googleExtraContent struct {
	ThoughtSignature string `json:"thought_signature"`
}

func thoughtSignatureFields(signature string) core.UnknownJSONFields {
	if signature == "" {
		return core.UnknownJSONFields{}
	}
	encoded, err := json.Marshal(googleExtraContent{ThoughtSignature: signature})
	if err != nil {
		return core.UnknownJSONFields{}
	}
	fields, err := core.UnknownJSONFields{}.WithExtraContent(core.ExtraContentVendorGoogle, encoded)
	if err != nil {
		return core.UnknownJSONFields{}
	}
	return fields
}

func thoughtSignatureOf(fields core.UnknownJSONFields) string {
	raw := fields.ExtraContent(core.ExtraContentVendorGoogle)
	if len(raw) == 0 {
		return ""
	}
	var extra googleExtraContent
	if err := json.Unmarshal(raw, &extra); err != nil {
		return ""
	}
	return extra.ThoughtSignature
}
