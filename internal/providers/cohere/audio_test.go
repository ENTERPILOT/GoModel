package cohere

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestCreateTranscriptionTranslatesMultipartRequest(t *testing.T) {
	var (
		partNames []string
		fields    = map[string]string{}
		filename  string
		audio     []byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/audio/transcriptions" {
			t.Errorf("path = %q, want /v2/audio/transcriptions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("parse Content-Type: %v", err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				t.Fatalf("read multipart: %v", nextErr)
			}
			partNames = append(partNames, part.FormName())
			data, readErr := io.ReadAll(part)
			if readErr != nil {
				t.Fatalf("read part %q: %v", part.FormName(), readErr)
			}
			if part.FormName() == "file" {
				filename = part.FileName()
				audio = data
			} else {
				fields[part.FormName()] = string(data)
			}
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"text":"GoModel routes requests reliably."}`)
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:          "cohere-transcribe-03-2026",
		Filename:       "sample.wav",
		FileReader:     bytes.NewBufferString("wave-bytes"),
		Language:       "en",
		ResponseFormat: "json",
		Temperature:    "0.2",
	})
	if err != nil {
		t.Fatalf("CreateTranscription() error = %v", err)
	}

	wantPartNames := []string{"model", "language", "temperature", "file"}
	if !reflect.DeepEqual(partNames, wantPartNames) {
		t.Fatalf("multipart fields = %#v, want %#v", partNames, wantPartNames)
	}
	wantFields := map[string]string{
		"model":       "cohere-transcribe-03-2026",
		"language":    "en",
		"temperature": "0.2",
	}
	if !reflect.DeepEqual(fields, wantFields) {
		t.Fatalf("multipart values = %#v, want %#v", fields, wantFields)
	}
	if filename != "sample.wav" || string(audio) != "wave-bytes" {
		t.Fatalf("file = %q/%q", filename, audio)
	}
	if resp.ContentType != "application/json; charset=utf-8" {
		t.Fatalf("ContentType = %q", resp.ContentType)
	}
	if string(resp.Data) != `{"text":"GoModel routes requests reliably."}` {
		t.Fatalf("Data = %s", resp.Data)
	}
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
