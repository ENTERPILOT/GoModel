package geminiapi

import (
	"errors"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func translate(t *testing.T, body string, stream bool) (*core.ChatRequest, error) {
	t.Helper()
	req, err := DecodeGenerateContentRequest([]byte(body))
	require.NoError(t, err)
	return ToChatRequest(req, "gemini-test", stream)
}

func mustTranslate(t *testing.T, body string) *core.ChatRequest {
	t.Helper()
	chat, err := translate(t, body, false)
	require.NoError(t, err)
	return chat
}

func extraField[T any](t *testing.T, fields core.UnknownJSONFields, key string) T {
	t.Helper()
	var value T
	raw := fields.Lookup(key)
	require.NotEmpty(t, raw, "extra field %q is missing", key)
	require.NoError(t, json.Unmarshal(raw, &value))
	return value
}

func TestDecodeGenerateContentRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty body", body: "  "},
		{name: "not an object", body: `[]`},
		{name: "trailing value", body: `{"contents":[]} {}`},
		{name: "wrong member type", body: `{"generationConfig":{"temperature":"hot"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeGenerateContentRequest([]byte(tt.body))
			require.Error(t, err)
		})
	}
}

func TestToChatRequest_TextSystemAndGenerationConfig(t *testing.T) {
	// The Gemini API takes every member in camelCase and snake_case, and the
	// SDKs mix both in one body.
	bodies := map[string]string{
		"camelCase": `{
			"systemInstruction":{"role":"user","parts":[{"text":"Be brief."},{"text":"Be kind."}]},
			"contents":[{"role":"user","parts":[{"text":"Hello"}]},{"role":"model","parts":[{"text":"Hi"}]},{"parts":[{"text":"Bye"}]}],
			"generationConfig":{"temperature":0.2,"topP":0.9,"topK":40,"maxOutputTokens":128,"stopSequences":["END"],"seed":7,"candidateCount":1,"presencePenalty":0.5,"frequencyPenalty":-1}
		}`,
		"snake_case": `{
			"system_instruction":{"parts":[{"text":"Be brief."},{"text":"Be kind."}]},
			"contents":[{"role":"user","parts":[{"text":"Hello"}]},{"role":"model","parts":[{"text":"Hi"}]},{"parts":[{"text":"Bye"}]}],
			"generation_config":{"temperature":0.2,"top_p":0.9,"top_k":40,"max_output_tokens":128,"stop_sequences":["END"],"seed":7,"candidate_count":1,"presence_penalty":0.5,"frequency_penalty":-1}
		}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			chat := mustTranslate(t, body)

			assert.Equal(t, "gemini-test", chat.Model)
			assert.False(t, chat.Stream)
			assert.Nil(t, chat.StreamOptions)
			require.Len(t, chat.Messages, 4)
			assert.Equal(t, core.Message{Role: "system", Content: "Be brief.\nBe kind."}, chat.Messages[0])
			assert.Equal(t, core.Message{Role: "user", Content: "Hello"}, chat.Messages[1])
			assert.Equal(t, core.Message{Role: "assistant", Content: "Hi"}, chat.Messages[2])
			assert.Equal(t, core.Message{Role: "user", Content: "Bye"}, chat.Messages[3])

			require.NotNil(t, chat.Temperature)
			assert.InDelta(t, 0.2, *chat.Temperature, 1e-9)
			require.NotNil(t, chat.TopP)
			assert.InDelta(t, 0.9, *chat.TopP, 1e-9)
			require.NotNil(t, chat.MaxTokens)
			assert.Equal(t, 128, *chat.MaxTokens)
			assert.Equal(t, []string{"END"}, extraField[[]string](t, chat.ExtraFields, "stop"))
			assert.Equal(t, 7, extraField[int](t, chat.ExtraFields, "seed"))
			assert.InDelta(t, 0.5, extraField[float64](t, chat.ExtraFields, "presence_penalty"), 1e-9)
			assert.InDelta(t, -1, extraField[float64](t, chat.ExtraFields, "frequency_penalty"), 1e-9)
			// topK has no portable equivalent and must not reach providers
			// that reject unknown fields.
			assert.Empty(t, chat.ExtraFields.Lookup("top_k"))
			assert.Empty(t, chat.ExtraFields.Lookup("topK"))
		})
	}
}

func TestToChatRequest_StreamAsksForUsage(t *testing.T) {
	chat, err := translate(t, `{"contents":[{"parts":[{"text":"Hi"}]}]}`, true)
	require.NoError(t, err)
	assert.True(t, chat.Stream)
	require.NotNil(t, chat.StreamOptions)
	assert.True(t, chat.StreamOptions.IncludeUsage)
}

func TestToChatRequest_InlineMedia(t *testing.T) {
	chat := mustTranslate(t, `{"contents":[{"role":"user","parts":[
		{"text":"Describe both."},
		{"inlineData":{"mimeType":"image/png","data":"aW1n"}},
		{"inline_data":{"mime_type":"application/pdf","data":"cGRm"}}
	]}]}`)

	require.Len(t, chat.Messages, 1)
	parts, ok := chat.Messages[0].Content.([]core.ContentPart)
	require.True(t, ok, "content = %#v", chat.Messages[0].Content)
	require.Len(t, parts, 3)
	assert.Equal(t, "text", parts[0].Type)
	require.NotNil(t, parts[1].ImageURL)
	assert.Equal(t, "data:image/png;base64,aW1n", parts[1].ImageURL.URL)
	require.NotNil(t, parts[2].File)
	assert.Equal(t, "data:application/pdf;base64,cGRm", parts[2].File.FileData)
	assert.Equal(t, "document.pdf", parts[2].File.Filename)
}

func TestToChatRequest_FunctionDeclarations(t *testing.T) {
	chat := mustTranslate(t, `{
		"contents":[{"parts":[{"text":"Weather?"}]}],
		"tools":[
			{"functionDeclarations":[{
				"name":"get_weather","description":"Get the weather",
				"parameters":{"type":"OBJECT","property_ordering":["city","unit"],"required":["city"],"properties":{
					"city":{"type":"STRING"},
					"unit":{"type":"STRING","nullable":true,"enum":["c","f"]},
					"days":{"type":"ARRAY","items":{"type":"INTEGER"},"max_items":7},
					"where":{"any_of":[{"type":"STRING"},{"type":"NUMBER"}]}
				}}
			}]},
			{"function_declarations":[
				{"name":"now","parameters_json_schema":{"type":"object","properties":{"tz":{"type":"string"}}}},
				{"name":"ping"}
			]}
		]
	}`)

	require.Len(t, chat.Tools, 3)
	weather := chat.Tools[0]["function"].(map[string]any)
	assert.Equal(t, "function", chat.Tools[0]["type"])
	assert.Equal(t, "get_weather", weather["name"])
	assert.Equal(t, "Get the weather", weather["description"])
	// Gemini's Schema becomes JSON Schema: lower-case types, a "null" type
	// (and enum value) for nullable, camelCase keywords, and no
	// propertyOrdering.
	assert.Equal(t, map[string]any{
		"type":     "object",
		"required": []any{"city"},
		"properties": map[string]any{
			"city":  map[string]any{"type": "string"},
			"unit":  map[string]any{"type": []any{"string", "null"}, "enum": []any{"c", "f", nil}},
			"days":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "maxItems": float64(7)},
			"where": map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "number"}}},
		},
	}, weather["parameters"])

	now := chat.Tools[1]["function"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "object", "properties": map[string]any{"tz": map[string]any{"type": "string"}}}, now["parameters"])

	ping := chat.Tools[2]["function"].(map[string]any)
	assert.NotContains(t, ping, "parameters")
}

// A nullable schema without a type of its own is nullable through anyOf.
func TestJSONSchemaFromGemini_NullableAnyOf(t *testing.T) {
	schema, err := jsonSchemaFromGemini([]byte(`{"nullable":true,"anyOf":[{"type":"STRING"},{"type":"INTEGER"}]}`))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"anyOf": []any{
		map[string]any{"type": "string"}, map[string]any{"type": "integer"}, map[string]any{"type": "null"},
	}}, schema)

	_, err = jsonSchemaFromGemini([]byte(`[]`))
	require.Error(t, err)
}

func TestToChatRequest_ToolConfig(t *testing.T) {
	function := func(name string) map[string]any {
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}
	}
	tests := []struct {
		name   string
		config string
		want   any
	}{
		{name: "absent", config: `null`, want: nil},
		{name: "unspecified mode", config: `{"functionCallingConfig":{}}`, want: nil},
		{name: "explicitly unspecified mode", config: `{"functionCallingConfig":{"mode":"MODE_UNSPECIFIED"}}`, want: nil},
		{name: "auto", config: `{"functionCallingConfig":{"mode":"AUTO"}}`, want: "auto"},
		{name: "none", config: `{"function_calling_config":{"mode":"none"}}`, want: "none"},
		{name: "any", config: `{"functionCallingConfig":{"mode":"ANY"}}`, want: "required"},
		{name: "any with one function", config: `{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["a"]}}`, want: function("a")},
		{
			name:   "any with several functions",
			config: `{"functionCallingConfig":{"mode":"ANY","allowed_function_names":["a","b"]}}`,
			want: map[string]any{"type": "allowed_tools", "allowed_tools": map[string]any{
				"mode": "required", "tools": []any{function("a"), function("b")},
			}},
		},
		// The schema guarantee of VALIDATED has no portable equivalent; the
		// model may still call a function or answer in text, as with AUTO.
		{name: "validated", config: `{"functionCallingConfig":{"mode":"VALIDATED"}}`, want: "auto"},
		{
			name:   "validated with functions",
			config: `{"functionCallingConfig":{"mode":"VALIDATED","allowedFunctionNames":["a"]}}`,
			want: map[string]any{"type": "allowed_tools", "allowed_tools": map[string]any{
				"mode": "auto", "tools": []any{function("a")},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat := mustTranslate(t, `{"contents":[{"parts":[{"text":"Hi"}]}],"tools":[{"functionDeclarations":[{"name":"a"},{"name":"b"}]}],"toolConfig":`+tt.config+`}`)
			assert.Equal(t, tt.want, chat.ToolChoice)
		})
	}

	t.Run("ignored without tools", func(t *testing.T) {
		chat := mustTranslate(t, `{"contents":[{"parts":[{"text":"Hi"}]}],"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`)
		assert.Nil(t, chat.ToolChoice)
	})
}

func TestToChatRequest_FunctionCallRoundTrip(t *testing.T) {
	// Gemini 2.5 function calls carry no ID: a response answers the call of
	// the same name, in order. Chat needs IDs, so they are generated.
	chat := mustTranslate(t, `{"contents":[
		{"role":"user","parts":[{"text":"Weather in Paris and Rome?"}]},
		{"role":"model","parts":[
			{"text":"Checking."},
			{"functionCall":{"name":"get_weather","args":{"city":"Paris"}},"thoughtSignature":"sig-1"},
			{"function_call":{"name":"get_weather","args":{"city":"Rome"}}}
		]},
		{"role":"user","parts":[
			{"functionResponse":{"name":"get_weather","response":{"temp":20}}},
			{"function_response":{"name":"get_weather","response":{"temp":25}}},
			{"text":"Summarize."}
		]},
		{"role":"model","parts":[{"functionCall":{"id":"fc_9","name":"lookup"}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"fc_9","name":"lookup","response":{"ok":true}}}]}
	]}`)

	require.Len(t, chat.Messages, 7)

	assistant := chat.Messages[1]
	assert.Equal(t, "assistant", assistant.Role)
	assert.Equal(t, "Checking.", assistant.Content)
	require.Len(t, assistant.ToolCalls, 2)
	assert.Equal(t, "call_0", assistant.ToolCalls[0].ID)
	assert.Equal(t, "function", assistant.ToolCalls[0].Type)
	assert.Equal(t, "get_weather", assistant.ToolCalls[0].Function.Name)
	assert.JSONEq(t, `{"city":"Paris"}`, assistant.ToolCalls[0].Function.Arguments)
	assert.JSONEq(t, `{"thought_signature":"sig-1"}`, string(assistant.ToolCalls[0].ExtraFields.ExtraContent(core.ExtraContentVendorGoogle)))
	assert.Equal(t, "call_1", assistant.ToolCalls[1].ID)
	assert.True(t, assistant.ToolCalls[1].ExtraFields.IsEmpty())

	// Tool results come first, in call order, then the follow-up text.
	assert.Equal(t, core.Message{Role: "tool", ToolCallID: "call_0", Content: `{"temp":20}`}, chat.Messages[2])
	assert.Equal(t, core.Message{Role: "tool", ToolCallID: "call_1", Content: `{"temp":25}`}, chat.Messages[3])
	assert.Equal(t, core.Message{Role: "user", Content: "Summarize."}, chat.Messages[4])

	// A client-supplied ID is kept, and a tool-call-only turn has no content.
	second := chat.Messages[5]
	assert.True(t, second.ContentNull)
	require.Len(t, second.ToolCalls, 1)
	assert.Equal(t, "fc_9", second.ToolCalls[0].ID)
	assert.Equal(t, "{}", second.ToolCalls[0].Function.Arguments)
	assert.Equal(t, core.Message{Role: "tool", ToolCallID: "fc_9", Content: `{"ok":true}`}, chat.Messages[6])
}

// A call without an ID must not be given one the client used itself: two tool
// calls sharing an ID would send both results under one function's name.
func TestToChatRequest_GeneratedCallIDsAvoidSuppliedOnes(t *testing.T) {
	chat := mustTranslate(t, `{"contents":[
		{"role":"user","parts":[{"text":"Go"}]},
		{"role":"model","parts":[
			{"functionCall":{"name":"first"}},
			{"functionCall":{"id":"call_0","name":"second"}},
			{"functionCall":{"name":"third"}}
		]},
		{"role":"user","parts":[
			{"functionResponse":{"name":"third","response":{"n":3}}},
			{"functionResponse":{"id":"call_0","name":"second","response":{"n":2}}},
			{"functionResponse":{"name":"first","response":{"n":1}}}
		]},
		{"role":"model","parts":[{"functionCall":{"id":"call_2","name":"fourth"}}]},
		{"role":"user","parts":[{"functionResponse":{"name":"fourth","response":{"n":4}}}]}
	]}`)

	require.Len(t, chat.Messages, 7)
	calls := chat.Messages[1].ToolCalls
	require.Len(t, calls, 3)
	// call_0 and call_2 are the client's, so the generated IDs skip them.
	assert.Equal(t, []string{"call_1", "call_0", "call_3"}, []string{calls[0].ID, calls[1].ID, calls[2].ID})

	assert.Equal(t, core.Message{Role: "tool", ToolCallID: "call_3", Content: `{"n":3}`}, chat.Messages[2])
	assert.Equal(t, core.Message{Role: "tool", ToolCallID: "call_0", Content: `{"n":2}`}, chat.Messages[3])
	assert.Equal(t, core.Message{Role: "tool", ToolCallID: "call_1", Content: `{"n":1}`}, chat.Messages[4])
	// A response without an ID still answers a call that carries one, by name.
	assert.Equal(t, core.Message{Role: "tool", ToolCallID: "call_2", Content: `{"n":4}`}, chat.Messages[6])
}

// A client that drops the ID from the call it echoes but keeps it on the
// response still gets its result matched, by name.
func TestToChatRequest_ResponseIDWithoutMatchingCallFallsBackToName(t *testing.T) {
	chat := mustTranslate(t, `{"contents":[
		{"role":"user","parts":[{"text":"Go"}]},
		{"role":"model","parts":[{"functionCall":{"name":"lookup"}}]},
		{"role":"user","parts":[{"functionResponse":{"id":"upstream_7","name":"lookup","response":{"ok":true}}}]}
	]}`)

	require.Len(t, chat.Messages, 3)
	assert.Equal(t, "call_0", chat.Messages[1].ToolCalls[0].ID)
	assert.Equal(t, core.Message{Role: "tool", ToolCallID: "call_0", Content: `{"ok":true}`}, chat.Messages[2])
}

func TestToChatRequest_ModelTurnThoughts(t *testing.T) {
	chat := mustTranslate(t, `{"contents":[
		{"role":"user","parts":[{"text":"Hi"}]},
		{"role":"model","parts":[{"text":"pondering","thought":true},{"text":"Hello","thought_signature":"sig-text"}]},
		{"role":"user","parts":[{"text":"More"}]}
	]}`)

	require.Len(t, chat.Messages, 3)
	assistant := chat.Messages[1]
	// Reasoning is not replayed; the text turn's signature rides on the message.
	assert.Equal(t, "Hello", assistant.Content)
	assert.JSONEq(t, `{"thought_signature":"sig-text"}`, string(assistant.ExtraFields.ExtraContent(core.ExtraContentVendorGoogle)))
}

func TestToChatRequest_ResponseFormat(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{name: "none", config: `{}`},
		{name: "plain text", config: `{"responseMimeType":"text/plain"}`},
		{name: "json object", config: `{"response_mime_type":"application/json"}`, want: `{"type":"json_object"}`},
		{
			name:   "gemini schema",
			config: `{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","property_ordering":["a"],"properties":{"a":{"type":"STRING"}}}}`,
			want:   `{"type":"json_schema","json_schema":{"name":"response","schema":{"type":"object","properties":{"a":{"type":"string"}}}}}`,
		},
		{
			name:   "json schema",
			config: `{"responseMimeType":"application/json","response_json_schema":{"type":"object","additionalProperties":false}}`,
			want:   `{"type":"json_schema","json_schema":{"name":"response","schema":{"type":"object","additionalProperties":false}}}`,
		},
		{
			name:   "schema without a mime type",
			config: `{"responseJsonSchema":{"type":"array"}}`,
			want:   `{"type":"json_schema","json_schema":{"name":"response","schema":{"type":"array"}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chat := mustTranslate(t, `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":`+tt.config+`}`)
			raw := chat.ExtraFields.Lookup("response_format")
			if tt.want == "" {
				assert.Empty(t, raw)
				return
			}
			assert.JSONEq(t, tt.want, string(raw))
		})
	}
}

func TestToChatRequest_ThinkingConfig(t *testing.T) {
	tests := []struct {
		name            string
		config          string
		wantEffort      string
		includeThoughts bool
	}{
		{name: "absent", config: `{}`},
		{name: "empty", config: `{"thinkingConfig":{}}`},
		{name: "dynamic budget", config: `{"thinkingConfig":{"thinkingBudget":-1}}`},
		{name: "budget off", config: `{"thinkingConfig":{"thinkingBudget":0}}`, wantEffort: "none"},
		{name: "low budget", config: `{"thinkingConfig":{"thinking_budget":1024}}`, wantEffort: "low"},
		{name: "medium budget", config: `{"thinkingConfig":{"thinkingBudget":8192}}`, wantEffort: "medium"},
		{name: "high budget", config: `{"thinking_config":{"thinkingBudget":24576}}`, wantEffort: "high"},
		{name: "level", config: `{"thinkingConfig":{"thinkingLevel":"HIGH"}}`, wantEffort: "high"},
		{name: "level wins over budget", config: `{"thinkingConfig":{"thinking_level":"minimal","thinkingBudget":24576}}`, wantEffort: "minimal"},
		{name: "include thoughts", config: `{"thinkingConfig":{"include_thoughts":true}}`, includeThoughts: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":` + tt.config + `}`
			req, err := DecodeGenerateContentRequest([]byte(body))
			require.NoError(t, err)
			chat, err := ToChatRequest(req, "gemini-test", false)
			require.NoError(t, err)

			assert.Equal(t, tt.includeThoughts, IncludeThoughts(req))
			if tt.wantEffort == "" {
				assert.Nil(t, chat.Reasoning)
				return
			}
			require.NotNil(t, chat.Reasoning)
			assert.Equal(t, tt.wantEffort, chat.Reasoning.Effort)
		})
	}
}

func TestToChatRequest_AcceptsAndIgnoresSafetySettings(t *testing.T) {
	chat := mustTranslate(t, `{"contents":[{"parts":[{"text":"Hi"}]}],"safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_NONE"}]}`)
	assert.True(t, chat.ExtraFields.IsEmpty())
}

func TestToChatRequest_Rejects(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		message string
		param   string
	}{
		{name: "no contents", body: `{}`, message: "contents must not be empty", param: "contents"},
		{
			name:    "cached content",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"cachedContent":"cachedContents/abc"}`,
			message: "cachedContent is not supported on this endpoint", param: "cachedContent",
		},
		{
			name:    "built-in tool",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"tools":[{"googleSearch":{}}]}`,
			message: "tools[0].googleSearch: built-in tools are not supported on this endpoint; only functionDeclarations translate", param: "tools",
		},
		{
			name:    "built-in tool next to functions",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"tools":[{"functionDeclarations":[{"name":"a"}],"code_execution":{}}]}`,
			message: "tools[0].code_execution: built-in tools are not supported on this endpoint; only functionDeclarations translate", param: "tools",
		},
		{
			name:    "unnamed function",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"tools":[{"functionDeclarations":[{"description":"x"}]}]}`,
			message: "tools[0].functionDeclarations[0].name is required", param: "tools",
		},
		{
			name:    "file data",
			body:    `{"contents":[{"parts":[{"fileData":{"fileUri":"https://example.com/a.mp4"}}]}]}`,
			message: "fileData is not supported on this endpoint", param: "contents",
		},
		{
			name:    "code execution history",
			body:    `{"contents":[{"role":"model","parts":[{"executable_code":{"language":"PYTHON","code":"1"}}]}]}`,
			message: "executableCode is not supported on this endpoint", param: "contents",
		},
		{
			name:    "inline data without a type",
			body:    `{"contents":[{"parts":[{"inlineData":{"data":"aW1n"}}]}]}`,
			message: "contents[0]: inlineData requires mimeType and data", param: "contents",
		},
		{
			name:    "unknown role",
			body:    `{"contents":[{"role":"system","parts":[{"text":"Hi"}]}]}`,
			message: `contents[0]: role must be "user" or "model"`, param: "contents",
		},
		{
			name:    "function response without a call",
			body:    `{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"get_weather","response":{}}}]}]}`,
			message: `contents[0]: functionResponse "get_weather" does not answer a functionCall of the preceding model turn`, param: "contents",
		},
		{
			name:    "several candidates",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":{"candidateCount":2}}`,
			message: "generationConfig.candidateCount above 1 is not supported on this endpoint", param: "generationConfig.candidateCount",
		},
		{
			name:    "image output",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":{"responseModalities":["TEXT","IMAGE"]}}`,
			message: "generationConfig.responseModalities other than TEXT is not supported on this endpoint", param: "generationConfig.responseModalities",
		},
		{
			name:    "enum output",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":{"responseMimeType":"text/x.enum"}}`,
			message: `generationConfig.responseMimeType "text/x.enum" is not supported on this endpoint`, param: "generationConfig.responseMimeType",
		},
		{
			name:    "function response in a model turn",
			body:    `{"contents":[{"role":"model","parts":[{"functionResponse":{"name":"a","response":{}}}]}]}`,
			message: "contents[0]: functionResponse belongs in a user turn", param: "contents",
		},
		{
			name:    "function call in a user turn",
			body:    `{"contents":[{"role":"user","parts":[{"functionCall":{"name":"a"}}]}]}`,
			message: "contents[0]: functionCall belongs in a model turn", param: "contents",
		},
		{
			name:    "unknown function calling mode",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"toolConfig":{"functionCallingConfig":{"mode":"sometimes"}}}`,
			message: `toolConfig.functionCallingConfig.mode "SOMETIMES" is not one of AUTO, ANY, NONE, VALIDATED`, param: "toolConfig.functionCallingConfig.mode",
		},
		{
			name:    "both parameter schemas",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"tools":[{"functionDeclarations":[{"name":"a","parameters":{"type":"OBJECT"},"parametersJsonSchema":{"type":"object"}}]}]}`,
			message: "tools[0].functionDeclarations[0]: parameters and parametersJsonSchema are mutually exclusive", param: "tools",
		},
		{
			name:    "both response schemas",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"generationConfig":{"responseSchema":{"type":"OBJECT"},"responseJsonSchema":{"type":"object"}}}`,
			message: "generationConfig.responseSchema and responseJsonSchema are mutually exclusive", param: "generationConfig.responseJsonSchema",
		},
		{
			name:    "system instruction function call",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"systemInstruction":{"parts":[{"text":"x"},{"functionCall":{"name":"a"}}]}}`,
			message: "systemInstruction supports text parts only", param: "systemInstruction",
		},
		{
			name:    "system instruction media",
			body:    `{"contents":[{"parts":[{"text":"Hi"}]}],"systemInstruction":{"parts":[{"inlineData":{"mimeType":"image/png","data":"aW1n"}}]}}`,
			message: "systemInstruction supports text parts only", param: "systemInstruction",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := translate(t, tt.body, false)
			require.Error(t, err)
			gatewayErr, ok := errors.AsType[*core.GatewayError](err)
			require.True(t, ok, "err = %T", err)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
			assert.Equal(t, tt.message, gatewayErr.Message)
			require.NotNil(t, gatewayErr.Param)
			assert.Equal(t, tt.param, *gatewayErr.Param)
		})
	}
}

func TestToChatRequest_RequiresModel(t *testing.T) {
	req, err := DecodeGenerateContentRequest([]byte(`{"contents":[{"parts":[{"text":"Hi"}]}]}`))
	require.NoError(t, err)
	_, err = ToChatRequest(req, " ", false)
	gatewayErr, ok := errors.AsType[*core.GatewayError](err)
	require.True(t, ok, "err = %T", err)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
	assert.Equal(t, "model is required", gatewayErr.Message)
	require.NotNil(t, gatewayErr.Param)
	assert.Equal(t, "model", *gatewayErr.Param)
}
