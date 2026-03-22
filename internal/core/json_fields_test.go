package core

import (
	"bytes"
	"encoding/json"
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
