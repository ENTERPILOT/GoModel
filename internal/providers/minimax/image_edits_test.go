package minimax

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/require"
)

func portraitRequest() *core.ImageEditRequest {
	return &core.ImageEditRequest{Model: "image-01", Prompt: "A portrait in a garden", Images: []core.ImageFile{{Data: []byte("\x89PNG\r\n\x1a\nportrait")}}}
}

func TestCreateImageEdit(t *testing.T) {
	for _, format := range []string{"url", "b64_json"} {
		t.Run(format, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":{"image_urls":["https://example.com/portrait.png"],"image_base64":["cG9ydHJhaXQ="]},"base_resp":{"status_code":0}}`)
			provider := NewWithHTTPClient("minimax-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
			req := portraitRequest()
			req.Fields = []core.FormField{{Name: "response_format", Value: format}, {Name: "size", Value: "1024x1024"}, {Name: "n", Value: "2"}, {Name: "seed", Value: "42"}, {Name: "prompt_optimizer", Value: "true"}}
			response, err := provider.CreateImageEdit(context.Background(), req)
			require.NoError(t, err)
			require.Len(t, response.Data, 1)
			if format == "url" {
				require.Equal(t, "https://example.com/portrait.png", response.Data[0].URL)
			} else {
				require.Equal(t, "cG9ydHJhaXQ=", response.Data[0].B64JSON)
			}
			recorded := capture.Last(t)
			require.Equal(t, http.MethodPost, recorded.Method)
			require.Equal(t, "/v1/image_generation", recorded.Path)
			require.Equal(t, "Bearer minimax-key", recorded.Header.Get("Authorization"))
			body := recorded.JSON(t)
			require.Equal(t, "image-01", body["model"])
			require.Equal(t, float64(1024), body["width"])
			require.Equal(t, float64(2), body["n"])
			require.Equal(t, true, body["prompt_optimizer"])
			references := body["subject_reference"].([]any)
			require.Equal(t, map[string]any{"type": "character", "image_file": "data:image/png;base64," + base64.StdEncoding.EncodeToString(req.Images[0].Data)}, references[0])
		})
	}
}

func TestCreateImageEditErrors(t *testing.T) {
	for _, body := range []string{`{"base_resp":{"status_code":1004}}`, `{"data":{},"base_resp":{"status_code":0}}`, `invalid`, `{"data":{"image_urls":["https://example.com/image.png"]}}`, `{"data":{"image_urls":["https://example.com/image.png"]},"base_resp":{}}`} {
		t.Run(body, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, http.StatusOK, body)
			provider := NewWithHTTPClient("minimax-key", server.URL, server.Client(), llmclient.Hooks{})
			_, err := provider.CreateImageEdit(context.Background(), portraitRequest())
			require.Error(t, err)
		})
	}
}

func TestCreateImageEditValidation(t *testing.T) {
	for _, mutate := range []func(*core.ImageEditRequest){
		func(req *core.ImageEditRequest) { req.Mask = &core.ImageFile{Data: []byte("mask")} },
		func(req *core.ImageEditRequest) { req.Images[0].Data = []byte("not an image") },
		func(req *core.ImageEditRequest) { req.Fields = []core.FormField{{Name: "quality", Value: "high"}} },
		func(req *core.ImageEditRequest) { req.Fields = []core.FormField{{Name: "size", Value: "auto"}} },
	} {
		provider := NewWithHTTPClient("minimax-key", "http://unused.invalid", nil, llmclient.Hooks{})
		req := portraitRequest()
		mutate(req)
		_, err := provider.CreateImageEdit(context.Background(), req)
		require.Error(t, err)
	}
}
