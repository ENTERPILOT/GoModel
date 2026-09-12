package cohere

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// multipartParts is what the upstream saw in a transcription request: the
// form part names in wire order, the non-file fields, and the file part.
type multipartParts struct {
	names    []string
	fields   map[string]string
	filename string
	audio    []byte
}

func parseMultipart(contentType string, body []byte) (multipartParts, error) {
	parts := multipartParts{fields: map[string]string{}}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return parts, err
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return parts, nil
		}
		if err != nil {
			return parts, err
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return parts, err
		}
		parts.names = append(parts.names, part.FormName())
		if part.FormName() == "file" {
			parts.filename = part.FileName()
			parts.audio = data
			continue
		}
		parts.fields[part.FormName()] = string(data)
	}
}

func TestCreateTranscriptionTranslatesMultipartRequest(t *testing.T) {
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"text":"GoModel routes requests reliably."}`)
	})

	provider := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:          "cohere-transcribe-03-2026",
		Filename:       "sample.wav",
		FileReader:     bytes.NewBufferString("wave-bytes"),
		Language:       "en",
		ResponseFormat: "json",
		Temperature:    "0.2",
	})
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/v2/audio/transcriptions", sent.Path)
	assert.Equal(t, "Bearer test-key", sent.Header.Get("Authorization"))

	parts, err := parseMultipart(sent.Header.Get("Content-Type"), sent.Body)
	require.NoError(t, err)
	assert.Equal(t, []string{"model", "language", "temperature", "file"}, parts.names)
	assert.Equal(t, map[string]string{
		"model":       "cohere-transcribe-03-2026",
		"language":    "en",
		"temperature": "0.2",
	}, parts.fields)
	assert.Equal(t, "sample.wav", parts.filename)
	assert.Equal(t, "wave-bytes", string(parts.audio))

	assert.Equal(t, "application/json; charset=utf-8", resp.ContentType)
	assert.Equal(t, `{"text":"GoModel routes requests reliably."}`, string(resp.Data))
}

func TestCreateTranscriptionValidation(t *testing.T) {
	provider := NewWithHTTPClient("key", "https://example.com", nil, llmclient.Hooks{})
	tests := []struct {
		name string
		req  *core.AudioTranscriptionRequest
	}{
		{name: "nil request"},
		{name: "language required", req: &core.AudioTranscriptionRequest{File: []byte("audio")}},
		{name: "file required", req: &core.AudioTranscriptionRequest{Language: "en"}},
		{name: "response format", req: &core.AudioTranscriptionRequest{
			Language: "en", File: []byte("audio"), ResponseFormat: "text",
		}},
		{name: "prompt", req: &core.AudioTranscriptionRequest{
			Language: "en", File: []byte("audio"), Prompt: "names",
		}},
		{name: "timestamps", req: &core.AudioTranscriptionRequest{
			Language: "en", File: []byte("audio"), TimestampGranularities: []string{"word"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateTranscription(context.Background(), tt.req)
			assertInvalidRequest(t, err)
		})
	}
}

func TestCreateSpeechIsUnsupported(t *testing.T) {
	provider := NewWithHTTPClient("key", "https://example.com", nil, llmclient.Hooks{})
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{})
	assertInvalidRequest(t, err)
}
