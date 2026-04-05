package guardrails

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"gomodel/internal/core"
)

type mockChatCompletionExecutor struct {
	chatFn func(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error)
}

func (m mockChatCompletionExecutor) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if m.chatFn != nil {
		return m.chatFn(ctx, req)
	}
	return nil, fmt.Errorf("unexpected ChatCompletion call")
}

func TestNormalizeLLMBasedAlteringConfig_Defaults(t *testing.T) {
	cfg, err := NormalizeLLMBasedAlteringConfig(LLMBasedAlteringConfig{
		Model: "gpt-4o-mini",
	})
	if err != nil {
		t.Fatalf("NormalizeLLMBasedAlteringConfig() error = %v", err)
	}
	if cfg.Prompt != DefaultLLMBasedAlteringPrompt {
		t.Fatal("expected default prompt to be applied")
	}
	if cfg.MaxTokens != DefaultLLMBasedAlteringMaxTokens {
		t.Fatalf("MaxTokens = %d, want %d", cfg.MaxTokens, DefaultLLMBasedAlteringMaxTokens)
	}
	if len(cfg.Roles) != 1 || cfg.Roles[0] != "user" {
		t.Fatalf("Roles = %#v, want [user]", cfg.Roles)
	}
}

func TestNormalizeLLMBasedAlteringConfig_NormalizesUserPath(t *testing.T) {
	cfg, err := NormalizeLLMBasedAlteringConfig(LLMBasedAlteringConfig{
		Model:    "gpt-4o-mini",
		UserPath: "team/privacy",
	})
	if err != nil {
		t.Fatalf("NormalizeLLMBasedAlteringConfig() error = %v", err)
	}
	if cfg.UserPath != "/team/privacy" {
		t.Fatalf("UserPath = %q, want /team/privacy", cfg.UserPath)
	}
}

func TestNewLLMBasedAlteringGuardrail_RequiresModel(t *testing.T) {
	_, err := NewLLMBasedAlteringGuardrail("privacy", LLMBasedAlteringConfig{}, mockChatCompletionExecutor{})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestNewLLMBasedAlteringGuardrail_RejectsSlashInName(t *testing.T) {
	_, err := NewLLMBasedAlteringGuardrail("privacy/redactor", LLMBasedAlteringConfig{
		Model: "gpt-4o-mini",
	}, mockChatCompletionExecutor{})
	if err == nil {
		t.Fatal("expected error for slash in guardrail name")
	}
}

func TestLLMBasedAltering_Process_RewritesConfiguredRoles(t *testing.T) {
	var captured *core.ChatRequest
	g, err := NewLLMBasedAlteringGuardrail("privacy", LLMBasedAlteringConfig{
		Model: "gpt-4o-mini",
		Roles: []string{"user"},
	}, mockChatCompletionExecutor{
		chatFn: func(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
			captured = req
			return &core.ChatResponse{
				Choices: []core.Choice{
					{Message: core.ResponseMessage{Role: "assistant", Content: "[|---|](PERSON_1) says hello"}},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewLLMBasedAlteringGuardrail() error = %v", err)
	}

	msgs := []Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "John says hello"},
		{Role: "assistant", Content: "leave me alone"},
	}
	got, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if captured == nil {
		t.Fatal("expected auxiliary ChatCompletion call")
	}
	if captured.Model != "gpt-4o-mini" {
		t.Fatalf("auxiliary model = %q, want gpt-4o-mini", captured.Model)
	}
	if got[1].Content != "[|---|](PERSON_1) says hello" {
		t.Fatalf("user content = %q, want rewritten content", got[1].Content)
	}
	if got[2].Content != "leave me alone" {
		t.Fatalf("assistant content = %q, want unchanged content", got[2].Content)
	}
}

func TestLLMBasedAltering_Process_UsesInternalGuardrailOriginAndUserPath(t *testing.T) {
	var (
		gotOrigin   core.RequestOrigin
		gotUserPath string
	)
	g, err := NewLLMBasedAlteringGuardrail("privacy", LLMBasedAlteringConfig{
		Model:    "gpt-4o-mini",
		UserPath: "/team/privacy",
	}, mockChatCompletionExecutor{
		chatFn: func(ctx context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
			gotOrigin = core.GetRequestOrigin(ctx)
			gotUserPath = core.UserPathFromContext(ctx)
			return &core.ChatResponse{
				Choices: []core.Choice{
					{Message: core.ResponseMessage{Role: "assistant", Content: "[|---|](PERSON_1)"}},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewLLMBasedAlteringGuardrail() error = %v", err)
	}

	parentCtx := core.WithRequestSnapshot(context.Background(), &core.RequestSnapshot{
		UserPath: "/team/caller",
	})

	_, err = g.Process(parentCtx, []Message{{Role: "user", Content: "John Smith"}})
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if gotOrigin != core.RequestOriginGuardrail {
		t.Fatalf("request origin = %q, want %q", gotOrigin, core.RequestOriginGuardrail)
	}
	if gotUserPath != "/team/privacy/guardrails/privacy" {
		t.Fatalf("user path = %q, want /team/privacy/guardrails/privacy", gotUserPath)
	}
}

func TestLLMBasedAltering_Process_SkipsPrefix(t *testing.T) {
	g, err := NewLLMBasedAlteringGuardrail("privacy", LLMBasedAlteringConfig{
		Model:             "gpt-4o-mini",
		SkipContentPrefix: "### safe",
	}, mockChatCompletionExecutor{
		chatFn: func(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
			return nil, fmt.Errorf("should not be called")
		},
	})
	if err != nil {
		t.Fatalf("NewLLMBasedAlteringGuardrail() error = %v", err)
	}

	msgs := []Message{{Role: "user", Content: "### safe do not rewrite"}}
	got, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got[0].Content != msgs[0].Content {
		t.Fatalf("Content = %q, want unchanged", got[0].Content)
	}
}

func TestLLMBasedAltering_Process_FailsOpenOnProviderError(t *testing.T) {
	g, err := NewLLMBasedAlteringGuardrail("privacy", LLMBasedAlteringConfig{
		Model: "gpt-4o-mini",
	}, mockChatCompletionExecutor{
		chatFn: func(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
			return nil, fmt.Errorf("upstream failed")
		},
	})
	if err != nil {
		t.Fatalf("NewLLMBasedAlteringGuardrail() error = %v", err)
	}

	msgs := []Message{{Role: "user", Content: "John says hello"}}
	got, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got[0].Content != msgs[0].Content {
		t.Fatalf("Content = %q, want original content", got[0].Content)
	}
}

func TestLLMBasedAltering_Process_PropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	g, err := NewLLMBasedAlteringGuardrail("privacy", LLMBasedAlteringConfig{
		Model: "gpt-4o-mini",
	}, mockChatCompletionExecutor{
		chatFn: func(ctx context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
			return nil, ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("NewLLMBasedAlteringGuardrail() error = %v", err)
	}

	_, err = g.Process(ctx, []Message{{Role: "user", Content: "John says hello"}})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestLLMBasedAltering_Process_PropagatesMidFlightCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	g, err := NewLLMBasedAlteringGuardrail("privacy", LLMBasedAlteringConfig{
		Model: "gpt-4o-mini",
	}, mockChatCompletionExecutor{
		chatFn: func(ctx context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	if err != nil {
		t.Fatalf("NewLLMBasedAlteringGuardrail() error = %v", err)
	}

	resultCh := make(chan error, 1)
	go func() {
		_, err := g.Process(ctx, []Message{{Role: "user", Content: "John says hello"}})
		resultCh <- err
	}()

	<-started
	cancel()

	err = <-resultCh
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestLLMBasedAltering_Process_FailsOpenOnToolCallCompletion(t *testing.T) {
	g, err := NewLLMBasedAlteringGuardrail("privacy", LLMBasedAlteringConfig{
		Model: "gpt-4o-mini",
	}, mockChatCompletionExecutor{
		chatFn: func(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
			return &core.ChatResponse{
				Choices: []core.Choice{
					{
						Message: core.ResponseMessage{
							Role: "assistant",
							ToolCalls: []core.ToolCall{
								{
									ID:   "call_1",
									Type: "function",
									Function: core.FunctionCall{
										Name:      "lookup_patient",
										Arguments: "{}",
									},
								},
							},
						},
					},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewLLMBasedAlteringGuardrail() error = %v", err)
	}

	msgs := []Message{{Role: "user", Content: "John says hello"}}
	got, err := g.Process(context.Background(), msgs)
	if err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got[0].Content != msgs[0].Content {
		t.Fatalf("Content = %q, want original content after tool-call failure", got[0].Content)
	}
}

func TestLLMBasedAltering_Process_LimitsConcurrentRewrites(t *testing.T) {
	var (
		inFlight     int32
		maxInFlight  int32
		requestsSeen int32
		messageCount = maxConcurrentRewrites*3 + 1
	)
	g, err := NewLLMBasedAlteringGuardrail("privacy", LLMBasedAlteringConfig{
		Model: "gpt-4o-mini",
	}, mockChatCompletionExecutor{
		chatFn: func(_ context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
			current := atomic.AddInt32(&inFlight, 1)
			defer atomic.AddInt32(&inFlight, -1)
			atomic.AddInt32(&requestsSeen, 1)

			for {
				previous := atomic.LoadInt32(&maxInFlight)
				if current <= previous || atomic.CompareAndSwapInt32(&maxInFlight, previous, current) {
					break
				}
			}

			time.Sleep(10 * time.Millisecond)
			return &core.ChatResponse{
				Choices: []core.Choice{
					{Message: core.ResponseMessage{Role: "assistant", Content: core.ExtractTextContent(req.Messages[1].Content)}},
				},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewLLMBasedAlteringGuardrail() error = %v", err)
	}

	msgs := make([]Message, messageCount)
	for i := range msgs {
		msgs[i] = Message{Role: "user", Content: fmt.Sprintf("text-%d", i)}
	}

	if _, err := g.Process(context.Background(), msgs); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if got := atomic.LoadInt32(&requestsSeen); got != int32(messageCount) {
		t.Fatalf("requests seen = %d, want %d", got, messageCount)
	}
	if got := atomic.LoadInt32(&maxInFlight); got > int32(maxConcurrentRewrites) {
		t.Fatalf("max concurrent rewrites = %d, want <= %d", got, maxConcurrentRewrites)
	}
}
