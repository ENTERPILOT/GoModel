package core

import (
	"encoding/json"
	"testing"
)

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
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, want nil", err)
	}

	if msg.Content != nil {
		t.Fatalf("Content = %#v, want nil", msg.Content)
	}
	if !msg.ContentNull {
		t.Fatal("ContentNull = false, want true")
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].ID != "call_123" {
		t.Fatalf("ToolCalls[0].ID = %q, want call_123", msg.ToolCalls[0].ID)
	}
}

func TestMessageUnmarshalJSON_PreservesStringContent(t *testing.T) {
	payload := []byte(`{"role":"assistant","content":"hello"}`)

	var msg Message
	if err := json.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, want nil", err)
	}

	if msg.Content != "hello" {
		t.Fatalf("Content = %q, want hello", msg.Content)
	}
	if msg.ContentNull {
		t.Fatal("ContentNull = true, want false")
	}
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
	if err != nil {
		t.Fatalf("json.Marshal() error = %v, want nil", err)
	}

	if string(payload) != `{"role":"assistant","content":null,"tool_calls":[{"id":"call_123","type":"function","function":{"name":"lookup_weather","arguments":"{\"city\":\"Warsaw\"}"}}]}` {
		t.Fatalf("json.Marshal() = %s", payload)
	}
}

func TestMessageMarshalJSON_ContentWinsOverContentNull(t *testing.T) {
	payload, err := json.Marshal(Message{
		Role:        "assistant",
		Content:     "hello",
		ContentNull: true,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v, want nil", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if raw["content"] != "hello" {
		t.Fatalf("content = %v, want \"hello\"", raw["content"])
	}
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
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if resp.SystemFingerprint != "fp_abc123" {
		t.Fatalf("SystemFingerprint = %q, want fp_abc123", resp.SystemFingerprint)
	}

	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded["system_fingerprint"] != "fp_abc123" {
		t.Fatalf("decoded system_fingerprint = %#v, want fp_abc123", decoded["system_fingerprint"])
	}
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
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if len(resp.Choices) != 1 {
		t.Fatalf("len(Choices) = %d, want 1", len(resp.Choices))
	}
	if string(resp.Choices[0].Logprobs) == "" {
		t.Fatal("Choices[0].Logprobs = empty, want preserved payload")
	}

	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	choices, ok := decoded["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("decoded choices = %#v, want len=1", decoded["choices"])
	}

	choice, ok := choices[0].(map[string]any)
	if !ok {
		t.Fatalf("decoded choice = %#v, want object", choices[0])
	}

	logprobs, ok := choice["logprobs"].(map[string]any)
	if !ok {
		t.Fatalf("decoded logprobs = %#v, want object", choice["logprobs"])
	}

	content, ok := logprobs["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("decoded logprobs.content = %#v, want len=1", logprobs["content"])
	}
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

	if !streamReq.Stream {
		t.Fatal("Stream should be true")
	}
	if len(streamReq.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(streamReq.Tools))
	}
	if streamReq.ToolChoice == nil {
		t.Fatal("ToolChoice should not be nil")
	}
	if streamReq.ParallelToolCalls == nil || *streamReq.ParallelToolCalls {
		t.Fatal("ParallelToolCalls should be false")
	}
	if len(streamReq.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(streamReq.Messages))
	}
	if len(streamReq.Messages[0].ToolCalls) != 1 || streamReq.Messages[0].ToolCalls[0].ID != "call_123" {
		t.Fatalf("assistant tool_calls = %+v, want call_123", streamReq.Messages[0].ToolCalls)
	}
	if streamReq.Messages[1].ToolCallID != "call_123" {
		t.Fatalf("tool message ToolCallID = %q, want call_123", streamReq.Messages[1].ToolCallID)
	}
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
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if req.ExtraFields["response_format"] == nil {
		t.Fatalf("response_format missing from ExtraFields: %+v", req.ExtraFields)
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	responseFormat, ok := decoded["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("decoded response_format = %#v, want object", decoded["response_format"])
	}
	if responseFormat["type"] != "json_schema" {
		t.Fatalf("decoded response_format.type = %#v, want json_schema", responseFormat["type"])
	}
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
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
	}
	if req.Messages[0].ExtraFields["name"] == nil {
		t.Fatalf("message[0].name missing from ExtraFields: %+v", req.Messages[0].ExtraFields)
	}
	parts, ok := req.Messages[0].Content.([]ContentPart)
	if !ok || len(parts) != 1 {
		t.Fatalf("message[0].content = %#v, want []ContentPart len=1", req.Messages[0].Content)
	}
	if parts[0].ExtraFields["cache_control"] == nil {
		t.Fatalf("content part cache_control missing from ExtraFields: %+v", parts[0].ExtraFields)
	}
	if len(req.Messages[1].ToolCalls) != 1 {
		t.Fatalf("len(message[1].ToolCalls) = %d, want 1", len(req.Messages[1].ToolCalls))
	}
	if req.Messages[1].ToolCalls[0].ExtraFields["vendor_data"] == nil {
		t.Fatalf("tool_call vendor_data missing from ExtraFields: %+v", req.Messages[1].ToolCalls[0].ExtraFields)
	}
	if req.Messages[1].ToolCalls[0].Function.ExtraFields["strict"] == nil {
		t.Fatalf("function strict missing from ExtraFields: %+v", req.Messages[1].ToolCalls[0].Function.ExtraFields)
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	messages, ok := decoded["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("decoded messages = %#v, want []any len=2", decoded["messages"])
	}
	firstMsg, ok := messages[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0] = %#v, want object", messages[0])
	}
	if firstMsg["name"] != "alice" {
		t.Fatalf("messages[0].name = %#v, want alice", firstMsg["name"])
	}
	firstContent, ok := firstMsg["content"].([]any)
	if !ok || len(firstContent) != 1 {
		t.Fatalf("messages[0].content = %#v, want []any len=1", firstMsg["content"])
	}
	firstPart, ok := firstContent[0].(map[string]any)
	if !ok {
		t.Fatalf("messages[0].content[0] = %#v, want object", firstContent[0])
	}
	if _, ok := firstPart["cache_control"].(map[string]any); !ok {
		t.Fatalf("messages[0].content[0].cache_control = %#v, want object", firstPart["cache_control"])
	}

	secondMsg, ok := messages[1].(map[string]any)
	if !ok {
		t.Fatalf("messages[1] = %#v, want object", messages[1])
	}
	toolCalls, ok := secondMsg["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("messages[1].tool_calls = %#v, want []any len=1", secondMsg["tool_calls"])
	}
	toolCall, ok := toolCalls[0].(map[string]any)
	if !ok {
		t.Fatalf("tool_calls[0] = %#v, want object", toolCalls[0])
	}
	if _, ok := toolCall["vendor_data"].(map[string]any); !ok {
		t.Fatalf("tool_calls[0].vendor_data = %#v, want object", toolCall["vendor_data"])
	}
	function, ok := toolCall["function"].(map[string]any)
	if !ok {
		t.Fatalf("tool_calls[0].function = %#v, want object", toolCall["function"])
	}
	if function["strict"] != true {
		t.Fatalf("tool_calls[0].function.strict = %#v, want true", function["strict"])
	}
}

func TestEmbeddingRequestJSON_PreservesUnknownFields(t *testing.T) {
	payload := []byte(`{
		"model":"text-embedding-3-small",
		"input":"hello",
		"user":"tenant-123"
	}`)

	var req EmbeddingRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if req.ExtraFields["user"] == nil {
		t.Fatalf("user missing from ExtraFields: %+v", req.ExtraFields)
	}

	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded["user"] != "tenant-123" {
		t.Fatalf("decoded user = %#v, want tenant-123", decoded["user"])
	}
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

	if !streamReq.Stream {
		t.Fatal("Stream should be true")
	}
	if len(streamReq.Tools) != 1 {
		t.Fatalf("len(Tools) = %d, want 1", len(streamReq.Tools))
	}
	if streamReq.ToolChoice == nil {
		t.Fatal("ToolChoice should not be nil")
	}
	if streamReq.ParallelToolCalls == nil || *streamReq.ParallelToolCalls {
		t.Fatal("ParallelToolCalls should be false")
	}
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
			got := CategoriesForModes(tt.modes)
			if len(got) != len(tt.want) {
				t.Fatalf("CategoriesForModes(%v) returned %d categories, want %d", tt.modes, len(got), len(tt.want))
			}
			for i, c := range got {
				if c != tt.want[i] {
					t.Errorf("CategoriesForModes(%v)[%d] = %q, want %q", tt.modes, i, c, tt.want[i])
				}
			}
		})
	}
}

func TestCategoriesForModes_MultiMode(t *testing.T) {
	cats := CategoriesForModes([]string{"chat", "image_generation", "audio_speech"})
	want := []ModelCategory{CategoryTextGeneration, CategoryImage, CategoryAudio}
	if len(cats) != len(want) {
		t.Fatalf("got %d categories, want %d", len(cats), len(want))
	}
	for i, c := range cats {
		if c != want[i] {
			t.Errorf("[%d] = %q, want %q", i, c, want[i])
		}
	}
}

func TestCategoriesForModes_Dedup(t *testing.T) {
	// "chat" and "completion" both map to text_generation — should deduplicate
	cats := CategoriesForModes([]string{"chat", "completion"})
	if len(cats) != 1 {
		t.Fatalf("got %d categories, want 1 (deduped)", len(cats))
	}
	if cats[0] != CategoryTextGeneration {
		t.Errorf("got %q, want %q", cats[0], CategoryTextGeneration)
	}
}

func TestCategoriesForModes_UnknownMode(t *testing.T) {
	cats := CategoriesForModes([]string{"unknown_mode"})
	if len(cats) != 0 {
		t.Errorf("CategoriesForModes([\"unknown_mode\"]) = %v, want empty", cats)
	}
}

func TestCategoriesForModes_Empty(t *testing.T) {
	cats := CategoriesForModes(nil)
	if len(cats) != 0 {
		t.Errorf("CategoriesForModes(nil) = %v, want empty", cats)
	}
	cats = CategoriesForModes([]string{})
	if len(cats) != 0 {
		t.Errorf("CategoriesForModes([]) = %v, want empty", cats)
	}
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

	if len(cats) != len(expected) {
		t.Fatalf("AllCategories() returned %d categories, want %d", len(cats), len(expected))
	}

	for i, cat := range cats {
		if cat != expected[i] {
			t.Errorf("AllCategories()[%d] = %q, want %q", i, cat, expected[i])
		}
	}
}
