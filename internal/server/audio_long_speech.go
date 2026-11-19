package server

import (
	"context"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/longspeech"
)

// synthesizeSpeech sends a text-to-speech request upstream: as it is, or, when
// its input is longer than one provider request may carry, part by part with
// the audio joined into one response. It also returns the text the provider
// was sent, which is what the usage entry counts characters from: one entry
// for the whole request, however many upstream calls it took.
func (s *audioService) synthesizeSpeech(ctx context.Context, router core.AudioProvider, providerName string, req *core.AudioSpeechRequest) (*core.AudioResponse, string, error) {
	parts := s.speechParts(req)
	if len(parts) == 0 {
		resp, err := router.CreateSpeech(ctx, req)
		return resp, req.Input, err
	}
	resp, err := longspeech.Join(ctx, providerName, parts, func(ctx context.Context, text string) (*core.AudioResponse, error) {
		part := *req
		part.Input = text
		return router.CreateSpeech(ctx, &part)
	})
	return resp, strings.Join(parts, ""), err
}

// speechParts returns the parts a long speech input is synthesized in, or nil
// when the request goes upstream unchanged: splitting is off, the input fits
// in one request, or the audio could not be joined afterwards.
func (s *audioService) speechParts(req *core.AudioSpeechRequest) []string {
	// A string has at least as many bytes as characters, so input within the
	// limit in bytes is within it in characters and is never scanned.
	if s.speechChunkChars <= 0 || len(req.Input) <= s.speechChunkChars {
		return nil
	}
	if !longspeech.Joinable(req.ResponseFormat) || speechStreamsEvents(req) {
		return nil
	}
	parts := longspeech.Split(req.Input, s.speechChunkChars)
	if len(parts) < 2 {
		return nil
	}
	return parts
}

// speechStreamsEvents reports whether the client asked for the audio as
// server-sent events (stream_format: "sse"), which wrap each piece of audio in
// an event and close with a usage event, so they are not joinable bytes.
func speechStreamsEvents(req *core.AudioSpeechRequest) bool {
	format := req.ExtraFields.Lookup("stream_format")
	return format != nil && string(format) != `"audio"`
}
