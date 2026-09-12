package openai

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/goccy/go-json"
)

// TestCreateTranscription_ForwardsExtraFormFields checks that fields the gateway
// does not consume itself reach the upstream multipart body unchanged
// (ADR-0011 rule 1), including repeated values, and that a reserved name cannot
// be duplicated through the passthrough list.
func TestCreateTranscription_ForwardsExtraFormFields(t *testing.T) {
	var got map[string][]string
	provider := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		got = r.MultipartForm.Value
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hi"}`))
	})

	_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "gpt-4o-transcribe",
		Filename: "speech.wav",
		File:     []byte("wave-bytes"),
		Fields: []core.FormField{
			{Name: "include[]", Value: "logprobs"},
			{Name: "chunking_strategy", Value: "auto"},
			{Name: "x_vendor", Value: "a"},
			{Name: "x_vendor", Value: "b"},
			// A reserved name must never be re-emitted from the extras.
			{Name: "model", Value: "evil"},
		},
	})
	if err != nil {
		t.Fatalf("CreateTranscription() error = %v", err)
	}
	if want := []string{"logprobs"}; !reflect.DeepEqual(got["include[]"], want) {
		t.Errorf("include[] = %v, want %v", got["include[]"], want)
	}
	if want := []string{"auto"}; !reflect.DeepEqual(got["chunking_strategy"], want) {
		t.Errorf("chunking_strategy = %v, want %v", got["chunking_strategy"], want)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(got["x_vendor"], want) {
		t.Errorf("x_vendor = %v, want %v", got["x_vendor"], want)
	}
	if want := []string{"gpt-4o-transcribe"}; !reflect.DeepEqual(got["model"], want) {
		t.Errorf("model = %v, want %v (extras must not overwrite it)", got["model"], want)
	}
}

// TestCreateTranslation_ForwardsExtraFormFields covers the same passthrough on
// the translations endpoint, which shares the multipart builder.
func TestCreateTranslation_ForwardsExtraFormFields(t *testing.T) {
	var got map[string][]string
	provider := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		got = r.MultipartForm.Value
		_, _ = w.Write([]byte(`{"text":"hi"}`))
	})

	_, err := provider.CreateTranslation(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "whisper-1",
		Filename: "speech.wav",
		File:     []byte("wave-bytes"),
		Fields:   []core.FormField{{Name: "x_vendor", Value: "a"}},
	})
	if err != nil {
		t.Fatalf("CreateTranslation() error = %v", err)
	}
	if want := []string{"a"}; !reflect.DeepEqual(got["x_vendor"], want) {
		t.Errorf("x_vendor = %v, want %v", got["x_vendor"], want)
	}
}

// TestCreateTranscription_HostileFieldNamesKeepMultipartFraming pins the
// framing guarantee of the passthrough path: a forwarded field name or value is
// client input, so it must stay inside its own part. Each case would otherwise
// let a caller graft a second gateway-controlled part onto the upstream body.
func TestCreateTranscription_HostileFieldNamesKeepMultipartFraming(t *testing.T) {
	tests := []struct {
		name  string
		field core.FormField
	}{
		{"crlf in name", core.FormField{
			Name:  "evil\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nhijacked\r\n",
			Value: "v",
		}},
		{"quote escape promoting to a file part", core.FormField{
			Name: "x\"; filename=\"boom.txt", Value: "v",
		}},
		{"forged boundary in value", core.FormField{
			Name:  "ok",
			Value: "v\r\n--boundary\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nhijacked",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var form map[string][]string
			provider := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseMultipartForm(1 << 20); err != nil {
					t.Fatalf("upstream could not parse the multipart body: %v", err)
				}
				form = r.MultipartForm.Value
				if len(r.MultipartForm.File) != 1 {
					t.Errorf("upstream file parts = %d, want only the audio", len(r.MultipartForm.File))
				}
				_, _ = w.Write([]byte(`{"text":"hi"}`))
			})

			if _, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
				Model:    "gpt-4o-transcribe",
				Filename: "speech.wav",
				File:     []byte("wave-bytes"),
				Fields:   []core.FormField{tt.field},
			}); err != nil {
				t.Fatalf("CreateTranscription() error = %v", err)
			}
			if want := []string{"gpt-4o-transcribe"}; !reflect.DeepEqual(form["model"], want) {
				t.Errorf("model = %v, want %v (the hostile field must not forge a part)", form["model"], want)
			}
		})
	}
}

// TestCreateTranscription_StreamedResponseKeepsEventStreamType ensures a
// forwarded stream=true is not mislabelled as JSON: the client needs the
// upstream text/event-stream type to parse the body it gets back.
func TestCreateTranscription_StreamedResponseKeepsEventStreamType(t *testing.T) {
	provider := newSpeechTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = w.Write([]byte("data: {\"type\":\"transcript.text.delta\"}\n\n"))
	})

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "gpt-4o-transcribe",
		Filename: "speech.wav",
		File:     []byte("wave-bytes"),
		Fields:   []core.FormField{{Name: "stream", Value: "true"}},
	})
	if err != nil {
		t.Fatalf("CreateTranscription() error = %v", err)
	}
	if resp.ContentType != "text/event-stream; charset=utf-8" {
		t.Errorf("ContentType = %q, want the upstream event-stream type", resp.ContentType)
	}
}

// TestCreateSpeech_ForwardsUnknownJSONFields checks the JSON audio path: an
// unknown parameter (OpenAI's stream_format) is merged into the upstream body.
func TestCreateSpeech_ForwardsUnknownJSONFields(t *testing.T) {
	var body map[string]any
	provider := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode upstream body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"speech.audio.delta\"}\n\n"))
	})

	req, err := core.DecodeAudioSpeechRequest([]byte(
		`{"model":"gpt-4o-mini-tts","input":"hi","voice":"alloy","stream_format":"sse"}`), nil)
	if err != nil {
		t.Fatalf("DecodeAudioSpeechRequest() error = %v", err)
	}
	resp, err := provider.CreateSpeech(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateSpeech() error = %v", err)
	}
	if body["stream_format"] != "sse" {
		t.Errorf("upstream body = %+v, want stream_format=sse", body)
	}
	if body["model"] != "gpt-4o-mini-tts" || body["input"] != "hi" || body["voice"] != "alloy" {
		t.Errorf("upstream body lost typed fields: %+v", body)
	}
	// The upstream answered with a stream; its Content-Type must survive so the
	// client can parse the events.
	if resp.ContentType != "text/event-stream" {
		t.Errorf("ContentType = %q, want text/event-stream", resp.ContentType)
	}
}
