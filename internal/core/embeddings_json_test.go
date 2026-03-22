package core

import (
	"encoding/json"
	"testing"
)

func TestEmbeddingRequestJSON_RoundTripPreservesUnknownFields(t *testing.T) {
	body := []byte(`{
		"model":"text-embedding-3-small",
		"provider":"openai",
		"input":["hello","world"],
		"encoding_format":"float",
		"dimensions":256,
		"x_trace":{"id":"trace-1"},
		"x_mode":"keep-me"
	}`)

	wantExtra, err := extractUnknownJSONFields(body,
		"model",
		"provider",
		"input",
		"encoding_format",
		"dimensions",
	)
	if err != nil {
		t.Fatalf("extractUnknownJSONFields() error = %v", err)
	}

	var req EmbeddingRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if req.Model != "text-embedding-3-small" {
		t.Fatalf("Model = %q, want text-embedding-3-small", req.Model)
	}
	if req.Provider != "openai" {
		t.Fatalf("Provider = %q, want openai", req.Provider)
	}
	input, ok := req.Input.([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("Input = %#v, want len=2", req.Input)
	}
	if req.EncodingFormat != "float" {
		t.Fatalf("EncodingFormat = %q, want float", req.EncodingFormat)
	}
	if req.Dimensions == nil || *req.Dimensions != 256 {
		t.Fatalf("Dimensions = %#v, want 256", req.Dimensions)
	}
	traceField := lookupUnknownField(t, req.ExtraFields, "x_trace")
	if string(traceField) != string(wantExtra.Lookup("x_trace")) {
		t.Fatalf("ExtraFields[x_trace] = %s, want %s", traceField, wantExtra.Lookup("x_trace"))
	}
	modeField := lookupUnknownField(t, req.ExtraFields, "x_mode")
	if string(modeField) != string(wantExtra.Lookup("x_mode")) {
		t.Fatalf("ExtraFields[x_mode] = %s, want %s", modeField, wantExtra.Lookup("x_mode"))
	}

	roundTrip, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(roundTrip, &decoded); err != nil {
		t.Fatalf("json.Unmarshal(roundTrip) error = %v", err)
	}
	xTraceMap, ok := decoded["x_trace"].(map[string]any)
	if !ok {
		t.Fatalf("x_trace = %#v, want object", decoded["x_trace"])
	}
	if xTraceMap["id"] != "trace-1" {
		t.Fatalf("x_trace.id = %#v, want trace-1", xTraceMap["id"])
	}
	if decoded["x_mode"] != "keep-me" {
		t.Fatalf("x_mode = %#v, want keep-me", decoded["x_mode"])
	}
}
