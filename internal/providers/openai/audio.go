package openai

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

// CreateSpeech implements OpenAI text-to-speech (POST /audio/speech). The upstream
// returns binary audio; the response is read raw and tagged with the upstream
// Content-Type so it describes the bytes actually returned (see
// speechResponseContentType), which usage relies on to price output-duration
// models.
func (p *CompatibleProvider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio speech request is required", nil)
	}
	if strings.TrimSpace(req.Input) == "" {
		return nil, core.NewInvalidRequestError("input is required", nil)
	}
	if strings.TrimSpace(req.Voice) == "" {
		return nil, core.NewInvalidRequestError("voice is required", nil)
	}

	raw, err := p.client.DoRaw(ctx, p.prepareRequest(llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/audio/speech",
		Body:     req,
	}))
	if err != nil {
		return nil, err
	}
	return &core.AudioResponse{
		ContentType: speechResponseContentType(raw, req.ResponseFormat),
		Data:        raw.Body,
	}, nil
}

// speechResponseContentType returns the authoritative media type of synthesized
// speech. The upstream response Content-Type describes the bytes actually
// returned, so it wins; it falls back to the type implied by the requested
// response_format when the upstream omits the header.
func speechResponseContentType(raw *llmclient.Response, format string) string {
	if raw != nil {
		if ct := strings.TrimSpace(raw.ContentType); ct != "" {
			return ct
		}
	}
	return core.SpeechResponseContentType(format)
}

// CreateTranscription implements OpenAI speech-to-text (POST /audio/transcriptions).
// The request is multipart/form-data; the response (JSON or text per response_format)
// is proxied verbatim.
func (p *CompatibleProvider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio transcription request is required", nil)
	}
	content := req.FileReader
	if content == nil && len(req.File) > 0 {
		content = bytes.NewReader(req.File)
	}
	if content == nil {
		return nil, core.NewInvalidRequestError("file is required", nil)
	}

	body, contentType := transcriptionMultipart(req, content)
	raw, err := p.client.DoRaw(ctx, p.prepareRequest(llmclient.Request{
		Method:        http.MethodPost,
		Endpoint:      "/audio/transcriptions",
		RawBodyReader: body,
		Headers:       http.Header{"Content-Type": {contentType}},
	}))
	if err != nil {
		return nil, err
	}
	return &core.AudioResponse{
		ContentType: core.TranscriptionResponseContentType(req.ResponseFormat),
		Data:        raw.Body,
	}, nil
}

// transcriptionMultipart streams a multipart/form-data body for a transcription
// request and returns the reader plus its Content-Type. It mirrors the file-upload
// adapter: the body is produced on a goroutine through an io.Pipe so large audio
// files are never buffered whole.
func transcriptionMultipart(req *core.AudioTranscriptionRequest, content io.Reader) (io.Reader, string) {
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
			{"prompt", req.Prompt},
			{"response_format", req.ResponseFormat},
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
		for _, granularity := range req.TimestampGranularities {
			if strings.TrimSpace(granularity) == "" {
				continue
			}
			if err := writer.WriteField("timestamp_granularities[]", granularity); err != nil {
				_ = pw.CloseWithError(core.NewInvalidRequestError("failed to write timestamp_granularities field", err))
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
			return
		}
	}()
	return pr, writer.FormDataContentType()
}
