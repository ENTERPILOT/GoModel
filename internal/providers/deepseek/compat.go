package deepseek

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// jsonSchemaModeEnvVar selects how a json_schema response_format is handled
// for DeepSeek models, which document only json_object output.
const jsonSchemaModeEnvVar = "DEEPSEEK_JSON_SCHEMA_MODE"

// JSONSchemaMode is the handling applied to response_format type json_schema.
type JSONSchemaMode string

const (
	// JSONSchemaDowngrade sends json_object and adds the schema as a system
	// instruction. The output is JSON but no longer schema-enforced upstream.
	JSONSchemaDowngrade JSONSchemaMode = "downgrade"
	// JSONSchemaError rejects the request with an invalid_request_error.
	JSONSchemaError JSONSchemaMode = "error"
)

// LoadJSONSchemaMode resolves DEEPSEEK_JSON_SCHEMA_MODE, defaulting to
// downgrade so clients routed to DeepSeek through a virtual model keep working.
func LoadJSONSchemaMode() JSONSchemaMode {
	value := JSONSchemaMode(strings.ToLower(strings.TrimSpace(os.Getenv(jsonSchemaModeEnvVar))))
	switch value {
	case "":
		return JSONSchemaDowngrade
	case JSONSchemaDowngrade, JSONSchemaError:
		return value
	}
	slog.Warn("invalid "+jsonSchemaModeEnvVar+"; using downgrade", "value", value)
	return JSONSchemaDowngrade
}

// IsModel reports whether a model ID names a DeepSeek model, tolerating a
// vendor or provider prefix such as "deepseek/" or "opencode_go/".
func IsModel(model string) bool {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "deepseek")
}

// AdaptCompatibility applies DeepSeek's request requirements that do not
// depend on the serving provider's reasoning-effort dialect. Providers that
// route to DeepSeek models (deepseek, opencode_go) call it from their chat
// request hook, which runs after virtual-model routing has resolved the target.
func AdaptCompatibility(req *core.ChatRequest, providerName string, mode JSONSchemaMode) (*core.ChatRequest, error) {
	if req == nil {
		return req, nil
	}
	adapted, err := padMissingReasoningContent(req)
	if err != nil {
		return nil, err
	}
	return adaptJSONSchemaResponseFormat(adapted, providerName, mode)
}

// padMissingReasoningContent satisfies DeepSeek's thinking-mode rule that a
// request carrying tools replays reasoning_content on every earlier assistant
// turn, including turns without tool calls. Clients may not know DeepSeek
// serves the request, so a non-empty neutral value is added when missing.
func padMissingReasoningContent(req *core.ChatRequest) (*core.ChatRequest, error) {
	if len(req.Tools) == 0 {
		return req, nil
	}

	var adapted *core.ChatRequest
	for i, message := range req.Messages {
		if message.Role != "assistant" || message.ExtraFields.Lookup("reasoning_content") != nil {
			continue
		}

		extra, err := core.MergeUnknownJSONFields(message.ExtraFields, map[string]json.RawMessage{
			"reasoning_content": json.RawMessage(`" "`),
		})
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to adapt DeepSeek assistant message: "+err.Error(), err)
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

// adaptJSONSchemaResponseFormat handles response_format type json_schema,
// which DeepSeek rejects ("This response_format type is unavailable now").
// Other formats, and malformed ones, are left for the upstream to judge.
func adaptJSONSchemaResponseFormat(req *core.ChatRequest, providerName string, mode JSONSchemaMode) (*core.ChatRequest, error) {
	raw := req.ExtraFields.Lookup("response_format")
	if raw == nil {
		return req, nil
	}
	var format struct {
		Type       string `json:"type"`
		JSONSchema *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if err := json.Unmarshal(raw, &format); err != nil || !strings.EqualFold(strings.TrimSpace(format.Type), "json_schema") {
		return req, nil
	}

	if mode == JSONSchemaError {
		return nil, core.NewInvalidRequestError(fmt.Sprintf(
			"response_format type json_schema is not supported by %s model %q; use json_object", providerName, req.Model), nil)
	}

	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		"response_format": json.RawMessage(`{"type":"json_object"}`),
	})
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to adapt DeepSeek response_format: "+err.Error(), err)
	}

	var name, description string
	var schema json.RawMessage
	if format.JSONSchema != nil {
		name, description, schema = format.JSONSchema.Name, format.JSONSchema.Description, format.JSONSchema.Schema
	}
	adapted := *req
	adapted.ExtraFields = extra
	adapted.Messages = withSystemInstruction(req.Messages, jsonSchemaInstruction(name, description, schema))
	slog.Debug("downgraded response_format json_schema to json_object", "provider", providerName, "model", req.Model)
	return &adapted, nil
}

// jsonSchemaInstruction describes the requested schema. DeepSeek's JSON mode
// needs the prompt to ask for JSON and show the expected shape.
func jsonSchemaInstruction(name, description string, schema json.RawMessage) string {
	var b strings.Builder
	b.WriteString("Respond only with a valid JSON object")
	if core.IsJSONNull(bytes.TrimSpace(schema)) {
		b.WriteString(".")
		return b.String()
	}
	b.WriteString(" that conforms to the JSON schema below")
	if name != "" {
		fmt.Fprintf(&b, " (%s)", name)
	}
	b.WriteString(".")
	if description != "" {
		b.WriteString("\nSchema description: ")
		b.WriteString(description)
	}
	b.WriteString("\nJSON schema:\n")
	var compact bytes.Buffer
	if err := json.Compact(&compact, schema); err == nil {
		b.Write(compact.Bytes())
	} else {
		b.Write(bytes.TrimSpace(schema))
	}
	return b.String()
}

// withSystemInstruction returns a copy of messages with a system message
// inserted after any leading system or developer messages.
func withSystemInstruction(messages []core.Message, instruction string) []core.Message {
	i := 0
	for i < len(messages) && (messages[i].Role == "system" || messages[i].Role == "developer") {
		i++
	}
	out := make([]core.Message, 0, len(messages)+1)
	out = append(out, messages[:i]...)
	out = append(out, core.Message{Role: "system", Content: instruction})
	return append(out, messages[i:]...)
}
