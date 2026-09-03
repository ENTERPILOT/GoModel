package core

import "testing"

func TestDuplicateSelectorField(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "unique selectors", body: `{"model":"gpt-5-mini","provider":"openai","stream":true}`, want: ""},
		{name: "no selectors", body: `{"messages":[]}`, want: ""},
		{name: "empty object", body: `{}`, want: ""},
		{name: "duplicate model", body: `{"model":"allowed","model":"blocked"}`, want: "model"},
		{name: "duplicate provider", body: `{"provider":"a","model":"m","provider":"b"}`, want: "provider"},
		{name: "duplicate stream", body: `{"stream":true,"stream":false}`, want: "stream"},
		{name: "first repeated field wins the report", body: `{"stream":true,"model":"a","stream":false,"model":"b"}`, want: "stream"},
		{name: "duplicate with escaped key", body: `{"model":"allowed","mod\u0065l":"blocked"}`, want: "model"},
		{name: "nested duplicates ignored", body: `{"model":"m","messages":[{"model":"a","model":"b"}],"x":{"stream":true,"stream":false}}`, want: ""},
		{name: "duplicate other fields ignored", body: `{"model":"m","n":1,"n":2}`, want: ""},
		{name: "pretty printed duplicate", body: "{\n  \"model\" : \"a\" ,\n  \"messages\": [ {\"x\": [1, 2]} ],\n  \"model\" : \"b\"\n}", want: "model"},
		{name: "values containing braces and escaped quotes", body: `{"model":"a{\"}","messages":[{"content":"[{]\\"}],"model":"b"}`, want: "model"},
		{name: "scalar values of every kind", body: `{"n":1.5e3,"b":true,"z":null,"stream":false,"stream":true}`, want: "stream"},
		{name: "array root", body: `[{"model":"a","model":"b"}]`, want: ""},
		{name: "not json", body: `nope`, want: ""},
		{name: "unterminated object", body: `{"model":"a","model":"b"`, want: "model"},
		{name: "unterminated string", body: `{"model":"a`, want: ""},
		{name: "unterminated nested value", body: `{"model":"a","x":[1,2`, want: ""},
		{name: "missing colon", body: `{"model" "a"}`, want: ""},
		{name: "empty body", body: ``, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DuplicateSelectorField([]byte(tt.body)); got != tt.want {
				t.Fatalf("DuplicateSelectorField(%s) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestNewDuplicateSelectorFieldError(t *testing.T) {
	err := NewDuplicateSelectorFieldError("model")
	if err.StatusCode != 400 || err.Type != ErrorTypeInvalidRequest {
		t.Fatalf("error = %+v, want 400 invalid_request", err)
	}
	if want := `duplicate top-level "model" field in request body`; err.Message != want {
		t.Fatalf("message = %q, want %q", err.Message, want)
	}
}
