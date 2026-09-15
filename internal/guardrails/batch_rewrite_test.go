package guardrails

import (
	"encoding/json"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestRewriteGuardedChatBatchBody(t *testing.T) {
	makeReq := func(role, content string) *core.ChatRequest {
		return &core.ChatRequest{
			Model:    "gpt-4",
			Messages: []core.Message{{Role: role, Content: content}},
		}
	}

	originalBody := func(req *core.ChatRequest) json.RawMessage {
		body, err := json.Marshal(req)
		require.NoError(t, err)

		return body
	}

	tests := []struct {
		name         string
		originalBody func(orig *core.ChatRequest) json.RawMessage
		original     *core.ChatRequest
		modified     *core.ChatRequest
		wantErrIs    core.ErrorType // empty = expect success
		wantBodyHas  string         // substring assertion when no error
	}{
		{
			name:         "nil modified rejected with invalid_request_error",
			originalBody: originalBody,
			original:     makeReq("user", "hello"),
			modified:     nil,
			wantErrIs:    core.ErrorTypeInvalidRequest,
		},
		{
			name:         "nil original rejected with invalid_request_error",
			originalBody: originalBody,
			original:     nil,
			modified:     makeReq("user", "hello"),
			wantErrIs:    core.ErrorTypeInvalidRequest,
		},
		{
			name:         "successful raw-body patch returns patched body",
			originalBody: originalBody,
			original:     makeReq("user", "hello"),
			modified:     makeReq("user", "rewritten"),
			wantBodyHas:  `"rewritten"`,
		},
		{
			name:         "validation error from message reorder propagates as invalid_request_error",
			originalBody: originalBody,
			original:     makeReq("user", "hello"),
			modified: &core.ChatRequest{
				Model: "gpt-4",
				// guardrails inserted a non-system message — patcher returns InvalidRequest.
				Messages: []core.Message{
					{Role: "user", Content: "hello"},
					{Role: "user", Content: "extra-injected"},
				},
			},
			wantErrIs: core.ErrorTypeInvalidRequest,
		},
		{
			name: "raw-body parse failure falls back to Marshal(modified)",
			originalBody: func(orig *core.ChatRequest) json.RawMessage {
				return json.RawMessage(`not valid json`)
			},
			original:    makeReq("user", "hello"),
			modified:    makeReq("user", "rewritten"),
			wantBodyHas: `"rewritten"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := rewriteGuardedChatBatchBody(tt.originalBody(tt.original), tt.original, tt.modified)
			if tt.wantErrIs != "" {
				require.Error(t, err)

				var gwErr *core.GatewayError
				require.ErrorAs(t, err, &gwErr)
				require.Equal(t, tt.wantErrIs, gwErr.Type)

				return
			}
			require.NoError(t, err)
			require.Contains(t, string(body), tt.wantBodyHas, "expected body to contain %q, got %s", tt.wantBodyHas, body)
		})
	}
}
