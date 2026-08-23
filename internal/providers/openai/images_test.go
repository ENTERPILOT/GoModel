package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestCreateImage_ForwardsRequestAndDecodesResponse(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	provider := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1713833628,"data":[{"b64_json":"aGk=","revised_prompt":"a fluffy cat"}],"output_format":"png","quality":"high","size":"1024x1024","usage":{"input_tokens":10,"output_tokens":1000,"total_tokens":1010,"input_tokens_details":{"text_tokens":10,"image_tokens":0}}}`))
	})

	req, err := core.DecodeImageGenerationRequest([]byte(`{"model":"gpt-image-1","prompt":"a cat","n":1,"quality":"high","background":"opaque"}`), nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp, err := provider.CreateImage(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateImage() error = %v", err)
	}

	if gotPath != "/images/generations" {
		t.Errorf("path = %q, want /images/generations", gotPath)
	}
	if gotBody["model"] != "gpt-image-1" || gotBody["prompt"] != "a cat" || gotBody["background"] != "opaque" {
		t.Errorf("forwarded body = %v", gotBody)
	}
	if _, present := gotBody["provider"]; present {
		t.Errorf("forwarded body carries provider hint: %v", gotBody)
	}

	if resp.Created != 1713833628 || len(resp.Data) != 1 || resp.Data[0].B64JSON != "aGk=" || resp.Data[0].RevisedPrompt != "a fluffy cat" {
		t.Errorf("response = %+v", resp)
	}
	if resp.Quality != "high" || resp.Size != "1024x1024" || resp.OutputFormat != "png" {
		t.Errorf("echoed output parameters = %+v", resp)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 1010 || resp.Usage.InputTokensDetails == nil || resp.Usage.InputTokensDetails.TextTokens != 10 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestCreateImage_FillsMissingCreatedAndData(t *testing.T) {
	provider := newSpeechTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	})

	resp, err := provider.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat"})
	if err != nil {
		t.Fatalf("CreateImage() error = %v", err)
	}
	if resp.Created == 0 {
		t.Error("Created should default to now when upstream omits it")
	}
	if resp.Data == nil {
		t.Error("Data should be an empty array, not null")
	}
}

func TestCreateImage_RejectsInvalidRequests(t *testing.T) {
	provider := newSpeechTestProvider(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("upstream must not be called for invalid requests")
	})

	tests := []struct {
		name    string
		req     *core.ImageGenerationRequest
		wantMsg string
	}{
		{"nil", nil, "image generation request is required"},
		{"empty prompt", &core.ImageGenerationRequest{Model: "dall-e-3"}, "prompt is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateImage(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("error = %v, want %q", err, tt.wantMsg)
			}
		})
	}
}

func TestCreateImage_PropagatesUpstreamError(t *testing.T) {
	provider := newSpeechTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Your request was rejected as a result of our safety system.","type":"invalid_request_error"}}`))
	})

	_, err := provider.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "dall-e-3", Prompt: "a cat"})
	if err == nil {
		t.Fatal("expected upstream error")
	}
	if !strings.Contains(err.Error(), "safety system") {
		t.Errorf("error = %v, want upstream message preserved", err)
	}
}
