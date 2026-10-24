package core

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResponsesRequestUnmarshalJSON_StringInput(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":"hello"}`), &req)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini", req.Model)

	input, ok := req.Input.(string)
	require.True(t, ok)
	require.Equal(t, "hello", input, "Input = %#v, want string hello", req.Input)
}

func TestResponsesRequestUnmarshalJSON_ArrayInput(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`), &req)
	require.NoError(t, err)

	input, ok := req.Input.([]ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 1, "Input = %#v, want []ResponsesInputElement len=1", req.Input)
	require.Equal(t, "user", input[0].Role)
}

func TestResponsesRequestJSON_CanonicalizesToolObjectKeyOrder(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"model":"gpt-4o-mini","input":"hi","tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"string"}}}}]}`),
		[]byte(`{"tools":[{"parameters":{"properties":{"a":{"type":"string"},"z":{"type":"string"}},"type":"object"},"name":"lookup","type":"function"}],"input":"hi","model":"gpt-4o-mini"}`),
	}

	encoded := make([][]byte, len(bodies))
	for i, body := range bodies {
		var req ResponsesRequest
		err := json.Unmarshal(body, &req)
		require.NoError(t, err)

		encoded[i], err = json.Marshal(req)
		require.NoError(t, err)
	}
	require.Equal(t, encoded[1], encoded[0])
}

func TestResponsesRequestUnmarshalJSON_ArrayInputFunctionCall(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":[
		{"type":"function_call","call_id":"call_123","name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"},
		{"type":"function_call_output","call_id":"call_123","output":{"temperature_c":21}}
	]}`), &req)
	require.NoError(t, err)

	input, ok := req.Input.([]ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 2, "Input = %#v, want []ResponsesInputElement len=2", req.Input)
	require.Equal(t, "function_call", input[0].Type)
	require.Equal(t, "call_123", input[0].CallID)
	require.Equal(t, "lookup_weather", input[0].Name, "Input[0] = %+v, want function_call with call_id=call_123 name=lookup_weather", input[0])
	require.Equal(t, `{"city":"Warsaw"}`, input[0].Arguments)
	require.Equal(t, "function_call_output", input[1].Type)
	require.Equal(t, "call_123", input[1].CallID, "Input[1] = %+v, want function_call_output with call_id=call_123", input[1])
	require.Equal(t, `{"temperature_c":21}`, input[1].Output)
}

func TestResponsesRequestUnmarshalJSON_FunctionCallAcceptsIDField(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{"model":"gpt-4o-mini","input":[
		{"type":"function_call","id":"call_456","name":"get_time","arguments":"{}"}
	]}`), &req)
	require.NoError(t, err)

	input := req.Input.([]ResponsesInputElement)
	require.Equal(t, "call_456", input[0].CallID)
}

func TestResponsesRequestUnmarshalJSON_PreservesToolCallingControls(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"gpt-4o-mini",
		"input":"hello",
		"tool_choice":{"type":"function","function":{"name":"lookup_weather"}},
		"parallel_tool_calls":false
	}`), &req)
	require.NoError(t, err)

	toolChoice, ok := req.ToolChoice.(map[string]any)
	require.True(t, ok, "ToolChoice = %#v, want object", req.ToolChoice)
	typ, _ := toolChoice["type"].(string)
	require.Equal(t, "function", typ)
	require.NotNil(t, req.ParallelToolCalls)
	require.False(t, *req.ParallelToolCalls)
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
			err := json.Unmarshal([]byte(tt.raw), &ref)
			require.NoError(t, err)

			ref.ID = tt.id

			body, err := json.Marshal(ref)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(body))
		})
	}
}

func TestResponsesConversationRefMarshalJSON_InvalidRaw(t *testing.T) {
	ref := ResponsesConversationRef{
		ID:  "conv_new",
		Raw: json.RawMessage(`{"id":`),
	}
	_, err := json.Marshal(ref)
	require.Error(t, err)
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
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	inputRaw, ok := decoded["input"]
	require.True(t, ok, "marshal output missing input: %s", string(body))

	input, ok := inputRaw.([]any)
	require.True(t, ok)
	require.Len(t, input, 1)

	firstMsg, ok := input[0].(map[string]any)
	require.True(t, ok, "first input item = %#v, want object", input[0])
	role, _ := firstMsg["role"].(string)
	require.Equal(t, "user", role)

	contentRaw, ok := firstMsg["content"]
	require.True(t, ok, "first input missing content: %#v", firstMsg)

	content, ok := contentRaw.([]any)
	require.True(t, ok)
	require.Len(t, content, 1)

	firstPart, ok := content[0].(map[string]any)
	require.True(t, ok, "first content part = %#v, want object", content[0])
	typ, _ := firstPart["type"].(string)
	require.Equal(t, "input_text", typ)
	text, _ := firstPart["text"].(string)
	require.Equal(t, "hello", text)
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
			require.NoError(t, err)

			var decoded map[string]any
			err = json.Unmarshal(body, &decoded)
			require.NoError(t, err)
			require.Equal(t, "openai_primary", decoded["provider"], "provider = %#v, want openai_primary in %s", decoded["provider"], string(body))

			switch original := tt.req.(type) {
			case ResponseInputTokensRequest:
				var roundTripped ResponseInputTokensRequest
				err := json.Unmarshal(body, &roundTripped)
				require.NoError(t, err)
				require.Equal(t, original.Provider, roundTripped.Provider)
				input, ok := roundTripped.Input.(string)
				require.True(t, ok)
				require.Equal(t, original.Input, input)

			case ResponseCompactRequest:
				var roundTripped ResponseCompactRequest
				err := json.Unmarshal(body, &roundTripped)
				require.NoError(t, err)
				require.Equal(t, original.Provider, roundTripped.Provider)
				input, ok := roundTripped.Input.(string)
				require.True(t, ok)
				require.Equal(t, original.Input, input)

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
			require.NoError(t, err)

			var decoded map[string]any
			err = json.Unmarshal(body, &decoded)
			require.NoError(t, err)

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
				_, ok := decoded[field]
				require.True(t, ok, "decoded utility request missing %q: %s", field, string(body))
			}

			switch tt.req.(type) {
			case ResponseInputTokensRequest:
				var roundTripped ResponseInputTokensRequest
				err := json.Unmarshal(body, &roundTripped)
				require.NoError(t, err)
				require.Equal(t, "resp_previous", roundTripped.PreviousResponseID)
				require.NotNil(t, roundTripped.ExtraFields.Lookup("future_field"), "round-tripped input token request lost context fields: %+v", roundTripped)

			case ResponseCompactRequest:
				var roundTripped ResponseCompactRequest
				err := json.Unmarshal(body, &roundTripped)
				require.NoError(t, err)
				require.Equal(t, "resp_previous", roundTripped.PreviousResponseID)
				require.NotNil(t, roundTripped.ExtraFields.Lookup("future_field"), "round-tripped compact request lost context fields: %+v", roundTripped)
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
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	toolChoice, ok := decoded["tool_choice"].(map[string]any)
	require.True(t, ok, "decoded tool_choice = %#v, want object", decoded["tool_choice"])
	typ, _ := toolChoice["type"].(string)
	require.Equal(t, "function", typ)

	parallel, ok := decoded["parallel_tool_calls"].(bool)
	require.True(t, ok)
	require.False(t, parallel)
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
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	input, ok := decoded["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)

	first, ok := input[0].(map[string]any)
	require.True(t, ok, "decoded first input item = %#v, want object", input[0])
	role, _ := first["role"].(string)
	require.Equal(t, "user", role)
	content, _ := first["content"].(string)
	require.Equal(t, "hello", content)
}

func TestResponsesRequestJSON_PreservesUnknownNestedFields(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{
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
	}`), &req)
	require.NoError(t, err)

	input, ok := req.Input.([]ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 2, "Input = %#v, want []ResponsesInputElement len=2", req.Input)
	require.NotNil(t, input[0].ExtraFields.Lookup("x_trace"))
	require.NotNil(t, input[1].ExtraFields.Lookup("strict"))

	body, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	decodedInput, ok := decoded["input"].([]any)
	require.True(t, ok)
	require.Len(t, decodedInput, 2, "decoded input = %#v, want []any len=2", decoded["input"])

	firstInput, ok := decodedInput[0].(map[string]any)
	require.True(t, ok, "decoded input[0] = %#v, want object", decodedInput[0])
	_, ok = firstInput["x_trace"].(map[string]any)
	require.True(t, ok, "decoded input[0].x_trace = %#v, want object", firstInput["x_trace"])

	secondInput, ok := decodedInput[1].(map[string]any)
	require.True(t, ok, "decoded input[1] = %#v, want object", decodedInput[1])
	require.Equal(t, true, secondInput["strict"])
}

func TestResponsesRequestJSON_PreservesUnknownInputItems(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"gpt-5-mini",
		"input":[
			{
				"type":"reasoning",
				"id":"rs_123",
				"summary":[{"type":"summary_text","text":"Checked the facts."}]
			}
		]
	}`), &req)
	require.NoError(t, err)

	input, ok := req.Input.([]ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 1, "Input = %#v, want []ResponsesInputElement len=1", req.Input)
	require.Equal(t, "reasoning", input[0].Type)
	require.NotEmpty(t, input[0].Raw)

	body, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	items := decoded["input"].([]any)
	item := items[0].(map[string]any)
	require.Equal(t, "reasoning", item["type"])
	require.Equal(t, "rs_123", item["id"], "round-tripped item = %#v, want reasoning item", item)
	_, ok = item["summary"].([]any)
	require.True(t, ok, "round-tripped summary = %#v, want array", item["summary"])
	_, ok = item["role"]
	require.False(t, ok, "unknown item gained role field: %#v", item)
	_, ok = item["content"]
	require.False(t, ok, "unknown item gained content field: %#v", item)
}

func TestResponsesInputElementJSON_UnknownItemRoundTripHasNoDuplicateKeys(t *testing.T) {
	var elem ResponsesInputElement
	err := json.Unmarshal([]byte(`{"type":"reasoning","id":"rs_123","summary":[]}`), &elem)
	require.NoError(t, err)

	body, err := json.Marshal(elem)
	require.NoError(t, err)

	// A decode→encode round trip must not duplicate the fields preserved in Raw.
	for _, key := range []string{`"type"`, `"id"`, `"summary"`} {
		got := bytes.Count(body, []byte(key))
		require.Equal(t, 1, got, "key %s appears %d times in %s, want 1", key, got, body)
	}
}

func TestResponsesInputElementUnmarshalJSON_ResetsReceiver(t *testing.T) {
	var elem ResponsesInputElement
	err := json.Unmarshal([]byte(`{"type":"message","role":"user","content":"hi","x_trace":"old"}`), &elem)
	require.NoError(t, err)
	require.Equal(t, "user", elem.Role)
	require.NotNil(t, elem.Content)
	require.NotNil(t, elem.ExtraFields.Lookup("x_trace"), "initial element = %+v, want populated message", elem)
	err = json.Unmarshal([]byte(`{"type":"reasoning","id":"rs_123","summary":[]}`), &elem)
	require.NoError(t, err)
	require.Equal(t, "reasoning", elem.Type)
	require.Empty(t, elem.Role)
	require.Nil(t, elem.Content)
	require.True(t, elem.ExtraFields.IsEmpty(), "stale typed fields remained after unknown item decode: %+v", elem)
	require.NotEmpty(t, elem.Raw)
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
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)
	require.Equal(t, "reasoning", decoded["type"])
	require.Equal(t, "rs_123", decoded["id"], "decoded item = %#v, want original raw reasoning item", decoded)

	providerData, ok := decoded["provider_data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "trace-1", providerData["trace_id"], "provider_data = %#v, want merged trace id", decoded["provider_data"])
}

func TestResponsesOutputItemJSONPreservesReasoningFields(t *testing.T) {
	raw := []byte(`{"id":"rs_123","type":"reasoning","summary":[],"encrypted_content":"opaque","provider_trace":{"id":"trace_1"}}`)
	var item ResponsesOutputItem
	err := json.Unmarshal(raw, &item)
	require.NoError(t, err)

	body, err := json.Marshal(item)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)
	_, ok := decoded["summary"].([]any)
	require.True(t, ok)
	require.Equal(t, "opaque", decoded["encrypted_content"], "round-tripped item = %#v, want reasoning fields", decoded)
	trace, ok := decoded["provider_trace"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "trace_1", trace["id"], "provider_trace = %#v, want trace_1", decoded["provider_trace"])
}

func TestResponsesRequestJSON_PreservesVariantSpecificUnknownFields(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{
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
	}`), &req)
	require.NoError(t, err)

	input, ok := req.Input.([]ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 2, "Input = %#v, want []ResponsesInputElement len=2", req.Input)
	require.NotNil(t, input[0].ExtraFields.Lookup("id"))
	require.NotNil(t, input[1].ExtraFields.Lookup("name"))

	body, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	items := decoded["input"].([]any)
	message := items[0].(map[string]any)
	require.Equal(t, "msg_123", message["id"])

	callOutput := items[1].(map[string]any)
	require.Equal(t, "still-extra", callOutput["name"])
}

func TestResponsesRequestJSON_PreservesAgentsSDKFields(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{
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
	}`), &req)
	require.NoError(t, err)
	require.Equal(t, "resp_previous", req.PreviousResponseID)
	require.NotNil(t, req.Store)
	require.False(t, *req.Store)
	require.NotNil(t, req.TopP)
	require.Equal(t, 0.8, *req.TopP)
	require.NotNil(t, req.TopLogprobs)
	require.Equal(t, 3, *req.TopLogprobs)
	require.NotNil(t, req.Text)

	body, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	textField, ok := decoded["text"].(map[string]any)
	require.True(t, ok, "decoded text = %#v, want object", decoded["text"])

	formatField, ok := textField["format"].(map[string]any)
	require.True(t, ok, "decoded text.format = %#v, want object", textField["format"])
	require.Equal(t, "json_schema", formatField["type"])
	require.Equal(t, false, decoded["store"])
	require.Equal(t, "resp_previous", decoded["previous_response_id"])
	require.Equal(t, "conv_123", decoded["conversation"])
	require.Equal(t, "flex", decoded["service_tier"])
}

func TestResponsesRequestJSON_PreservesConversationObjectShape(t *testing.T) {
	var req ResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"gpt-5-mini",
		"input":"hello",
		"conversation":{"id":"conv_123","metadata":{"team":"alpha"}}
	}`), &req)
	require.NoError(t, err)
	require.NotNil(t, req.Conversation)
	require.Equal(t, "conv_123", req.Conversation.ID)

	body, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	conversation, ok := decoded["conversation"].(map[string]any)
	require.True(t, ok, "decoded conversation = %#v, want object", decoded["conversation"])

	metadata, ok := conversation["metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "alpha", metadata["team"], "decoded conversation metadata = %#v, want team alpha", conversation["metadata"])
}

func TestResponsesResponseJSON_AcceptsStructuredAnnotations(t *testing.T) {
	var resp ResponsesResponse
	err := json.Unmarshal([]byte(`{
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
	}`), &resp)
	require.NoError(t, err)
	require.Len(t, resp.Output, 1)
	require.Len(t, resp.Output[0].Content, 1)

	annotations := resp.Output[0].Content[0].Annotations
	require.Len(t, annotations, 1)

	var annotation map[string]any
	err = json.Unmarshal(annotations[0], &annotation)
	require.NoError(t, err)
	require.Equal(t, "url_citation", annotation["type"])

	body, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	output := decoded["output"].([]any)
	content := output[0].(map[string]any)["content"].([]any)
	roundTripAnnotations := content[0].(map[string]any)["annotations"].([]any)
	firstAnnotation := roundTripAnnotations[0].(map[string]any)
	require.Equal(t, "https://example.com", firstAnnotation["url"])
}

func TestResponsesInputElementMarshalJSON_FunctionCall(t *testing.T) {
	elem := ResponsesInputElement{
		Type:      "function_call",
		CallID:    "call_123",
		Name:      "lookup_weather",
		Arguments: `{"city":"Warsaw"}`,
	}

	body, err := json.Marshal(elem)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)
	require.Equal(t, "function_call", decoded["type"])
	require.Equal(t, "call_123", decoded["call_id"])
	require.Equal(t, "lookup_weather", decoded["name"])
	_, ok := // Must not emit message-specific fields.
		decoded["role"]
	require.False(t, ok)
	_, ok = decoded["content"]
	require.False(t, ok)
}

func TestResponsesInputElementMarshalJSON_FunctionCallOutput(t *testing.T) {
	elem := ResponsesInputElement{
		Type:   "function_call_output",
		CallID: "call_123",
		Output: `{"temperature_c":21}`,
	}

	body, err := json.Marshal(elem)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)
	require.Equal(t, "function_call_output", decoded["type"])
	require.Equal(t, "call_123", decoded["call_id"])
	require.Equal(t, `{"temperature_c":21}`, decoded["output"])
}

func TestResponsesInputElementRoundTrip(t *testing.T) {
	original := `{"model":"gpt-4o-mini","input":[
		{"role":"user","content":"What is the weather?"},
		{"type":"function_call","call_id":"call_123","name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"},
		{"type":"function_call_output","call_id":"call_123","output":"{\"temperature_c\":21}"},
		{"role":"assistant","content":"It is 21°C in Warsaw."}
	]}`

	var req ResponsesRequest
	err := json.Unmarshal([]byte(original), &req)
	require.NoError(t, err)

	input, ok := req.Input.([]ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input, 4, "Input = %#v, want []ResponsesInputElement len=4", req.Input)

	// Verify each element type.
	require.Empty(t, input[0].Type)
	require.Equal(t, "user", input[0].Role, "Input[0] = %+v, want message role=user", input[0])
	require.Equal(t, "function_call", input[1].Type)
	require.Equal(t, "lookup_weather", input[1].Name, "Input[1] = %+v, want function_call", input[1])
	require.Equal(t, "function_call_output", input[2].Type)
	require.Equal(t, `{"temperature_c":21}`, input[2].Output, "Input[2] = %+v, want function_call_output", input[2])
	require.Equal(t, "assistant", input[3].Role, "Input[3] = %+v, want message role=assistant", input[3])

	// Marshal and re-unmarshal to verify round-trip.
	body, err := json.Marshal(req)
	require.NoError(t, err)

	var req2 ResponsesRequest
	err = json.Unmarshal(body, &req2)
	require.NoError(t, err)

	input2, ok := req2.Input.([]ResponsesInputElement)
	require.True(t, ok)
	require.Len(t, input2, 4, "round-trip Input = %#v, want []ResponsesInputElement len=4", req2.Input)
	require.Equal(t, "function_call", input2[1].Type)
	require.Equal(t, `{"city":"Warsaw"}`, input2[1].Arguments, "round-trip Input[1] = %+v, want function_call with arguments preserved", input2[1])
	require.Equal(t, "function_call_output", input2[2].Type)
	require.Equal(t, `{"temperature_c":21}`, input2[2].Output, "round-trip Input[2] = %+v, want function_call_output with output preserved", input2[2])
}

func TestResponsesContentItemMarshalJSON_Annotations(t *testing.T) {
	tests := []struct {
		name string
		item ResponsesContentItem
		want string
	}{
		{"output_text nil annotations", ResponsesContentItem{Type: "output_text", Text: "hi"}, `{"type":"output_text","text":"hi","annotations":[]}`},
		{"output_text empty annotations", ResponsesContentItem{Type: "output_text", Text: "hi", Annotations: []json.RawMessage{}}, `{"type":"output_text","text":"hi","annotations":[]}`},
		{"output_text with annotations", ResponsesContentItem{Type: "output_text", Text: "hi", Annotations: []json.RawMessage{json.RawMessage(`{"type":"url_citation"}`)}}, `{"type":"output_text","text":"hi","annotations":[{"type":"url_citation"}]}`},
		{"input_text omits annotations", ResponsesContentItem{Type: "input_text", Text: "hi"}, `{"type":"input_text","text":"hi"}`},
		{"input_text omits empty annotations", ResponsesContentItem{Type: "input_text", Text: "hi", Annotations: []json.RawMessage{}}, `{"type":"input_text","text":"hi"}`},
		{"input_text keeps supplied annotations", ResponsesContentItem{Type: "input_text", Text: "hi", Annotations: []json.RawMessage{json.RawMessage(`{"type":"url_citation"}`)}}, `{"type":"input_text","text":"hi","annotations":[{"type":"url_citation"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.item)
			require.NoError(t, err)
			require.Equal(t, tt.want, string(got))
		})
	}
}

func TestResponsesBlocksFromContentPartsKeepsVocabularyAndExtras(t *testing.T) {
	parts := []ContentPart{
		{
			Type: "input_text", Text: "hello",
			ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_note": json.RawMessage(`"keep"`)}),
		},
		{Type: "input_image", ImageURL: &ImageURLContent{URL: "https://example.com/a.png", Detail: "low"}},
	}
	blocks := ResponsesBlocksFromContentParts(parts)
	require.Len(t, blocks, 2)

	text, _ := blocks[0].(map[string]any)
	require.Equal(t, "input_text", text["type"])
	require.Equal(t, "hello", text["text"])
	require.Equal(t, "keep", text["x_note"], "text block = %v", text)

	image, _ := blocks[1].(map[string]any)
	imageURL, _ := image["image_url"].(map[string]any)
	require.Equal(t, "input_image", image["type"])
	require.Equal(t, "https://example.com/a.png", imageURL["url"])
	require.Equal(t, "low", imageURL["detail"], "image block = %v", image)

	encoded, err := json.Marshal(blocks)
	require.NoError(t, err)
	require.False(t, bytes.Contains(encoded, []byte(`"type":"text"`)), "blocks serialized with Chat vocabulary: %s", encoded)
}

func TestResponsesBlocksFromContentPartsRejectsUnencodablePart(t *testing.T) {
	got := ResponsesBlocksFromContentParts([]ContentPart{{Type: "input_file"}})
	require.Nil(t, got)
	got = ResponsesBlocksFromContentParts(nil)
	require.Empty(t, got)
}

func TestResponsesBlocksFromContentPartsKeepsLargeIntegersExact(t *testing.T) {
	parts := []ContentPart{{
		Type: "input_text", Text: "hello",
		ExtraFields: UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"x_id":    json.RawMessage(`9007199254740993`),
			"x_ratio": json.RawMessage(`0.1`),
		}),
	}}
	encoded, err := json.Marshal(ResponsesBlocksFromContentParts(parts))
	require.NoError(t, err)

	for _, want := range []string{`"x_id":9007199254740993`, `"x_ratio":0.1`} {
		require.Contains(t, string(encoded), want)
	}
}
