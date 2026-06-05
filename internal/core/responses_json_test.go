package core

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestResponsesRequestUnmarshalJSON_StringInput(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":"hello"}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if req.Model != "gpt-4o-mini" {
		t.Fatalf("Model = %q, want gpt-4o-mini", req.Model)
	}
	input, ok := req.Input.(string)
	if !ok || input != "hello" {
		t.Fatalf("Input = %#v, want string hello", req.Input)
	}
}

func TestResponsesRequestUnmarshalJSON_ArrayInput(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	input, ok := req.Input.([]ResponsesInputElement)
	if !ok || len(input) != 1 {
		t.Fatalf("Input = %#v, want []ResponsesInputElement len=1", req.Input)
	}
	if input[0].Role != "user" {
		t.Fatalf("Input[0].Role = %q, want user", input[0].Role)
	}
}

func TestResponsesRequestUnmarshalJSON_ArrayInputFunctionCall(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":[
		{"type":"function_call","call_id":"call_123","name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"},
		{"type":"function_call_output","call_id":"call_123","output":{"temperature_c":21}}
	]}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	input, ok := req.Input.([]ResponsesInputElement)
	if !ok || len(input) != 2 {
		t.Fatalf("Input = %#v, want []ResponsesInputElement len=2", req.Input)
	}
	if input[0].Type != "function_call" || input[0].CallID != "call_123" || input[0].Name != "lookup_weather" {
		t.Fatalf("Input[0] = %+v, want function_call with call_id=call_123 name=lookup_weather", input[0])
	}
	if input[0].Arguments != `{"city":"Warsaw"}` {
		t.Fatalf("Input[0].Arguments = %q, want JSON string", input[0].Arguments)
	}
	if input[1].Type != "function_call_output" || input[1].CallID != "call_123" {
		t.Fatalf("Input[1] = %+v, want function_call_output with call_id=call_123", input[1])
	}
	if input[1].Output != `{"temperature_c":21}` {
		t.Fatalf("Input[1].Output = %q, want stringified JSON object", input[1].Output)
	}
}

func TestResponsesRequestUnmarshalJSON_FunctionCallAcceptsIDField(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":[
		{"type":"function_call","id":"call_456","name":"get_time","arguments":"{}"}
	]}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	input := req.Input.([]ResponsesInputElement)
	if input[0].CallID != "call_456" {
		t.Fatalf("Input[0].CallID = %q, want call_456 (from id field)", input[0].CallID)
	}
}

func TestResponsesRequestUnmarshalJSON_PreservesToolCallingControls(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model":"gpt-4o-mini",
		"input":"hello",
		"tool_choice":{"type":"function","function":{"name":"lookup_weather"}},
		"parallel_tool_calls":false
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	toolChoice, ok := req.ToolChoice.(map[string]any)
	if !ok {
		t.Fatalf("ToolChoice = %#v, want object", req.ToolChoice)
	}
	if typ, _ := toolChoice["type"].(string); typ != "function" {
		t.Fatalf("ToolChoice.type = %#v, want function", toolChoice["type"])
	}
	if req.ParallelToolCalls == nil || *req.ParallelToolCalls {
		t.Fatalf("ParallelToolCalls = %#v, want false", req.ParallelToolCalls)
	}
}

func TestResponsesConversationRefMarshalJSON_UsesUpdatedID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		raw  string
		want string
	}{
		{
			name: "string shape",
			id:   "conv_new",
			raw:  `"conv_old"`,
			want: `"conv_new"`,
		},
		{
			name: "object shape",
			id:   "conv_new",
			raw:  `{"id":"conv_old","metadata":{"team":"alpha"}}`,
			want: `{"id":"conv_new","metadata":{"team":"alpha"}}`,
		},
		{
			name: "clear string shape",
			raw:  `"conv_old"`,
			want: `null`,
		},
		{
			name: "clear object shape",
			raw:  `{"id":"conv_old","metadata":{"team":"alpha"}}`,
			want: `null`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ref ResponsesConversationRef
			if err := json.Unmarshal([]byte(tt.raw), &ref); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			ref.ID = tt.id

			body, err := json.Marshal(ref)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if !jsonEqual(body, []byte(tt.want)) {
				t.Fatalf("body = %s, want JSON equivalent to %s", body, tt.want)
			}
		})
	}
}

func TestResponsesConversationRefMarshalJSON_InvalidRaw(t *testing.T) {
	ref := ResponsesConversationRef{
		ID:  "conv_new",
		Raw: json.RawMessage(`{"id":`),
	}

	if _, err := json.Marshal(ref); err == nil {
		t.Fatal("json.Marshal() error = nil, want invalid raw conversation error")
	}
}

func jsonEqual(a, b []byte) bool {
	var av any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	var bv any
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return jsonValueEqual(av, bv)
}

func jsonValueEqual(a, b any) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

func TestResponsesRequestMarshalJSON_PreservesInput(t *testing.T) {
	body, err := json.Marshal(ResponsesRequest{
		Model: "gpt-4o-mini",
		Input: []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "hello",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	inputRaw, ok := decoded["input"]
	if !ok {
		t.Fatalf("marshal output missing input: %s", string(body))
	}

	input, ok := inputRaw.([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("decoded input = %#v, want []any len=1", inputRaw)
	}

	firstMsg, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("first input item = %#v, want object", input[0])
	}
	if role, _ := firstMsg["role"].(string); role != "user" {
		t.Fatalf("first input role = %#v, want user", firstMsg["role"])
	}

	contentRaw, ok := firstMsg["content"]
	if !ok {
		t.Fatalf("first input missing content: %#v", firstMsg)
	}
	content, ok := contentRaw.([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("first input content = %#v, want []any len=1", contentRaw)
	}

	firstPart, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("first content part = %#v, want object", content[0])
	}
	if typ, _ := firstPart["type"].(string); typ != "input_text" {
		t.Fatalf("first content type = %#v, want input_text", firstPart["type"])
	}
	if text, _ := firstPart["text"].(string); text != "hello" {
		t.Fatalf("first content text = %#v, want hello", firstPart["text"])
	}
}

func TestResponseUtilityRequestMarshalJSON_PreservesProvider(t *testing.T) {
	tests := []struct {
		name string
		req  any
	}{
		{
			name: "input tokens",
			req: ResponseInputTokensRequest{
				Model:    "gpt-4o-mini",
				Provider: "openai_primary",
				Input:    "hello",
			},
		},
		{
			name: "compact",
			req: ResponseCompactRequest{
				Model:    "gpt-4o-mini",
				Provider: "openai_primary",
				Input:    "hello",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if decoded["provider"] != "openai_primary" {
				t.Fatalf("provider = %#v, want openai_primary in %s", decoded["provider"], string(body))
			}

			switch original := tt.req.(type) {
			case ResponseInputTokensRequest:
				var roundTripped ResponseInputTokensRequest
				if err := json.Unmarshal(body, &roundTripped); err != nil {
					t.Fatalf("json.Unmarshal(ResponseInputTokensRequest) error = %v", err)
				}
				if roundTripped.Provider != original.Provider {
					t.Fatalf("round-tripped provider = %q, want %q", roundTripped.Provider, original.Provider)
				}
				if input, ok := roundTripped.Input.(string); !ok || input != original.Input {
					t.Fatalf("round-tripped input = %#v, want %#v", roundTripped.Input, original.Input)
				}
			case ResponseCompactRequest:
				var roundTripped ResponseCompactRequest
				if err := json.Unmarshal(body, &roundTripped); err != nil {
					t.Fatalf("json.Unmarshal(ResponseCompactRequest) error = %v", err)
				}
				if roundTripped.Provider != original.Provider {
					t.Fatalf("round-tripped provider = %q, want %q", roundTripped.Provider, original.Provider)
				}
				if input, ok := roundTripped.Input.(string); !ok || input != original.Input {
					t.Fatalf("round-tripped input = %#v, want %#v", roundTripped.Input, original.Input)
				}
			default:
				t.Fatalf("unexpected request type %T", tt.req)
			}
		})
	}
}

func TestResponseUtilityRequestJSON_PreservesResponsesContextFields(t *testing.T) {
	store := false
	parallelToolCalls := true
	temperature := 0.2
	topP := 0.8
	topLogprobs := 3
	maxOutputTokens := 256
	utilityRequests := []struct {
		name string
		req  any
	}{
		{
			name: "input tokens",
			req: ResponseInputTokensRequest{
				Model:                "gpt-5-mini",
				Input:                "hello",
				Instructions:         "be brief",
				Tools:                []map[string]any{{"type": "function", "name": "lookup"}},
				ToolChoice:           "auto",
				ParallelToolCalls:    &parallelToolCalls,
				Temperature:          &temperature,
				TopP:                 &topP,
				TopLogprobs:          &topLogprobs,
				MaxOutputTokens:      &maxOutputTokens,
				Metadata:             map[string]string{"team": "alpha"},
				Reasoning:            &Reasoning{Effort: "low"},
				Text:                 map[string]any{"format": map[string]any{"type": "text"}},
				Include:              []string{"reasoning.encrypted_content"},
				Truncation:           "auto",
				Store:                &store,
				PreviousResponseID:   "resp_previous",
				Conversation:         &ResponsesConversationRef{ID: "conv_123"},
				Prompt:               map[string]any{"id": "pmpt_123"},
				PromptCacheRetention: "24h",
				ContextManagement:    map[string]any{"truncation": "auto"},
				User:                 "tenant-123",
				ServiceTier:          "flex",
				SafetyIdentifier:     "safe_123",
				ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"future_field": json.RawMessage(`{"enabled":true}`),
				}),
			},
		},
		{
			name: "compact",
			req: ResponseCompactRequest{
				Model:                "gpt-5-mini",
				Input:                "hello",
				Instructions:         "be brief",
				Tools:                []map[string]any{{"type": "function", "name": "lookup"}},
				ToolChoice:           "auto",
				ParallelToolCalls:    &parallelToolCalls,
				Temperature:          &temperature,
				TopP:                 &topP,
				TopLogprobs:          &topLogprobs,
				MaxOutputTokens:      &maxOutputTokens,
				Metadata:             map[string]string{"team": "alpha"},
				Reasoning:            &Reasoning{Effort: "low"},
				Text:                 map[string]any{"format": map[string]any{"type": "text"}},
				Include:              []string{"reasoning.encrypted_content"},
				Truncation:           "auto",
				Store:                &store,
				PreviousResponseID:   "resp_previous",
				Conversation:         &ResponsesConversationRef{ID: "conv_123"},
				Prompt:               map[string]any{"id": "pmpt_123"},
				PromptCacheRetention: "24h",
				ContextManagement:    map[string]any{"truncation": "auto"},
				User:                 "tenant-123",
				ServiceTier:          "flex",
				SafetyIdentifier:     "safe_123",
				ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"future_field": json.RawMessage(`{"enabled":true}`),
				}),
			},
		},
	}

	for _, tt := range utilityRequests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			for _, field := range []string{
				"tools",
				"tool_choice",
				"parallel_tool_calls",
				"temperature",
				"top_p",
				"top_logprobs",
				"max_output_tokens",
				"metadata",
				"reasoning",
				"text",
				"include",
				"truncation",
				"store",
				"previous_response_id",
				"conversation",
				"prompt",
				"prompt_cache_retention",
				"context_management",
				"user",
				"service_tier",
				"safety_identifier",
				"future_field",
			} {
				if _, ok := decoded[field]; !ok {
					t.Fatalf("decoded utility request missing %q: %s", field, string(body))
				}
			}

			switch tt.req.(type) {
			case ResponseInputTokensRequest:
				var roundTripped ResponseInputTokensRequest
				if err := json.Unmarshal(body, &roundTripped); err != nil {
					t.Fatalf("json.Unmarshal(ResponseInputTokensRequest) error = %v", err)
				}
				if roundTripped.PreviousResponseID != "resp_previous" || roundTripped.ExtraFields.Lookup("future_field") == nil {
					t.Fatalf("round-tripped input token request lost context fields: %+v", roundTripped)
				}
			case ResponseCompactRequest:
				var roundTripped ResponseCompactRequest
				if err := json.Unmarshal(body, &roundTripped); err != nil {
					t.Fatalf("json.Unmarshal(ResponseCompactRequest) error = %v", err)
				}
				if roundTripped.PreviousResponseID != "resp_previous" || roundTripped.ExtraFields.Lookup("future_field") == nil {
					t.Fatalf("round-tripped compact request lost context fields: %+v", roundTripped)
				}
			}
		})
	}
}

func TestResponsesRequestMarshalJSON_PreservesToolCallingControls(t *testing.T) {
	parallelToolCalls := false
	body, err := json.Marshal(ResponsesRequest{
		Model: "gpt-4o-mini",
		Input: "hello",
		ToolChoice: map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": "lookup_weather",
			},
		},
		ParallelToolCalls: &parallelToolCalls,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	toolChoice, ok := decoded["tool_choice"].(map[string]any)
	if !ok {
		t.Fatalf("decoded tool_choice = %#v, want object", decoded["tool_choice"])
	}
	if typ, _ := toolChoice["type"].(string); typ != "function" {
		t.Fatalf("decoded tool_choice.type = %#v, want function", toolChoice["type"])
	}
	parallel, ok := decoded["parallel_tool_calls"].(bool)
	if !ok || parallel {
		t.Fatalf("decoded parallel_tool_calls = %#v, want false", decoded["parallel_tool_calls"])
	}
}

func TestResponsesRequestMarshalJSON_PreservesTypedInputElementContent(t *testing.T) {
	body, err := json.Marshal(ResponsesRequest{
		Model: "gpt-4o-mini",
		Input: []ResponsesInputElement{
			{
				Role:    "user",
				Content: "hello",
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	input, ok := decoded["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("decoded input = %#v, want []any len=1", decoded["input"])
	}

	first, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("decoded first input item = %#v, want object", input[0])
	}
	if role, _ := first["role"].(string); role != "user" {
		t.Fatalf("decoded role = %#v, want user", first["role"])
	}
	if content, _ := first["content"].(string); content != "hello" {
		t.Fatalf("decoded content = %#v, want hello", first["content"])
	}
}

func TestResponsesRequestJSON_PreservesUnknownNestedFields(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model":"gpt-4o-mini",
		"input":[
			{
				"type":"message",
				"role":"user",
				"content":"hello",
				"x_trace":{"id":"trace-1"}
			},
			{
				"type":"function_call",
				"call_id":"call_123",
				"name":"lookup_weather",
				"arguments":"{}",
				"strict":true
			}
		]
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	input, ok := req.Input.([]ResponsesInputElement)
	if !ok || len(input) != 2 {
		t.Fatalf("Input = %#v, want []ResponsesInputElement len=2", req.Input)
	}
	if input[0].ExtraFields.Lookup("x_trace") == nil {
		t.Fatal("input[0].x_trace missing from ExtraFields")
	}
	if input[1].ExtraFields.Lookup("strict") == nil {
		t.Fatal("input[1].strict missing from ExtraFields")
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	decodedInput, ok := decoded["input"].([]any)
	if !ok || len(decodedInput) != 2 {
		t.Fatalf("decoded input = %#v, want []any len=2", decoded["input"])
	}
	firstInput, ok := decodedInput[0].(map[string]any)
	if !ok {
		t.Fatalf("decoded input[0] = %#v, want object", decodedInput[0])
	}
	if _, ok := firstInput["x_trace"].(map[string]any); !ok {
		t.Fatalf("decoded input[0].x_trace = %#v, want object", firstInput["x_trace"])
	}
	secondInput, ok := decodedInput[1].(map[string]any)
	if !ok {
		t.Fatalf("decoded input[1] = %#v, want object", decodedInput[1])
	}
	if secondInput["strict"] != true {
		t.Fatalf("decoded input[1].strict = %#v, want true", secondInput["strict"])
	}
}

func TestResponsesRequestJSON_PreservesUnknownInputItems(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model":"gpt-5-mini",
		"input":[
			{
				"type":"reasoning",
				"id":"rs_123",
				"summary":[{"type":"summary_text","text":"Checked the facts."}]
			}
		]
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	input, ok := req.Input.([]ResponsesInputElement)
	if !ok || len(input) != 1 {
		t.Fatalf("Input = %#v, want []ResponsesInputElement len=1", req.Input)
	}
	if input[0].Type != "reasoning" {
		t.Fatalf("Input[0].Type = %q, want reasoning", input[0].Type)
	}
	if len(input[0].Raw) == 0 {
		t.Fatal("Input[0].Raw missing for unknown input item")
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(roundTrip) error = %v", err)
	}
	items := decoded["input"].([]any)
	item := items[0].(map[string]any)
	if item["type"] != "reasoning" || item["id"] != "rs_123" {
		t.Fatalf("round-tripped item = %#v, want reasoning item", item)
	}
	if _, ok := item["summary"].([]any); !ok {
		t.Fatalf("round-tripped summary = %#v, want array", item["summary"])
	}
	if _, ok := item["role"]; ok {
		t.Fatalf("unknown item gained role field: %#v", item)
	}
	if _, ok := item["content"]; ok {
		t.Fatalf("unknown item gained content field: %#v", item)
	}
}

func TestResponsesInputElementJSON_UnknownItemRoundTripHasNoDuplicateKeys(t *testing.T) {
	var elem ResponsesInputElement
	if err := json.Unmarshal([]byte(`{"type":"reasoning","id":"rs_123","summary":[]}`), &elem); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	body, err := json.Marshal(elem)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// A decode→encode round trip must not duplicate the fields preserved in Raw.
	for _, key := range []string{`"type"`, `"id"`, `"summary"`} {
		if got := bytes.Count(body, []byte(key)); got != 1 {
			t.Fatalf("key %s appears %d times in %s, want 1", key, got, body)
		}
	}
}

func TestResponsesInputElementUnmarshalJSON_ResetsReceiver(t *testing.T) {
	var elem ResponsesInputElement
	if err := json.Unmarshal([]byte(`{"type":"message","role":"user","content":"hi","x_trace":"old"}`), &elem); err != nil {
		t.Fatalf("json.Unmarshal(message) error = %v", err)
	}
	if elem.Role != "user" || elem.Content == nil || elem.ExtraFields.Lookup("x_trace") == nil {
		t.Fatalf("initial element = %+v, want populated message", elem)
	}

	if err := json.Unmarshal([]byte(`{"type":"reasoning","id":"rs_123","summary":[]}`), &elem); err != nil {
		t.Fatalf("json.Unmarshal(reasoning) error = %v", err)
	}
	if elem.Type != "reasoning" {
		t.Fatalf("Type = %q, want reasoning", elem.Type)
	}
	if elem.Role != "" || elem.Content != nil || !elem.ExtraFields.IsEmpty() {
		t.Fatalf("stale typed fields remained after unknown item decode: %+v", elem)
	}
	if len(elem.Raw) == 0 {
		t.Fatal("Raw missing for unknown item")
	}
}

func TestResponsesInputElementMarshalJSON_MergesRawUnknownItemExtras(t *testing.T) {
	elem := ResponsesInputElement{
		Type: "reasoning",
		Raw:  json.RawMessage(`{"type":"reasoning","id":"rs_123","summary":[]}`),
		ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"provider_data": json.RawMessage(`{"trace_id":"trace-1"}`),
		}),
	}

	body, err := json.Marshal(elem)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded["type"] != "reasoning" || decoded["id"] != "rs_123" {
		t.Fatalf("decoded item = %#v, want original raw reasoning item", decoded)
	}
	providerData, ok := decoded["provider_data"].(map[string]any)
	if !ok || providerData["trace_id"] != "trace-1" {
		t.Fatalf("provider_data = %#v, want merged trace id", decoded["provider_data"])
	}
}

func TestResponsesRequestJSON_PreservesVariantSpecificUnknownFields(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model":"gpt-4o-mini",
		"input":[
			{
				"type":"message",
				"id":"msg_123",
				"role":"user",
				"content":"hello"
			},
			{
				"type":"function_call_output",
				"call_id":"call_123",
				"name":"still-extra",
				"output":"{}"
			}
		]
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	input, ok := req.Input.([]ResponsesInputElement)
	if !ok || len(input) != 2 {
		t.Fatalf("Input = %#v, want []ResponsesInputElement len=2", req.Input)
	}
	if input[0].ExtraFields.Lookup("id") == nil {
		t.Fatal("message id missing from ExtraFields")
	}
	if input[1].ExtraFields.Lookup("name") == nil {
		t.Fatal("function_call_output name missing from ExtraFields")
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(roundTrip) error = %v", err)
	}
	items := decoded["input"].([]any)
	message := items[0].(map[string]any)
	if message["id"] != "msg_123" {
		t.Fatalf("message.id = %#v, want msg_123", message["id"])
	}
	callOutput := items[1].(map[string]any)
	if callOutput["name"] != "still-extra" {
		t.Fatalf("function_call_output.name = %#v, want still-extra", callOutput["name"])
	}
}

func TestResponsesRequestJSON_PreservesAgentsSDKFields(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model":"gpt-5-mini",
		"input":"hello",
		"previous_response_id":"resp_previous",
		"conversation":"conv_123",
		"include":["reasoning.encrypted_content"],
		"top_p":0.8,
		"top_logprobs":3,
		"truncation":"auto",
		"store":false,
		"prompt":{"id":"pmpt_123"},
		"prompt_cache_retention":"24h",
		"context_management":{"truncation":"auto"},
		"user":"tenant-123",
		"service_tier":"flex",
		"safety_identifier":"safe_123",
		"text":{
			"format":{
				"type":"json_schema",
				"name":"answer"
			}
		}
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if req.PreviousResponseID != "resp_previous" {
		t.Fatalf("PreviousResponseID = %q, want resp_previous", req.PreviousResponseID)
	}
	if req.Store == nil || *req.Store {
		t.Fatalf("Store = %#v, want false", req.Store)
	}
	if req.TopP == nil || *req.TopP != 0.8 {
		t.Fatalf("TopP = %#v, want 0.8", req.TopP)
	}
	if req.TopLogprobs == nil || *req.TopLogprobs != 3 {
		t.Fatalf("TopLogprobs = %#v, want 3", req.TopLogprobs)
	}
	if req.Text == nil {
		t.Fatal("Text missing")
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	textField, ok := decoded["text"].(map[string]any)
	if !ok {
		t.Fatalf("decoded text = %#v, want object", decoded["text"])
	}
	formatField, ok := textField["format"].(map[string]any)
	if !ok {
		t.Fatalf("decoded text.format = %#v, want object", textField["format"])
	}
	if formatField["type"] != "json_schema" {
		t.Fatalf("decoded text.format.type = %#v, want json_schema", formatField["type"])
	}
	if decoded["store"] != false {
		t.Fatalf("decoded store = %#v, want false", decoded["store"])
	}
	if decoded["previous_response_id"] != "resp_previous" {
		t.Fatalf("decoded previous_response_id = %#v, want resp_previous", decoded["previous_response_id"])
	}
	if decoded["conversation"] != "conv_123" {
		t.Fatalf("decoded conversation = %#v, want conv_123", decoded["conversation"])
	}
	if decoded["service_tier"] != "flex" {
		t.Fatalf("decoded service_tier = %#v, want flex", decoded["service_tier"])
	}
}

func TestResponsesRequestJSON_PreservesConversationObjectShape(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model":"gpt-5-mini",
		"input":"hello",
		"conversation":{"id":"conv_123","metadata":{"team":"alpha"}}
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if req.Conversation == nil || req.Conversation.ID != "conv_123" {
		t.Fatalf("Conversation = %+v, want id conv_123", req.Conversation)
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(roundTrip) error = %v", err)
	}
	conversation, ok := decoded["conversation"].(map[string]any)
	if !ok {
		t.Fatalf("decoded conversation = %#v, want object", decoded["conversation"])
	}
	metadata, ok := conversation["metadata"].(map[string]any)
	if !ok || metadata["team"] != "alpha" {
		t.Fatalf("decoded conversation metadata = %#v, want team alpha", conversation["metadata"])
	}
}

func TestResponsesResponseJSON_AcceptsStructuredAnnotations(t *testing.T) {
	var resp ResponsesResponse
	if err := json.Unmarshal([]byte(`{
		"id":"resp_123",
		"object":"response",
		"created_at":1677652288,
		"model":"gpt-4o-mini",
		"status":"completed",
		"output":[{
			"id":"msg_123",
			"type":"message",
			"role":"assistant",
			"status":"completed",
			"content":[{
				"type":"output_text",
				"text":"Found a result.",
				"annotations":[{
					"type":"url_citation",
					"title":"Example Domain",
					"url":"https://example.com"
				}]
			}]
		}]
	}`), &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(resp.Output) != 1 || len(resp.Output[0].Content) != 1 {
		t.Fatalf("unexpected output shape: %+v", resp.Output)
	}
	annotations := resp.Output[0].Content[0].Annotations
	if len(annotations) != 1 {
		t.Fatalf("len(Annotations) = %d, want 1", len(annotations))
	}

	var annotation map[string]any
	if err := json.Unmarshal(annotations[0], &annotation); err != nil {
		t.Fatalf("json.Unmarshal(annotation) error = %v", err)
	}
	if annotation["type"] != "url_citation" {
		t.Fatalf("annotation.type = %#v, want url_citation", annotation["type"])
	}

	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(roundTrip) error = %v", err)
	}

	output := decoded["output"].([]any)
	content := output[0].(map[string]any)["content"].([]any)
	roundTripAnnotations := content[0].(map[string]any)["annotations"].([]any)
	firstAnnotation := roundTripAnnotations[0].(map[string]any)
	if firstAnnotation["url"] != "https://example.com" {
		t.Fatalf("roundTrip annotation.url = %#v, want https://example.com", firstAnnotation["url"])
	}
}

func TestResponsesInputElementMarshalJSON_FunctionCall(t *testing.T) {
	elem := ResponsesInputElement{
		Type:      "function_call",
		CallID:    "call_123",
		Name:      "lookup_weather",
		Arguments: `{"city":"Warsaw"}`,
	}

	body, err := json.Marshal(elem)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded["type"] != "function_call" {
		t.Fatalf("type = %v, want function_call", decoded["type"])
	}
	if decoded["call_id"] != "call_123" {
		t.Fatalf("call_id = %v, want call_123", decoded["call_id"])
	}
	if decoded["name"] != "lookup_weather" {
		t.Fatalf("name = %v, want lookup_weather", decoded["name"])
	}
	// Must not emit message-specific fields.
	if _, ok := decoded["role"]; ok {
		t.Fatal("function_call should not emit role")
	}
	if _, ok := decoded["content"]; ok {
		t.Fatal("function_call should not emit content")
	}
}

func TestResponsesInputElementMarshalJSON_FunctionCallOutput(t *testing.T) {
	elem := ResponsesInputElement{
		Type:   "function_call_output",
		CallID: "call_123",
		Output: `{"temperature_c":21}`,
	}

	body, err := json.Marshal(elem)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded["type"] != "function_call_output" {
		t.Fatalf("type = %v, want function_call_output", decoded["type"])
	}
	if decoded["call_id"] != "call_123" {
		t.Fatalf("call_id = %v, want call_123", decoded["call_id"])
	}
	if decoded["output"] != `{"temperature_c":21}` {
		t.Fatalf("output = %v, want JSON string", decoded["output"])
	}
}

func TestResponsesInputElementRoundTrip(t *testing.T) {
	original := `{"model":"gpt-4o-mini","input":[
		{"role":"user","content":"What is the weather?"},
		{"type":"function_call","call_id":"call_123","name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"},
		{"type":"function_call_output","call_id":"call_123","output":"{\"temperature_c\":21}"},
		{"role":"assistant","content":"It is 21°C in Warsaw."}
	]}`

	var req ResponsesRequest
	if err := json.Unmarshal([]byte(original), &req); err != nil {
		t.Fatalf("unmarshal error = %v", err)
	}

	input, ok := req.Input.([]ResponsesInputElement)
	if !ok || len(input) != 4 {
		t.Fatalf("Input = %#v, want []ResponsesInputElement len=4", req.Input)
	}

	// Verify each element type.
	if input[0].Type != "" || input[0].Role != "user" {
		t.Fatalf("Input[0] = %+v, want message role=user", input[0])
	}
	if input[1].Type != "function_call" || input[1].Name != "lookup_weather" {
		t.Fatalf("Input[1] = %+v, want function_call", input[1])
	}
	if input[2].Type != "function_call_output" || input[2].Output != `{"temperature_c":21}` {
		t.Fatalf("Input[2] = %+v, want function_call_output", input[2])
	}
	if input[3].Role != "assistant" {
		t.Fatalf("Input[3] = %+v, want message role=assistant", input[3])
	}

	// Marshal and re-unmarshal to verify round-trip.
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error = %v", err)
	}

	var req2 ResponsesRequest
	if err := json.Unmarshal(body, &req2); err != nil {
		t.Fatalf("re-unmarshal error = %v", err)
	}

	input2, ok := req2.Input.([]ResponsesInputElement)
	if !ok || len(input2) != 4 {
		t.Fatalf("round-trip Input = %#v, want []ResponsesInputElement len=4", req2.Input)
	}
	if input2[1].Type != "function_call" || input2[1].Arguments != `{"city":"Warsaw"}` {
		t.Fatalf("round-trip Input[1] = %+v, want function_call with arguments preserved", input2[1])
	}
	if input2[2].Type != "function_call_output" || input2[2].Output != `{"temperature_c":21}` {
		t.Fatalf("round-trip Input[2] = %+v, want function_call_output with output preserved", input2[2])
	}
}
