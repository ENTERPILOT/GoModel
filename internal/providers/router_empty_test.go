package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/stretchr/testify/require"
)

func TestEmptyResponseReason(t *testing.T) {
	message := []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}}}
	usage := core.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4}
	output := []core.ResponsesOutputItem{{Type: "message"}}
	responsesUsage := &core.ResponsesUsage{InputTokens: 3, OutputTokens: 1, TotalTokens: 4}

	tests := []struct {
		name string
		resp any
		err  error
		want string
	}{
		{name: "chat with choices and usage", resp: &core.ChatResponse{Choices: message, Usage: usage}},
		{name: "chat without choices", resp: &core.ChatResponse{Usage: usage}, want: llmclient.EmptyReasonNoChoices},
		{name: "nil chat response", resp: (*core.ChatResponse)(nil), want: llmclient.EmptyReasonNoChoices},
		{name: "chat without usage", resp: &core.ChatResponse{Choices: message}, want: llmclient.EmptyReasonNoUsage},
		{name: "no choices error", err: core.NewNoChoicesProviderError("openai"), want: llmclient.EmptyReasonNoChoices},
		{name: "other provider error", err: core.NewEmptyProviderResponseError("openai")},
		{name: "plain error", err: errors.New("boom")},
		{name: "completed responses", resp: &core.ResponsesResponse{Status: "completed", Output: output, Usage: responsesUsage}},
		{name: "responses without output", resp: &core.ResponsesResponse{Status: "completed", Usage: responsesUsage}, want: llmclient.EmptyReasonNoOutput},
		{name: "responses without usage", resp: &core.ResponsesResponse{Status: "completed", Output: output}, want: llmclient.EmptyReasonNoUsage},
		{name: "responses with zero usage", resp: &core.ResponsesResponse{Output: output, Usage: &core.ResponsesUsage{}}, want: llmclient.EmptyReasonNoUsage},
		{name: "queued background responses", resp: &core.ResponsesResponse{Status: "queued"}},
		{name: "nil responses response", resp: (*core.ResponsesResponse)(nil), want: llmclient.EmptyReasonNoOutput},
		{name: "embeddings are not checked", resp: &core.EmbeddingResponse{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := emptyResponseReason(tt.resp, tt.err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestRouterReportsEmptyResponses(t *testing.T) {
	tests := []struct {
		name       string
		provider   *mockProvider
		call       func(*Router) error
		wantReason string
	}{
		{
			name:     "chat without choices",
			provider: &mockProvider{chatResponse: &core.ChatResponse{ID: "empty"}},
			call: func(r *Router) error {
				_, err := r.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-4o"})
				return err
			},
			wantReason: llmclient.EmptyReasonNoChoices,
		},
		{
			name:     "responses translated from a chat without choices",
			provider: &mockProvider{err: core.NewNoChoicesProviderError("openai-primary")},
			call: func(r *Router) error {
				_, err := r.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-4o"})
				if !errors.Is(err, core.ErrNoChoices) {
					return err
				}
				return nil
			},
			wantReason: llmclient.EmptyReasonNoChoices,
		},
		{
			name: "complete chat",
			provider: &mockProvider{chatResponse: &core.ChatResponse{
				Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}}},
				Usage:   core.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
			}},
			call: func(r *Router) error {
				_, err := r.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-4o"})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, err := NewRouter(newTestRegistryWithModels(registryModelEntry{
				provider:     tt.provider,
				providerName: "openai-primary",
				providerType: "openai",
				modelID:      "gpt-4o",
			}))
			require.NoError(t, err)

			var reported []llmclient.EmptyResponseInfo
			router.SetEmptyResponseHook(func(_ context.Context, info llmclient.EmptyResponseInfo) {
				reported = append(reported, info)
			})
			err = tt.call(router)
			require.NoError(t, err)

			if tt.wantReason == "" {
				require.Empty(t, reported)

				return
			}
			want := llmclient.EmptyResponseInfo{
				Provider:     "openai-primary",
				ProviderType: "openai",
				Model:        "gpt-4o",
				Operation:    llmclient.OperationChat,
				Reason:       tt.wantReason,
			}
			require.Len(t, reported, 1)
			require.Equal(t, want, reported[0])
		})
	}
}
