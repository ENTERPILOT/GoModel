package core

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOperationPathsMatchDescribeEndpoint(t *testing.T) {
	for op, paths := range operationPaths {
		for _, path := range paths.Exact {
			assert.Equal(t, op, DescribeEndpointPath(path).Operation, path)
		}
		for _, prefix := range paths.Prefixes {
			assert.Equal(t, op, DescribeEndpointPath(prefix+"/x").Operation, prefix+"/x")
		}
	}
}

func TestOperationPathsCoverClassifiedPaths(t *testing.T) {
	paths := []string{
		"/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens",
		"/v1/responses", "/v1/responses/resp_1/input_items", "/v1/conversations/conv_1",
		"/v1/embeddings", "/v1/batches/b_1/cancel", "/v1/messages/batches/b_1",
		"/v1/files/f_1/content", "/v1/audio/speech", "/v1/audio/transcriptions",
		"/v1/audio/translations", "/v1/images/generations", "/v1/images/edits",
		"/v1/realtime", "/v1/realtime/translations/calls", "/mcp", "/mcp/github",
		"/p/openai/v1/models",
	}
	for _, path := range paths {
		want := DescribeEndpointPath(path).Operation
		require.NotEmpty(t, want, path)
		rule, ok := PathsForOperation(want)
		require.True(t, ok, path)
		assert.True(t, rule.matches(path), path)
	}
}

func (p OperationPaths) matches(path string) bool {
	if slices.Contains(p.Exact, path) {
		return true
	}
	for _, prefix := range p.Prefixes {
		if path == prefix || len(path) > len(prefix) && path[:len(prefix)+1] == prefix+"/" {
			return true
		}
	}
	return false
}

func TestParseOperations(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []Operation
		unknown string
	}{
		{name: "empty", raw: ""},
		{name: "list", raw: " MCP, audio_speech,,mcp ", want: []Operation{OperationMCP, OperationAudioSpeech}},
		{name: "unknown", raw: "mcp,nope", unknown: "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unknown, ok := ParseOperations(tt.raw)
			assert.Equal(t, tt.unknown == "", ok)
			assert.Equal(t, tt.unknown, unknown)
			assert.Equal(t, tt.want, got)
		})
	}
}
