package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseGeminiModelMethodPath(t *testing.T) {
	tests := []struct {
		path   string
		model  string
		method string
		ok     bool
	}{
		{path: "/v1beta/models/gemini-2.5-flash:generateContent", model: "gemini-2.5-flash", method: "generateContent", ok: true},
		{path: "/v1beta/models/openai/gpt-4o:streamGenerateContent", model: "openai/gpt-4o", method: "streamGenerateContent", ok: true},
		// A model ID may contain colons itself; the method follows the last one.
		{path: "/v1beta/models/ollama/llama3:8b:generateContent", model: "ollama/llama3:8b", method: "generateContent", ok: true},
		{path: "/v1beta/models/gemini-2.5-flash:countTokens", model: "gemini-2.5-flash", method: "countTokens", ok: true},
		{path: "/v1beta/models/gemini-2.5-flash"},
		{path: "/v1beta/models/:generateContent"},
		{path: "/v1beta/models/gemini-2.5-flash:"},
		{path: "/v1beta/models"},
		{path: "/v1/models/gemini-2.5-flash:generateContent"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			model, method, ok := ParseGeminiModelMethodPath(tt.path)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.model, model)
			assert.Equal(t, tt.method, method)
		})
	}
}

func TestUsageEndpoint(t *testing.T) {
	tests := map[string]string{
		"/v1/chat/completions":                               "/v1/chat/completions",
		"/v1/responses/resp_1":                               "/v1/responses/resp_1",
		"/v1beta/models/gemini-2.5-flash:generateContent":    "/v1beta/models/{model}:generateContent",
		"/v1beta/models/openai/gpt-4o:streamGenerateContent": "/v1beta/models/{model}:streamGenerateContent",
		"/v1beta/models/gemini-2.5-flash:countTokens":        "/v1beta/models/gemini-2.5-flash:countTokens",
		"/v1beta/models/gemini-2.5-flash":                    "/v1beta/models/gemini-2.5-flash",
	}
	for path, want := range tests {
		assert.Equal(t, want, UsageEndpoint(path), path)
	}
}

func TestIsGeminiAPIPath(t *testing.T) {
	assert.True(t, IsGeminiAPIPath("/v1beta/models"))
	assert.True(t, IsGeminiAPIPath("/v1beta/models/gemini-2.5-flash:generateContent"))
	assert.False(t, IsGeminiAPIPath("/v1/models"))
	assert.False(t, IsGeminiAPIPath("/v1beta"))
}
