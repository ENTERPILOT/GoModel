package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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
			if got := emptyResponseReason(tt.resp, tt.err); got != tt.want {
				t.Fatalf("emptyResponseReason() = %q, want %q", got, tt.want)
			}
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
			if err != nil {
				t.Fatalf("NewRouter: %v", err)
			}
			var reported []llmclient.EmptyResponseInfo
			router.SetEmptyResponseHook(func(_ context.Context, info llmclient.EmptyResponseInfo) {
				reported = append(reported, info)
			})

			if err := tt.call(router); err != nil {
				t.Fatalf("call error = %v", err)
			}

			if tt.wantReason == "" {
				if len(reported) != 0 {
					t.Fatalf("reported = %+v, want none", reported)
				}
				return
			}
			want := llmclient.EmptyResponseInfo{
				Provider:     "openai-primary",
				ProviderType: "openai",
				Model:        "gpt-4o",
				Operation:    llmclient.OperationChat,
				Reason:       tt.wantReason,
			}
			if len(reported) != 1 || reported[0] != want {
				t.Fatalf("reported = %+v, want [%+v]", reported, want)
			}
		})
	}
}
