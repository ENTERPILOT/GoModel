//go:build contract

// Contract tests in this file are intended to run with: -tags=contract -timeout=5m.
package contract

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func openAIImageProvider(t *testing.T, routes map[string]replayRoute) core.ImageProvider {
	t.Helper()
	provider := newOpenAIReplayProvider(t, routes)
	images, ok := provider.(core.ImageProvider)
	require.True(t, ok, "openai provider should implement core.ImageProvider")
	return images
}

func TestOpenAIReplayCreateImage(t *testing.T) {
	testCases := []struct {
		name      string
		body      string
		wantURL   string
		wantB64   string
		wantUsage bool
	}{
		{
			name:    "dall-e url response",
			body:    `{"created":1713833628,"data":[{"url":"https://oaidalleapiprodscus.blob.core.windows.net/img.png","revised_prompt":"A fluffy cat"}]}`,
			wantURL: "https://oaidalleapiprodscus.blob.core.windows.net/img.png",
		},
		{
			name:      "gpt-image-1 base64 response with usage",
			body:      `{"created":1713833628,"background":"opaque","output_format":"png","quality":"high","size":"1024x1024","data":[{"b64_json":"aGVsbG8="}],"usage":{"input_tokens":12,"output_tokens":1056,"total_tokens":1068,"input_tokens_details":{"image_tokens":0,"text_tokens":12}}}`,
			wantB64:   "aGVsbG8=",
			wantUsage: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			images := openAIImageProvider(t, map[string]replayRoute{
				replayKey(http.MethodPost, "/images/generations"): {
					statusCode:  http.StatusOK,
					contentType: "application/json",
					body:        []byte(tc.body),
				},
			})

			resp, err := images.CreateImage(context.Background(), &core.ImageGenerationRequest{
				Model:  "gpt-image-1",
				Prompt: "A fluffy cat",
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.EqualValues(t, 1713833628, resp.Created)
			require.Len(t, resp.Data, 1)
			require.Equal(t, tc.wantURL, resp.Data[0].URL)
			require.Equal(t, tc.wantB64, resp.Data[0].B64JSON)
			if tc.wantUsage {
				require.NotNil(t, resp.Usage)
				require.Equal(t, 1068, resp.Usage.TotalTokens)
				require.Equal(t, "1024x1024", resp.Size)
			} else {
				require.Nil(t, resp.Usage)
			}
		})
	}
}

func TestOpenAIReplayCreateImageValidation(t *testing.T) {
	images := openAIImageProvider(t, map[string]replayRoute{})

	_, err := images.CreateImage(context.Background(), &core.ImageGenerationRequest{Model: "dall-e-3"})
	require.Error(t, err, "missing prompt should be rejected before any upstream call")
}
