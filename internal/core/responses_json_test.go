package core

import (
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

	toolChoice, ok := req.ToolChoice.(map[string]interface{})
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

func TestResponsesRequestMarshalJSON_PreservesInput(t *testing.T) {
	body, err := json.Marshal(ResponsesRequest{
		Model: "gpt-4o-mini",
		Input: []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
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

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	inputRaw, ok := decoded["input"]
	if !ok {
		t.Fatalf("marshal output missing input: %s", string(body))
	}

	input, ok := inputRaw.([]interface{})
	if !ok || len(input) != 1 {
		t.Fatalf("decoded input = %#v, want []interface{} len=1", inputRaw)
	}

	firstMsg, ok := input[0].(map[string]interface{})
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
	content, ok := contentRaw.([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("first input content = %#v, want []interface{} len=1", contentRaw)
	}

	firstPart, ok := content[0].(map[string]interface{})
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

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	toolChoice, ok := decoded["tool_choice"].(map[string]interface{})
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

	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	input, ok := decoded["input"].([]interface{})
	if !ok || len(input) != 1 {
		t.Fatalf("decoded input = %#v, want []interface{} len=1", decoded["input"])
	}

	first, ok := input[0].(map[string]interface{})
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

func TestResponsesRequestJSON_PreservesUnknownFields(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model":"gpt-5-mini",
		"input":"hello",
		"text":{
			"format":{
				"type":"json_schema",
				"name":"answer"
			}
		}
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if req.ExtraFields["text"] == nil {
		t.Fatalf("text missing from ExtraFields: %+v", req.ExtraFields)
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

	var decoded map[string]interface{}
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

	var decoded map[string]interface{}
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
