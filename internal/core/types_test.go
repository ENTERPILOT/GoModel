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
