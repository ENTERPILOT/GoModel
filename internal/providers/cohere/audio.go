package cohere

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// CreateSpeech reports that Cohere has no text-to-speech API. AudioProvider
// currently groups speech and transcription into one optional capability, so a
// transcription-only provider must still implement this method.
func (p *Provider) CreateSpeech(_ context.Context, _ *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	return nil, core.NewInvalidRequestError("cohere does not support text-to-speech", nil)
}

// CreateTranscription translates the OpenAI-compatible multipart request to
// Cohere's native POST /v2/audio/transcriptions endpoint.
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio transcription request is required", nil)
	}
	if strings.TrimSpace(req.Language) == "" {
		return nil, core.NewInvalidRequestError("language is required for Cohere transcription", nil)
	}
	if format := strings.ToLower(strings.TrimSpace(req.ResponseFormat)); format != "" && format != "json" {
		return nil, core.NewInvalidRequestError("cohere transcription supports only the json response format", nil)
	}
	if strings.TrimSpace(req.Prompt) != "" {
		return nil, core.NewInvalidRequestError("cohere transcription does not support prompt", nil)
	}
	for _, granularity := range req.TimestampGranularities {
		if strings.TrimSpace(granularity) != "" {
			return nil, core.NewInvalidRequestError("cohere transcription does not support timestamp_granularities", nil)
		}
	}

	content := req.FileReader
	if content == nil && len(req.File) > 0 {
		content = bytes.NewReader(req.File)
	}
	if content == nil {
		return nil, core.NewInvalidRequestError("file is required", nil)
	}

	body, contentType := cohereTranscriptionMultipart(req, content)
	raw, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:        http.MethodPost,
		Endpoint:      "/v2/audio/transcriptions",
		RawBodyReader: body,
		Headers:       http.Header{"Content-Type": {contentType}},
	})
	if err != nil {
		return nil, err
	}

	responseContentType := strings.TrimSpace(raw.ContentType)
	if responseContentType == "" {
		responseContentType = "application/json"
	}
	return &core.AudioResponse{
		ContentType: responseContentType,
		Data:        raw.Body,
	}, nil
}

// cohereTranscriptionMultipart streams form fields followed by the file.
// Cohere requires the file to be the final multipart field.
func cohereTranscriptionMultipart(req *core.AudioTranscriptionRequest, content io.Reader) (io.Reader, string) {
	filename := strings.TrimSpace(req.Filename)
	if filename == "" {
		filename = "audio"
	}

	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	go func() {
		defer func() { _ = pw.Close() }()

		fields := [...][2]string{
			{"model", req.Model},
			{"language", req.Language},
			{"temperature", req.Temperature},
		}
		for _, field := range fields {
			if strings.TrimSpace(field[1]) == "" {
				continue
			}
			if err := writer.WriteField(field[0], field[1]); err != nil {
				_ = pw.CloseWithError(core.NewInvalidRequestError("failed to write "+field[0]+" field", err))
				return
			}
		}

		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to create multipart file field", err))
			return
		}
		if _, err := io.Copy(part, content); err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to stream file content", err))
			return
		}
		if err := writer.Close(); err != nil {
			_ = pw.CloseWithError(core.NewInvalidRequestError("failed to finalize multipart payload", err))
		}
	}()
	return pr, writer.FormDataContentType()
}
