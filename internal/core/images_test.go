package core

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

func TestDecodeImageGenerationRequest_PreservesExtraFields(t *testing.T) {
	body := []byte(`{"model":"gpt-image-1","prompt":"a cat","n":2,"size":"1024x1024","quality":"high","provider":"openai","background":"transparent","output_format":"webp","moderation":"low"}`)

	req, err := DecodeImageGenerationRequest(body, nil)
	if err != nil {
		t.Fatalf("DecodeImageGenerationRequest() error = %v", err)
	}
	if req.Model != "gpt-image-1" || req.Prompt != "a cat" || req.Size != "1024x1024" || req.Quality != "high" || req.Provider != "openai" {
		t.Fatalf("typed fields = %+v", req)
	}
	if req.ImageCount() != 2 {
		t.Fatalf("ImageCount() = %d, want 2", req.ImageCount())
	}

	// Marshal round-trip: unknown fields are forwarded upstream verbatim and
	// the gateway-only provider hint is dropped once cleared.
	req.Provider = ""
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var forwarded map[string]any
	if err := json.Unmarshal(out, &forwarded); err != nil {
		t.Fatalf("Unmarshal(forwarded) error = %v", err)
	}
	for key, want := range map[string]any{
		"model": "gpt-image-1", "prompt": "a cat", "n": float64(2), "size": "1024x1024",
		"quality": "high", "background": "transparent", "output_format": "webp", "moderation": "low",
	} {
		if got := forwarded[key]; got != want {
			t.Errorf("forwarded[%q] = %v, want %v", key, got, want)
		}
	}
	if _, present := forwarded["provider"]; present {
		t.Errorf("forwarded body still carries provider: %s", out)
	}
}

func TestDecodeImageGenerationRequest_RejectsMalformedJSON(t *testing.T) {
	_, err := DecodeImageGenerationRequest([]byte(`{"model":`), nil)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "invalid image generation request") {
		t.Errorf("error = %q, want invalid image generation request", err)
	}
}

func TestImageGenerationRequest_ImageCountDefaultsToOne(t *testing.T) {
	var nilReq *ImageGenerationRequest
	if got := nilReq.ImageCount(); got != 1 {
		t.Errorf("nil ImageCount() = %d, want 1", got)
	}
	if got := (&ImageGenerationRequest{}).ImageCount(); got != 1 {
		t.Errorf("unset ImageCount() = %d, want 1", got)
	}
}

func TestValidateImageGenerationRequest(t *testing.T) {
	zero := 0
	tests := []struct {
		name    string
		req     *ImageGenerationRequest
		wantErr string
	}{
		{"nil request", nil, "image generation request is required"},
		{"missing model", &ImageGenerationRequest{Model: " ", Prompt: "a cat"}, "model is required"},
		{"missing prompt", &ImageGenerationRequest{Model: "dall-e-3", Prompt: "  "}, "prompt is required"},
		{"zero n", &ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat", N: &zero}, "n must be at least 1"},
		{"streaming", &ImageGenerationRequest{Model: "gpt-image-1", Prompt: "a cat", Stream: true}, "streaming image generation is not supported"},
		{"valid", &ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateImageGenerationRequest(tt.req)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestImageGenerationResponse_OmitsEmptyOptionalFields(t *testing.T) {
	out, err := json.Marshal(&ImageGenerationResponse{Created: 1, Data: []ImageData{{URL: "https://img"}}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(out) != `{"created":1,"data":[{"url":"https://img"}]}` {
		t.Errorf("marshaled = %s", out)
	}
}
