package providers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

type mockImageProvider struct {
	*mockProvider
	imageResponse *core.ImageGenerationResponse
	lastImageReq  *core.ImageGenerationRequest
}

func (m *mockImageProvider) CreateImage(_ context.Context, req *core.ImageGenerationRequest) (*core.ImageGenerationResponse, error) {
	m.lastImageReq = req
	if m.err != nil {
		return nil, m.err
	}
	return m.imageResponse, nil
}

func TestRouterCreateImage(t *testing.T) {
	imager := &mockImageProvider{
		mockProvider:  &mockProvider{name: "openai"},
		imageResponse: &core.ImageGenerationResponse{Created: 1, Data: []core.ImageData{{URL: "https://img"}}},
	}
	lookup := newMockLookup()
	lookup.addModel("openai/dall-e-3", imager, "openai")
	router, _ := NewRouter(lookup)

	req := &core.ImageGenerationRequest{Model: "dall-e-3", Provider: "openai", Prompt: "a cat"}
	resp, err := router.CreateImage(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateImage() error = %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].URL != "https://img" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Provider != "openai" {
		t.Errorf("response provider = %q, want openai stamped", resp.Provider)
	}
	if imager.lastImageReq == nil {
		t.Fatal("image provider was not called")
	}
	if imager.lastImageReq.Model != "dall-e-3" || imager.lastImageReq.Provider != "" {
		t.Errorf("forwarded selector = %q/%q, want provider metadata stripped", imager.lastImageReq.Provider, imager.lastImageReq.Model)
	}
	if req.Provider != "openai" {
		t.Errorf("caller's request was mutated: provider = %q", req.Provider)
	}
}

func TestRouterCreateImage_Errors(t *testing.T) {
	providerErr := errors.New("image provider failed")
	tests := []struct {
		name         string
		model        string
		provider     core.Provider
		providerType string
		req          *core.ImageGenerationRequest
		wantError    string
		wantIs       error
	}{
		{
			name:         "unsupported provider",
			model:        "anthropic/claude-sonnet-4",
			provider:     &mockProvider{name: "anthropic"},
			providerType: "anthropic",
			req:          &core.ImageGenerationRequest{Model: "claude-sonnet-4", Provider: "anthropic", Prompt: "a cat"},
			wantError:    "does not support image generation",
		},
		{
			name:         "provider failure",
			model:        "openai/dall-e-3",
			provider:     &mockImageProvider{mockProvider: &mockProvider{name: "openai", err: providerErr}},
			providerType: "openai",
			req:          &core.ImageGenerationRequest{Model: "dall-e-3", Provider: "openai", Prompt: "a cat"},
			wantIs:       providerErr,
		},
		{
			name:         "unknown model",
			model:        "openai/dall-e-3",
			provider:     &mockImageProvider{mockProvider: &mockProvider{name: "openai"}},
			providerType: "openai",
			req:          &core.ImageGenerationRequest{Model: "missing-model", Prompt: "a cat"},
			wantError:    "missing-model",
		},
		{
			name:      "nil request",
			wantError: "image generation request is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := newMockLookup()
			if tt.provider != nil {
				lookup.addModel(tt.model, tt.provider, tt.providerType)
			}
			router, err := NewRouter(lookup)
			if err != nil {
				t.Fatalf("NewRouter() error = %v", err)
			}

			_, err = router.CreateImage(context.Background(), tt.req)
			if tt.wantIs != nil {
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("CreateImage() error = %v, want %v", err, tt.wantIs)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("CreateImage() error = %v, want message containing %q", err, tt.wantError)
			}
		})
	}
}
