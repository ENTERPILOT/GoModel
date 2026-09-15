package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelPricingTierUnmarshalUpToTokens(t *testing.T) {
	var pricing ModelPricing
	err := json.Unmarshal([]byte(`{
		"currency": "USD",
		"tiers": [
			{"up_to_tokens": 200000, "input_per_mtok": 1.25, "output_per_mtok": 10.0}
		]
	}`), &pricing)
	require.NoError(t, err)
	require.Len(t, pricing.Tiers, 1)
	require.NotNil(t, pricing.Tiers[0].UpToTokens)
	require.Equal(t, float64(200000), *pricing.Tiers[0].UpToTokens)

	cloned := pricing.Clone()
	require.NotNil(t, cloned)
	require.Len(t, cloned.Tiers, 1)
	require.NotNil(t, cloned.Tiers[0].UpToTokens)
	require.Equal(t, float64(200000), *cloned.Tiers[0].UpToTokens)
	require.NotSame(t, pricing.Tiers[0].UpToTokens, cloned.Tiers[0].UpToTokens)

	*pricing.Tiers[0].UpToTokens = 123
	require.Equal(t, float64(200000), *cloned.Tiers[0].UpToTokens)
}

func TestMessageUnmarshalJSON_AllowsNullContent(t *testing.T) {
	payload := []byte(`{
		"role":"assistant",
		"content":null,
		"tool_calls":[
			{
				"id":"call_123",
				"type":"function",
				"function":{"name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"}
			}
		]
	}`)

	var msg Message
	err := json.Unmarshal(payload, &msg)
	require.NoError(t, err)
	require.Nil(t, msg.Content)
	require.True(t, msg.ContentNull)
	require.Len(t, msg.ToolCalls, 1)
	require.Equal(t, "call_123", msg.ToolCalls[0].ID)
}

func TestMessageUnmarshalJSON_PreservesStringContent(t *testing.T) {
	payload := []byte(`{"role":"assistant","content":"hello"}`)

	var msg Message
	err := json.Unmarshal(payload, &msg)
	require.NoError(t, err)
	require.Equal(t, "hello", msg.Content)
	require.False(t, msg.ContentNull)
}

func TestMessageMarshalJSON_PreservesNullContent(t *testing.T) {
	payload, err := json.Marshal(Message{
		Role:        "assistant",
		ContentNull: true,
		ToolCalls: []ToolCall{
			{
				ID:   "call_123",
				Type: "function",
				Function: FunctionCall{
					Name:      "lookup_weather",
					Arguments: `{"city":"Warsaw"}`,
				},
			},
		},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"}}]}`, string(payload))
}

func TestMessageMarshalJSON_ContentWinsOverContentNull(t *testing.T) {
	payload, err := json.Marshal(Message{
		Role:        "assistant",
		Content:     "hello",
		ContentNull: true,
	})
	require.NoError(t, err)

	var raw map[string]any
	err = json.Unmarshal(payload, &raw)
	require.NoError(t, err)
	require.Equal(t, "hello", raw["content"])
}

func TestChatResponseJSON_PreservesSystemFingerprint(t *testing.T) {
	payload := []byte(`{
		"id":"chatcmpl-123",
		"object":"chat.completion",
		"created":1741723200,
		"model":"gpt-4o-mini",
		"provider":"openai",
		"system_fingerprint":"fp_abc123",
		"choices":[
			{
				"index":0,
				"message":{"role":"assistant","content":"hello"},
				"finish_reason":"stop"
			}
		],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`)

	var resp ChatResponse
	err := json.Unmarshal(payload, &resp)
	require.NoError(t, err)
	require.Equal(t, "fp_abc123", resp.SystemFingerprint)

	body, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)
	require.Equal(t, "fp_abc123", decoded["system_fingerprint"])
}

func TestChatResponseJSON_PreservesChoiceLogprobs(t *testing.T) {
	payload := []byte(`{
		"id":"chatcmpl-123",
		"object":"chat.completion",
		"created":1741723200,
		"model":"gpt-4o-mini",
		"provider":"openai",
		"choices":[
			{
				"index":0,
				"message":{"role":"assistant","content":"hello"},
				"finish_reason":"stop",
				"logprobs":{
					"content":[
						{
							"token":"hello",
							"logprob":-0.1,
							"top_logprobs":[{"token":"hello","logprob":-0.1}]
						}
					]
				}
			}
		],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`)

	var resp ChatResponse
	err := json.Unmarshal(payload, &resp)
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	require.NotEmpty(t, string(resp.Choices[0].Logprobs))

	body, err := json.Marshal(resp)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	choices, ok := decoded["choices"].([]any)
	require.True(t, ok)
	require.Len(t, choices, 1)

	choice, ok := choices[0].(map[string]any)
	require.True(t, ok, "decoded choice = %#v, want object", choices[0])

	logprobs, ok := choice["logprobs"].(map[string]any)
	require.True(t, ok, "decoded logprobs = %#v, want object", choice["logprobs"])

	content, ok := logprobs["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
}

func TestChatRequestWithStreaming_PreservesToolFields(t *testing.T) {
	parallelToolCalls := false
	req := &ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []Message{
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: FunctionCall{
							Name:      "lookup_weather",
							Arguments: `{"city":"Warsaw"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_123", Content: `{"temperature_c":21}`},
		},
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name": "lookup_weather",
				},
			},
		},
		ToolChoice:        map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
		ParallelToolCalls: &parallelToolCalls,
	}

	streamReq := req.WithStreaming()

	require.True(t, streamReq.Stream)
	require.Len(t, streamReq.Tools, 1)
	require.NotNil(t, streamReq.ToolChoice)
	require.NotNil(t, streamReq.ParallelToolCalls)
	require.False(t, *streamReq.ParallelToolCalls)
	require.Len(t, streamReq.Messages, 2)
	require.Len(t, streamReq.Messages[0].ToolCalls, 1)
	require.Equal(t, "call_123", streamReq.Messages[0].ToolCalls[0].ID)
	require.Equal(t, "call_123", streamReq.Messages[1].ToolCallID)
}

func TestChatRequestJSON_PreservesUnknownFields(t *testing.T) {
	payload := []byte(`{
		"model":"gpt-5-mini",
		"messages":[{"role":"user","content":"return json"}],
		"response_format":{
			"type":"json_schema",
			"json_schema":{
				"name":"math_response",
				"schema":{"type":"object","properties":{"answer":{"type":"string"}}}
			}
		}
	}`)

	var req ChatRequest
	err := json.Unmarshal(payload, &req)
	require.NoError(t, err)
	require.NotNil(t, req.ExtraFields.Lookup("response_format"))

	body, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	responseFormat, ok := decoded["response_format"].(map[string]any)
	require.True(t, ok, "decoded response_format = %#v, want object", decoded["response_format"])
	require.Equal(t, "json_schema", responseFormat["type"])
}

func TestChatRequestJSON_PreservesUnknownNestedFields(t *testing.T) {
	payload := []byte(`{
		"model":"gpt-5-mini",
		"messages":[
			{
				"role":"user",
				"name":"alice",
				"content":[
					{
						"type":"text",
						"text":"hello",
						"cache_control":{"type":"ephemeral"}
					}
				]
			},
			{
				"role":"assistant",
				"content":null,
				"tool_calls":[
					{
						"id":"call_123",
						"type":"function",
						"vendor_data":{"trace":"abc"},
						"function":{
							"name":"lookup_weather",
							"arguments":"{}",
							"strict":true
						}
					}
				]
			}
		]
	}`)

	var req ChatRequest
	err := json.Unmarshal(payload, &req)
	require.NoError(t, err)
	require.Len(t, req.Messages, 2)
	require.NotNil(t, req.Messages[0].ExtraFields.Lookup("name"))

	parts, ok := req.Messages[0].Content.([]ContentPart)
	require.True(t, ok)
	require.Len(t, parts, 1, "message[0].content = %#v, want []ContentPart len=1", req.Messages[0].Content)
	require.NotNil(t, parts[0].ExtraFields.Lookup("cache_control"))
	require.Len(t, req.Messages[1].ToolCalls, 1)
	require.NotNil(t, req.Messages[1].ToolCalls[0].ExtraFields.Lookup("vendor_data"))
	require.NotNil(t, req.Messages[1].ToolCalls[0].Function.ExtraFields.Lookup("strict"))

	body, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)

	messages, ok := decoded["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 2)

	firstMsg, ok := messages[0].(map[string]any)
	require.True(t, ok, "messages[0] = %#v, want object", messages[0])
	require.Equal(t, "alice", firstMsg["name"])

	firstContent, ok := firstMsg["content"].([]any)
	require.True(t, ok)
	require.Len(t, firstContent, 1, "messages[0].content = %#v, want []any len=1", firstMsg["content"])

	firstPart, ok := firstContent[0].(map[string]any)
	require.True(t, ok, "messages[0].content[0] = %#v, want object", firstContent[0])
	_, ok = firstPart["cache_control"].(map[string]any)
	require.True(t, ok, "messages[0].content[0].cache_control = %#v, want object", firstPart["cache_control"])

	secondMsg, ok := messages[1].(map[string]any)
	require.True(t, ok, "messages[1] = %#v, want object", messages[1])

	toolCalls, ok := secondMsg["tool_calls"].([]any)
	require.True(t, ok)
	require.Len(t, toolCalls, 1, "messages[1].tool_calls = %#v, want []any len=1", secondMsg["tool_calls"])

	toolCall, ok := toolCalls[0].(map[string]any)
	require.True(t, ok, "tool_calls[0] = %#v, want object", toolCalls[0])
	_, ok = toolCall["vendor_data"].(map[string]any)
	require.True(t, ok, "tool_calls[0].vendor_data = %#v, want object", toolCall["vendor_data"])

	function, ok := toolCall["function"].(map[string]any)
	require.True(t, ok, "tool_calls[0].function = %#v, want object", toolCall["function"])
	require.Equal(t, true, function["strict"])
}

func TestEmbeddingRequestJSON_PreservesUnknownFields(t *testing.T) {
	payload := []byte(`{
		"model":"text-embedding-3-small",
		"input":"hello",
		"user":"tenant-123"
	}`)

	var req EmbeddingRequest
	err := json.Unmarshal(payload, &req)
	require.NoError(t, err)
	require.NotNil(t, req.ExtraFields.Lookup("user"), "user missing from ExtraFields: %+v", req.ExtraFields)

	body, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded map[string]any
	err = json.Unmarshal(body, &decoded)
	require.NoError(t, err)
	require.Equal(t, "tenant-123", decoded["user"])
}

func TestResponsesRequestWithStreaming_PreservesToolFields(t *testing.T) {
	parallelToolCalls := false
	req := &ResponsesRequest{
		Model:             "gpt-4o-mini",
		Input:             "Hello",
		Tools:             []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup_weather"}}},
		ToolChoice:        map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
		ParallelToolCalls: &parallelToolCalls,
	}

	streamReq := req.WithStreaming()

	require.True(t, streamReq.Stream)
	require.Len(t, streamReq.Tools, 1)
	require.NotNil(t, streamReq.ToolChoice)
	require.NotNil(t, streamReq.ParallelToolCalls)
	require.False(t, *streamReq.ParallelToolCalls)
}

func TestCategoriesForModes_KnownModes(t *testing.T) {
	tests := []struct {
		modes []string
		want  []ModelCategory
	}{
		{[]string{"chat"}, []ModelCategory{CategoryTextGeneration}},
		{[]string{"completion"}, []ModelCategory{CategoryTextGeneration}},
		{[]string{"responses"}, []ModelCategory{CategoryTextGeneration}},
		{[]string{"embedding"}, []ModelCategory{CategoryEmbedding}},
		{[]string{"rerank"}, []ModelCategory{CategoryEmbedding}},
		{[]string{"image_generation"}, []ModelCategory{CategoryImage}},
		{[]string{"image_edit"}, []ModelCategory{CategoryImage}},
		{[]string{"audio_transcription"}, []ModelCategory{CategoryAudio}},
		{[]string{"audio_speech"}, []ModelCategory{CategoryAudio}},
		{[]string{"video_generation"}, []ModelCategory{CategoryVideo}},
		{[]string{"moderation"}, []ModelCategory{CategoryUtility}},
		{[]string{"ocr"}, []ModelCategory{CategoryUtility}},
		{[]string{"search"}, []ModelCategory{CategoryUtility}},
	}

	for _, tt := range tests {
		t.Run(tt.modes[0], func(t *testing.T) {
			assert.Equal(t, tt.want, CategoriesForModes(tt.modes), "CategoriesForModes(%v)", tt.modes)
		})
	}
}

func TestCategoriesForModes_MultiMode(t *testing.T) {
	cats := CategoriesForModes([]string{"chat", "image_generation", "audio_speech"})
	require.Equal(t, []ModelCategory{CategoryTextGeneration, CategoryImage, CategoryAudio}, cats)
}

func TestCategoriesForModes_Dedup(t *testing.T) {
	// "chat" and "completion" both map to text_generation — should deduplicate
	cats := CategoriesForModes([]string{"chat", "completion"})
	require.Len(t, cats, 1)
	assert.Equal(t, CategoryTextGeneration, cats[0])
}

func TestCategoriesForModes_UnknownMode(t *testing.T) {
	cats := CategoriesForModes([]string{"unknown_mode"})
	assert.Empty(t, cats)
}

func TestCategoriesForModes_Empty(t *testing.T) {
	cats := CategoriesForModes(nil)
	assert.Empty(t, cats)

	cats = CategoriesForModes([]string{})
	assert.Empty(t, cats)
}

func TestAllCategories_Order(t *testing.T) {
	cats := AllCategories()

	expected := []ModelCategory{
		CategoryAll,
		CategoryTextGeneration,
		CategoryEmbedding,
		CategoryImage,
		CategoryAudio,
		CategoryVideo,
		CategoryUtility,
	}

	require.Equal(t, expected, cats)
}

func TestModelMetadataClone_DeepClonesRankingPointers(t *testing.T) {
	elo := 1800.0
	rank := 5
	m := &ModelMetadata{
		Rankings: map[string]ModelRanking{
			"bench": {Elo: &elo, Rank: &rank, AsOf: "2026-01-01"},
		},
	}

	clone := m.Clone()

	// Mutate through the clone.
	*clone.Rankings["bench"].Elo = 0
	*clone.Rankings["bench"].Rank = 0

	assert.Equal(t, 1800.0, *m.Rankings["bench"].Elo)
	assert.Equal(t, 5, *m.Rankings["bench"].Rank)
}
