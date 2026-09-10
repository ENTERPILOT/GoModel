package pluginapi

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestDecodeMedia(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G'}
	b64 := base64.StdEncoding.EncodeToString(png)
	tests := []struct {
		name     string
		part     Part
		want     []byte
		wantType string
		wantOK   bool
	}{
		{"image data uri", Part{Kind: PartImage, URL: "data:image/png;base64," + b64}, png, "image/png", true},
		{"image data uri with charset param", Part{Kind: PartImage, URL: "data:image/png;charset=binary;base64," + b64}, png, "image/png", true},
		{"image remote url", Part{Kind: PartImage, URL: "https://x/y.png"}, nil, "", false},
		{"image not base64", Part{Kind: PartImage, URL: "data:image/png," + b64}, nil, "", false},
		{"audio base64", Part{Kind: PartAudio, Data: []byte(b64), MediaType: "audio/wav"}, png, "audio/wav", true},
		{"audio data uri", Part{Kind: PartAudio, Data: []byte("data:audio/mp3;base64," + b64)}, png, "audio/mp3", true},
		{"audio garbage", Part{Kind: PartAudio, Data: []byte("***")}, nil, "", false},
		{"text", Part{Kind: PartText, Text: b64}, nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, mediaType, ok := tt.part.DecodeMedia()
			if ok != tt.wantOK || string(data) != string(tt.want) || mediaType != tt.wantType {
				t.Fatalf("DecodeMedia() = %q, %q, %v; want %q, %q, %v", data, mediaType, ok, tt.want, tt.wantType, tt.wantOK)
			}
		})
	}
}

func TestPromptSetMedia(t *testing.T) {
	redacted := []byte("redacted-bytes")
	p := toolPrompt()
	p.Messages[1].Parts[1].Raw = []byte(`{"type":"input_image","image_url":"https://x/y.png"}`)
	p.Messages = append(p.Messages, Message{ID: "m5", Role: RoleUser, Parts: []Part{{Kind: PartAudio, Data: []byte("AAAA"), MediaType: "audio/wav"}}})

	if err := p.SetMedia("m1", 1, "image/jpeg", redacted); err != nil {
		t.Fatal(err)
	}
	img := p.Messages[1].Parts[1]
	if img.URL != "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(redacted) || img.MediaType != "image/jpeg" || img.Data != nil || img.Raw != nil {
		t.Fatalf("image part after SetMedia = %+v", img)
	}
	if data, mediaType, ok := img.DecodeMedia(); !ok || string(data) != string(redacted) || mediaType != "image/jpeg" {
		t.Fatalf("DecodeMedia() after SetMedia = %q, %q, %v", data, mediaType, ok)
	}

	if err := p.SetMedia("m5", 0, " audio/mp3 ", redacted); err != nil {
		t.Fatal(err)
	}
	audio := p.Messages[5].Parts[0]
	if string(audio.Data) != base64.StdEncoding.EncodeToString(redacted) || audio.MediaType != "audio/mp3" || audio.URL != "" {
		t.Fatalf("audio part after SetMedia = %+v", audio)
	}
	ch := p.Changes()
	if !ch.Dirty || ch.Messages["m1"] != ChangeEdited || ch.Messages["m5"] != ChangeEdited || len(ch.Messages) != 2 {
		t.Fatalf("changes = %+v", ch)
	}
}

func TestPromptSetMediaErrors(t *testing.T) {
	tests := []struct {
		name      string
		msgID     string
		partIdx   int
		mediaType string
		data      []byte
		want      string
	}{
		{"unknown message", "zz", 0, "image/png", []byte("x"), "unknown message"},
		{"part out of range", "m1", 7, "image/png", []byte("x"), "no part 7"},
		{"text part", "m1", 0, "image/png", []byte("x"), "not image or audio"},
		{"tool call part", "m2", 0, "image/png", []byte("x"), "not image or audio"},
		{"bad media type", "m1", 1, "png", []byte("x"), "not a MIME type"},
		{"audio type on an image part", "m1", 1, "audio/wav", []byte("x"), "does not fit the image part"},
		{"image type on an audio part", "m5", 0, "image/png", []byte("x"), "does not fit the audio part"},
		{"empty data", "m1", 1, "image/png", nil, "is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := toolPrompt()
			p.Messages = append(p.Messages, Message{ID: "m5", Role: RoleUser, Parts: []Part{{Kind: PartAudio, Data: []byte("AAAA"), MediaType: "audio/wav"}}})
			err := p.SetMedia(tt.msgID, tt.partIdx, tt.mediaType, tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
			if p.Changes().Dirty {
				t.Error("failed edit must not dirty the prompt")
			}
			if p.Messages[1].Parts[1].URL != "https://x/y.png" {
				t.Error("failed edit must leave the part alone")
			}
		})
	}
}
