package core

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
)

func TestExtractUnknownJSONFieldsObjectByScan_PreservesNestedValues(t *testing.T) {
	data := []byte(`{
		"known":"value",
		"x_object":{"nested":[1,{"ok":true}],"text":"hello"},
		"x_array":[{"type":"text","text":"hi"}],
		"x_bool":true
	}`)

	fields, err := extractUnknownJSONFieldsObjectByScan(data, "known")
	if err != nil {
		t.Fatalf("extractUnknownJSONFieldsObjectByScan() error = %v", err)
	}

	if fields.IsEmpty() {
		t.Fatal("expected unknown fields")
	}
	if got := fields.Lookup("x_bool"); !bytes.Equal(got, []byte("true")) {
		t.Fatalf("x_bool = %s, want true", got)
	}

	var nested map[string]any
	if err := json.Unmarshal(fields.Lookup("x_object"), &nested); err != nil {
		t.Fatalf("failed to unmarshal x_object: %v", err)
	}
	if nested["text"] != "hello" {
		t.Fatalf("x_object.text = %#v, want hello", nested["text"])
	}
}

func TestExtractUnknownJSONFieldsObjectByScan_HandlesEscapedStrings(t *testing.T) {
	data := []byte(`{
		"model":"gpt-5-mini",
		"x_text":"quote: \"ok\" and slash \\\\",
		"x_json":"{\"embedded\":true}"
	}`)

	fields, err := extractUnknownJSONFieldsObjectByScan(data, "model")
	if err != nil {
		t.Fatalf("extractUnknownJSONFieldsObjectByScan() error = %v", err)
	}

	if got := fields.Lookup("x_text"); !bytes.Equal(got, []byte(`"quote: \"ok\" and slash \\\\"`)) {
		t.Fatalf("x_text = %s", got)
	}
	if got := fields.Lookup("x_json"); !bytes.Equal(got, []byte(`"{\"embedded\":true}"`)) {
		t.Fatalf("x_json = %s", got)
	}
}

func TestUnknownJSONFieldsFromMap_EmptyRawValueEncodesAsNull(t *testing.T) {
	fields := UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"x_nil": nil,
		"x_set": json.RawMessage(`true`),
	})

	if got := fields.Lookup("x_nil"); !bytes.Equal(got, []byte("null")) {
		t.Fatalf("x_nil = %q, want null", got)
	}
	if got := fields.Lookup("x_set"); !bytes.Equal(got, []byte("true")) {
		t.Fatalf("x_set = %q, want true", got)
	}
}

func TestMergeUnknownJSONFields(t *testing.T) {
	first := UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"x_first":  json.RawMessage(`true`),
		"x_shared": json.RawMessage(`"first"`),
	})
	second := UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"x_second": json.RawMessage(`{"ok":true}`),
		"x_shared": json.RawMessage(`"second"`),
	})

	merged := MergeUnknownJSONFields(first, second)

	if got := merged.Lookup("x_first"); !bytes.Equal(got, []byte("true")) {
		t.Fatalf("x_first = %s, want true", got)
	}
	if got := merged.Lookup("x_second"); !bytes.Equal(got, []byte(`{"ok":true}`)) {
		t.Fatalf("x_second = %s, want object", got)
	}
	if got := merged.Lookup("x_shared"); !bytes.Equal(got, []byte(`"second"`)) {
		t.Fatalf("x_shared = %s, want later override", got)
	}
}

func TestExtractUnknownJSONFieldsObjectByScan_RejectsInvalidJSONSyntax(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid bare literal", body: `{"known":"value","x":wat}`},
		{name: "missing object comma", body: `{"known":"value" "x":1}`},
		{name: "trailing object comma", body: `{"known":"value","x":1,}`},
		{name: "trailing array comma", body: `{"known":"value","x":[1,]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := extractUnknownJSONFieldsObjectByScan([]byte(tt.body), "known"); err == nil {
				t.Fatalf("extractUnknownJSONFieldsObjectByScan(%q) error = nil, want syntax error", tt.body)
			}
		})
	}
}

func TestMergedJSONObjectCap_Overflow(t *testing.T) {
	if _, err := mergedJSONObjectCap(math.MaxInt, 2); err == nil {
		t.Fatal("mergedJSONObjectCap() error = nil, want overflow error")
	}
}
