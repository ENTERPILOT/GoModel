package groq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestCreateTranscription_StripsVendorMember(t *testing.T) {
	tests := []struct {
		name     string
		format   string
		upstream string
		want     string
	}{
		{
			name:     "json",
			format:   "json",
			upstream: `{"text":"hello","x_groq":{"id":"req_1"}}`,
			want:     `{"text":"hello"}`,
		},
		{
			name:     "verbose_json keeps member order and nested values",
			format:   "verbose_json",
			upstream: `{"task":"transcribe","x_groq":{"id":"req_1","seed":7},"duration":2.9,"segments":[{"id":0,"text":"hello"}]}`,
			want:     `{"task":"transcribe","duration":2.9,"segments":[{"id":0,"text":"hello"}]}`,
		},
		{
			name:     "default response_format is json",
			upstream: `{"text":"hello","x_groq":{"id":"req_1"}}`,
			want:     `{"text":"hello"}`,
		},
		{
			name:     "json without the member is untouched",
			format:   "json",
			upstream: `{"text":"hello"}`,
			want:     `{"text":"hello"}`,
		},
		{
			name:     "text body is proxied verbatim",
			format:   "text",
			upstream: "hello x_groq",
			want:     "hello x_groq",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, endpoint := range []string{"/audio/transcriptions", "/audio/translations"} {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != endpoint {
						t.Errorf("path = %q, want %q", r.URL.Path, endpoint)
					}
					_, _ = w.Write([]byte(tt.upstream))
				}))
				provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
				provider.SetBaseURL(server.URL)

				req := &core.AudioTranscriptionRequest{
					Model:          "whisper-large-v3-turbo",
					File:           []byte("audio"),
					Filename:       "a.mp3",
					ResponseFormat: tt.format,
				}
				var (
					resp *core.AudioResponse
					err  error
				)
				if strings.HasSuffix(endpoint, "transcriptions") {
					resp, err = provider.CreateTranscription(context.Background(), req)
				} else {
					resp, err = provider.CreateTranslation(context.Background(), req)
				}
				server.Close()
				if err != nil {
					t.Fatalf("%s error = %v", endpoint, err)
				}
				if got := string(resp.Data); got != tt.want {
					t.Errorf("%s body = %s, want %s", endpoint, got, tt.want)
				}
			}
		})
	}
}

func TestCreateTranscription_PropagatesUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad audio","type":"invalid_request_error"}}`))
	}))
	defer server.Close()
	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "whisper-large-v3-turbo",
		File:     []byte("audio"),
		Filename: "a.mp3",
	})
	if err == nil {
		t.Fatalf("CreateTranscription() error = nil, want the upstream error (resp = %+v)", resp)
	}
	if resp != nil {
		t.Errorf("response = %+v, want nil", resp)
	}
	if !strings.Contains(err.Error(), "bad audio") {
		t.Errorf("error = %v, want it to carry the upstream message", err)
	}
}

func TestWithoutJSONMember_LeavesMalformedBodiesAlone(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "not an object", body: `["x_groq"]`},
		{name: "truncated object", body: `{"text":"hi","x_groq":`},
		{name: "missing closing delimiter", body: `{"text":"hi","x_groq":{"id":"req_1"}`},
		{name: "trailing data", body: `{"text":"hi","x_groq":{"id":"req_1"}} oops`},
		{name: "empty", body: ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(withoutJSONMember([]byte(tt.body), vendorTranscriptionMember)); got != tt.body {
				t.Errorf("withoutJSONMember() = %q, want %q", got, tt.body)
			}
		})
	}
}
