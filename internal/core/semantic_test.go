package core

import "testing"

func TestBuildSemanticEnvelope_OpenAICompat(t *testing.T) {
	frame := &IngressFrame{
		Method:      "POST",
		Path:        "/v1/chat/completions",
		ContentType: "application/json",
		RawBody: []byte(`{
			"model":"gpt-5-mini",
			"provider":"openai",
			"messages":[{"role":"user","content":"hello"}],
			"response_format":{"type":"json_schema"}
		}`),
	}

	env := BuildSemanticEnvelope(frame)
	if env == nil {
		t.Fatal("BuildSemanticEnvelope() = nil")
	}
	if env.Dialect != "openai_compat" {
		t.Fatalf("Dialect = %q, want openai_compat", env.Dialect)
	}
	if env.Operation != "chat_completions" {
		t.Fatalf("Operation = %q, want chat_completions", env.Operation)
	}
	if !env.JSONBodyParsed {
		t.Fatal("JSONBodyParsed = false, want true")
	}
	if env.SelectorHints.Model != "gpt-5-mini" {
		t.Fatalf("SelectorHints.Model = %q, want gpt-5-mini", env.SelectorHints.Model)
	}
	if env.SelectorHints.Provider != "openai" {
		t.Fatalf("SelectorHints.Provider = %q, want openai", env.SelectorHints.Provider)
	}
	if len(env.OpaqueJSONFields) != 0 {
		t.Fatalf("OpaqueJSONFields = %+v, want empty", env.OpaqueJSONFields)
	}
}

func TestBuildSemanticEnvelope_InvalidJSONRemainsPartial(t *testing.T) {
	frame := &IngressFrame{
		Method:      "POST",
		Path:        "/v1/responses",
		ContentType: "application/json",
		RawBody:     []byte(`{invalid}`),
	}

	env := BuildSemanticEnvelope(frame)
	if env == nil {
		t.Fatal("BuildSemanticEnvelope() = nil")
	}
	if env.Dialect != "openai_compat" {
		t.Fatalf("Dialect = %q, want openai_compat", env.Dialect)
	}
	if env.Operation != "responses" {
		t.Fatalf("Operation = %q, want responses", env.Operation)
	}
	if env.JSONBodyParsed {
		t.Fatal("JSONBodyParsed = true, want false")
	}
	if env.SelectorHints.Model != "" {
		t.Fatalf("SelectorHints.Model = %q, want empty", env.SelectorHints.Model)
	}
}

func TestBuildSemanticEnvelope_PassthroughRouteParams(t *testing.T) {
	frame := &IngressFrame{
		Method:      "POST",
		Path:        "/p/openai/responses",
		RouteParams: map[string]string{"provider": "openai", "endpoint": "responses"},
		RawBody:     []byte(`{"model":"gpt-5-mini","foo":"bar"}`),
	}

	env := BuildSemanticEnvelope(frame)
	if env == nil {
		t.Fatal("BuildSemanticEnvelope() = nil")
	}
	if env.Dialect != "provider_passthrough" {
		t.Fatalf("Dialect = %q, want provider_passthrough", env.Dialect)
	}
	if env.Operation != "provider_passthrough" {
		t.Fatalf("Operation = %q, want provider_passthrough", env.Operation)
	}
	if env.SelectorHints.Provider != "openai" {
		t.Fatalf("SelectorHints.Provider = %q, want openai", env.SelectorHints.Provider)
	}
	if env.SelectorHints.Endpoint != "responses" {
		t.Fatalf("SelectorHints.Endpoint = %q, want responses", env.SelectorHints.Endpoint)
	}
	if env.SelectorHints.Model != "gpt-5-mini" {
		t.Fatalf("SelectorHints.Model = %q, want gpt-5-mini", env.SelectorHints.Model)
	}
	if len(env.OpaqueJSONFields) != 0 {
		t.Fatalf("OpaqueJSONFields = %+v, want empty", env.OpaqueJSONFields)
	}
}

func TestBuildSemanticEnvelope_SkipsBodyParsingWhenIngressBodyWasNotCaptured(t *testing.T) {
	frame := &IngressFrame{
		Method:          "POST",
		Path:            "/v1/chat/completions",
		RawBodyTooLarge: true,
	}

	env := BuildSemanticEnvelope(frame)
	if env == nil {
		t.Fatal("BuildSemanticEnvelope() = nil")
	}
	if env.JSONBodyParsed {
		t.Fatal("JSONBodyParsed = true, want false")
	}
	if env.SelectorHints.Model != "" {
		t.Fatalf("SelectorHints.Model = %q, want empty", env.SelectorHints.Model)
	}
}
