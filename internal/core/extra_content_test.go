package core

import (
	"testing"

	"github.com/goccy/go-json"
)

func extraContentFields(t *testing.T, raw string) UnknownJSONFields {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &members); err != nil {
		t.Fatal(err)
	}
	return UnknownJSONFieldsFromMap(members)
}

func TestUnknownJSONFields_ExtraContent(t *testing.T) {
	tests := []struct {
		name   string
		fields string
		vendor string
		want   string
	}{
		{name: "present", fields: `{"extra_content":{"google":{"thought_signature":"sig"}}}`, vendor: "google", want: `{"thought_signature":"sig"}`},
		{name: "other vendor", fields: `{"extra_content":{"google":{"thought_signature":"sig"}}}`, vendor: "anthropic", want: ""},
		{name: "absent", fields: `{"x":1}`, vendor: "google", want: ""},
		{name: "not an object", fields: `{"extra_content":"junk"}`, vendor: "google", want: ""},
		{name: "null", fields: `{"extra_content":null}`, vendor: "google", want: ""},
		{name: "empty fields", fields: `{}`, vendor: "google", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(extraContentFields(t, tt.fields).ExtraContent(tt.vendor))
			if got != tt.want {
				t.Errorf("ExtraContent(%q) = %s, want %s", tt.vendor, got, tt.want)
			}
		})
	}
}

func TestUnknownJSONFields_WithExtraContent(t *testing.T) {
	tests := []struct {
		name   string
		fields string
		vendor string
		value  string
		want   string
	}{
		{name: "empty fields", fields: `{}`, vendor: "google", value: `{"a":1}`, want: `{"extra_content":{"google":{"a":1}}}`},
		{name: "keeps other members", fields: `{"cache_control":{"type":"ephemeral"}}`, vendor: "anthropic", value: `{"is_error":true}`, want: `{"cache_control":{"type":"ephemeral"},"extra_content":{"anthropic":{"is_error":true}}}`},
		{name: "keeps other vendors", fields: `{"extra_content":{"google":{"a":1}}}`, vendor: "anthropic", value: `{"b":2}`, want: `{"extra_content":{"anthropic":{"b":2},"google":{"a":1}}}`},
		{name: "replaces same vendor", fields: `{"extra_content":{"google":{"a":1}}}`, vendor: "google", value: `{"a":2}`, want: `{"extra_content":{"google":{"a":2}}}`},
		{name: "replaces non-object member", fields: `{"extra_content":"junk"}`, vendor: "google", value: `{"a":1}`, want: `{"extra_content":{"google":{"a":1}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extraContentFields(t, tt.fields).WithExtraContent(tt.vendor, json.RawMessage(tt.value))
			if err != nil {
				t.Fatal(err)
			}
			assertSameJSON(t, got, tt.want)
		})
	}
}

func TestUnknownJSONFields_WithoutForeignExtraContent(t *testing.T) {
	tests := []struct {
		name   string
		fields string
		keep   string
		want   string
	}{
		{name: "no member", fields: `{"x":1}`, keep: "google", want: `{"x":1}`},
		{name: "keeps own vendor", fields: `{"extra_content":{"google":{"a":1}},"x":1}`, keep: "google", want: `{"extra_content":{"google":{"a":1}},"x":1}`},
		{name: "drops foreign vendor", fields: `{"extra_content":{"google":{"a":1}},"x":1}`, keep: "anthropic", want: `{"x":1}`},
		{name: "drops everything for stateless provider", fields: `{"extra_content":{"google":{"a":1}}}`, keep: "", want: `{}`},
		{name: "keeps own among several", fields: `{"extra_content":{"google":{"a":1},"anthropic":{"b":2}}}`, keep: "anthropic", want: `{"extra_content":{"anthropic":{"b":2}}}`},
		{name: "drops non-object member", fields: `{"extra_content":"junk","x":1}`, keep: "google", want: `{"x":1}`},
		{name: "drops null member", fields: `{"extra_content":null}`, keep: "google", want: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := extraContentFields(t, tt.fields)
			got := fields.WithoutForeignExtraContent(tt.keep)
			assertSameJSON(t, got, tt.want)
			if has, changed := fields.HasForeignExtraContent(tt.keep), tt.fields != tt.want; has != changed {
				t.Errorf("HasForeignExtraContent(%q) = %v, want %v", tt.keep, has, changed)
			}
		})
	}
}

func assertSameJSON(t *testing.T, fields UnknownJSONFields, want string) {
	t.Helper()
	raw := fields.raw
	if fields.IsEmpty() {
		raw = []byte("{}")
	}
	var got, expected any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("fields %s: %v", raw, err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(expected)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("fields = %s, want %s", gotJSON, wantJSON)
	}
}
