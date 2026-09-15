package deepseek

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestAdaptCompatibility_PadsEveryAssistantTurnWhenToolsPresent(t *testing.T) {
	var req core.ChatRequest
	if err := json.Unmarshal([]byte(`{
		"model":"deepseek-v4-flash",
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"hello"},
			{"role":"user","content":"check"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"assistant","content":"done","reasoning_content":"client reasoning"}
		],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	adapted, err := AdaptCompatibility(&req, "deepseek", JSONSchemaDowngrade)
	if err != nil {
		t.Fatalf("AdaptCompatibility() error = %v", err)
	}

	want := map[int]string{1: `" "`, 3: `" "`, 5: `"client reasoning"`}
	for i, message := range adapted.Messages {
		if got := string(message.ExtraFields.Lookup("reasoning_content")); got != want[i] {
			t.Errorf("messages[%d] (%s) reasoning_content = %q, want %q", i, message.Role, got, want[i])
		}
	}
	if req.Messages[1].ExtraFields.Lookup("reasoning_content") != nil {
		t.Fatal("AdaptCompatibility() mutated the caller's request")
	}
}

func TestAdaptCompatibility_JSONSchemaResponseFormat(t *testing.T) {
	schemaFormat := `{"type":"json_schema","json_schema":{"name":"weather","description":"Current weather","strict":true,"schema":{"type": "object", "properties": {"city": {"type": "string"}}}}}`

	tests := []struct {
		name            string
		responseFormat  string
		mode            JSONSchemaMode
		wantFormat      string
		wantInstruction []string
		wantErr         bool
	}{
		{
			name:           "downgrades json_schema with the schema as an instruction",
			responseFormat: schemaFormat,
			mode:           JSONSchemaDowngrade,
			wantFormat:     `{"type":"json_object"}`,
			wantInstruction: []string{
				"valid JSON object that conforms to the JSON schema below (weather).",
				"Schema description: Current weather",
				`{"type":"object","properties":{"city":{"type":"string"}}}`,
			},
		},
		{
			name:            "downgrades json_schema without a schema",
			responseFormat:  `{"type":"json_schema","json_schema":{}}`,
			mode:            JSONSchemaDowngrade,
			wantFormat:      `{"type":"json_object"}`,
			wantInstruction: []string{"Respond only with a valid JSON object."},
		},
		{
			name:           "leaves json_object untouched",
			responseFormat: `{"type":"json_object"}`,
			mode:           JSONSchemaDowngrade,
			wantFormat:     `{"type":"json_object"}`,
		},
		{
			name:           "rejects json_schema in error mode",
			responseFormat: schemaFormat,
			mode:           JSONSchemaError,
			wantErr:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{
				Model: "deepseek-v4-flash",
				Messages: []core.Message{
					{Role: "system", Content: "Be brief."},
					{Role: "user", Content: "weather?"},
				},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"response_format": json.RawMessage(tt.responseFormat),
				}),
			}

			adapted, err := AdaptCompatibility(req, "opencode_go", tt.mode)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), `opencode_go model "deepseek-v4-flash"`) {
					t.Fatalf("AdaptCompatibility() error = %v, want error naming provider and model", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("AdaptCompatibility() error = %v", err)
			}
			if got := string(adapted.ExtraFields.Lookup("response_format")); got != tt.wantFormat {
				t.Fatalf("response_format = %s, want %s", got, tt.wantFormat)
			}
			if tt.wantInstruction == nil {
				if adapted != req {
					t.Fatal("AdaptCompatibility() copied an unchanged request")
				}
				return
			}

			roles := make([]string, 0, len(adapted.Messages))
			for _, message := range adapted.Messages {
				roles = append(roles, message.Role)
			}
			if strings.Join(roles, ",") != "system,system,user" {
				t.Fatalf("roles = %v, want instruction after the leading system message", roles)
			}
			instruction := core.ExtractTextContent(adapted.Messages[1].Content)
			for _, want := range tt.wantInstruction {
				if !strings.Contains(instruction, want) {
					t.Errorf("instruction = %q, want it to contain %q", instruction, want)
				}
			}
			if len(req.Messages) != 2 || string(req.ExtraFields.Lookup("response_format")) != tt.responseFormat {
				t.Fatal("AdaptCompatibility() mutated the caller's request")
			}
		})
	}
}

func TestResponses_DowngradesJSONSchemaTextFormat(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-deepseek",
			"created":1,
			"model":"deepseek-v4-flash",
			"choices":[{"index":0,"message":{"role":"assistant","content":"{\"city\":\"Warsaw\"}"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	var req core.ResponsesRequest
	if err := json.Unmarshal([]byte(`{
		"model":"deepseek-v4-flash",
		"input":"weather?",
		"text":{"format":{"type":"json_schema","name":"weather","schema":{"type":"object"}}}
	}`), &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{})
	if _, err := provider.Responses(context.Background(), &req); err != nil {
		t.Fatalf("Responses() error = %v", err)
	}

	format, _ := gotBody["response_format"].(map[string]any)
	if format["type"] != "json_object" {
		t.Fatalf("response_format = %#v, want json_object", gotBody["response_format"])
	}
	messages, _ := gotBody["messages"].([]any)
	first, _ := messages[0].(map[string]any)
	if first["role"] != "system" || !strings.Contains(first["content"].(string), `{"type":"object"}`) {
		t.Fatalf("messages[0] = %#v, want schema instruction", first)
	}
}

func TestIsModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "deepseek-v4-pro", want: true},
		{model: "deepseek-v4.1-flash", want: true},
		{model: "opencode_go/deepseek-v4.1-flash", want: true},
		{model: "deepseek/DeepSeek-R1", want: true},
		{model: "glm-5.1", want: false},
		{model: "deepseek/glm-5.1", want: false},
		{model: "", want: false},
	}
	for _, tt := range tests {
		if got := IsModel(tt.model); got != tt.want {
			t.Errorf("IsModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestLoadJSONSchemaMode(t *testing.T) {
	tests := []struct {
		value string
		want  JSONSchemaMode
	}{
		{value: "", want: JSONSchemaDowngrade},
		{value: "downgrade", want: JSONSchemaDowngrade},
		{value: " ERROR ", want: JSONSchemaError},
		{value: "strict", want: JSONSchemaDowngrade},
	}
	for _, tt := range tests {
		t.Setenv(jsonSchemaModeEnvVar, tt.value)
		if got := LoadJSONSchemaMode(); got != tt.want {
			t.Errorf("LoadJSONSchemaMode() with %q = %q, want %q", tt.value, got, tt.want)
		}
	}
}
