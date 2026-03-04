//go:build contract

// Package contract provides contract tests that validate API response structures
// against recorded golden files. These tests verify that the gateway correctly
// handles provider API responses without making actual API calls.
package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// testdataDir is the path to the testdata directory.
const testdataDir = "testdata"

// loadGoldenFileRaw reads a golden file from testdata as raw bytes.
func loadGoldenFileRaw(t *testing.T, path string) []byte {
	t.Helper()

	fullPath := filepath.Join(testdataDir, path)
	data, err := os.ReadFile(fullPath)
	require.NoError(t, err, "failed to read golden file %s", fullPath)

	return data
}

// goldenFileExists checks if a golden file exists.
func goldenFileExists(t *testing.T, path string) bool {
	t.Helper()

	fullPath := filepath.Join(testdataDir, path)
	_, err := os.Stat(fullPath)
	return err == nil
}

// ToolCallResponse represents a chat response that may include tool calls.
type ToolCallResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Model   string           `json:"model"`
	Choices []ToolCallChoice `json:"choices"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Created int64 `json:"created"`
}

// ToolCallChoice represents a choice that may include tool calls.
type ToolCallChoice struct {
	Message      ToolCallMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
	Index        int             `json:"index"`
}

// ToolCallMessage represents a message that may include tool calls.
type ToolCallMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolCall represents a tool call in a message.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall represents the function details in a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
