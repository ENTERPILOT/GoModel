//go:build cgo

package embedding

import (
	"context"
	"errors"
	"testing"

	all_minilm "github.com/clems4ever/all-minilm-l6-v2-go/all_minilm_l6_v2"

	"gomodel/internal/core"
)

func TestMiniLMEmbedderEmbed_ContextAlreadyDone(t *testing.T) {
	orig := miniLMModelCompute
	t.Cleanup(func() { miniLMModelCompute = orig })

	called := false
	miniLMModelCompute = func(_ *all_minilm.Model, _ string) ([]float32, error) {
		called = true
		return nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (&miniLMEmbedder{}).Embed(ctx, "ignored")
	if err == nil {
		t.Fatal("expected error")
	}
	if called {
		t.Fatal("compute should not be called when context is already done")
	}

	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("gatewayErr.Type = %q, want %q", gatewayErr.Type, core.ErrorTypeInvalidRequest)
	}
	if !errors.Is(gatewayErr, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", gatewayErr)
	}
}

func TestMiniLMEmbedderEmbed_ComputeFailure(t *testing.T) {
	orig := miniLMModelCompute
	t.Cleanup(func() { miniLMModelCompute = orig })

	computeErr := errors.New("compute failed")
	miniLMModelCompute = func(_ *all_minilm.Model, _ string) ([]float32, error) {
		return nil, computeErr
	}

	_, err := (&miniLMEmbedder{}).Embed(context.Background(), "ignored")
	if err == nil {
		t.Fatal("expected error")
	}

	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider {
		t.Fatalf("gatewayErr.Type = %q, want %q", gatewayErr.Type, core.ErrorTypeProvider)
	}
	if !errors.Is(gatewayErr, computeErr) {
		t.Fatalf("expected wrapped compute error, got %v", gatewayErr)
	}
}

func TestMiniLMEmbedderEmbed_ContextDoneDuringCompute(t *testing.T) {
	orig := miniLMModelCompute
	t.Cleanup(func() { miniLMModelCompute = orig })

	computeErr := errors.New("compute interrupted")
	ctx, cancel := context.WithCancel(context.Background())
	miniLMModelCompute = func(_ *all_minilm.Model, _ string) ([]float32, error) {
		cancel()
		return nil, computeErr
	}

	_, err := (&miniLMEmbedder{}).Embed(ctx, "ignored")
	if err == nil {
		t.Fatal("expected error")
	}

	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("gatewayErr.Type = %q, want %q", gatewayErr.Type, core.ErrorTypeInvalidRequest)
	}
	if !errors.Is(gatewayErr, context.Canceled) {
		t.Fatalf("expected wrapped context cancellation, got %v", gatewayErr)
	}
	if !errors.Is(gatewayErr, computeErr) {
		t.Fatalf("expected wrapped compute error, got %v", gatewayErr)
	}
}
