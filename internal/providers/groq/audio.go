package groq

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// vendorTranscriptionMember is the Groq-specific member (request id and seed)
// added to JSON transcription and translation bodies. OpenAI clients do not
// expect it, so the gateway returns the OpenAI shape without it.
const vendorTranscriptionMember = "x_groq"

// CreateTranscription transcribes audio through Groq's OpenAI-compatible
// /audio/transcriptions API (whisper models), normalized to the OpenAI
// response shape.
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	return normalizeTranscription(p.compat.CreateTranscription(ctx, req))
}

// CreateTranslation translates audio through Groq's OpenAI-compatible
// /audio/translations API (whisper models), normalized to the OpenAI
// response shape.
func (p *Provider) CreateTranslation(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	return normalizeTranscription(p.compat.CreateTranslation(ctx, req))
}

// normalizeTranscription drops the vendor member from JSON bodies (json and
// verbose_json). Text, SRT and VTT bodies are returned untouched.
func normalizeTranscription(resp *core.AudioResponse, err error) (*core.AudioResponse, error) {
	if err != nil || resp == nil || !strings.Contains(resp.ContentType, "json") {
		return resp, err
	}
	resp.Data = withoutJSONMember(resp.Data, vendorTranscriptionMember)
	return resp, nil
}

// withoutJSONMember returns data with the named top-level object member
// removed, preserving the order and raw representation of the others. Bodies
// that are not a JSON object, or that do not carry the member, are returned
// unchanged.
func withoutJSONMember(data []byte, member string) []byte {
	if !bytes.Contains(data, []byte(`"`+member+`"`)) {
		return data
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if delim, ok := tok.(json.Delim); err != nil || !ok || delim != '{' {
		return data
	}

	var buf bytes.Buffer
	buf.Grow(len(data))
	buf.WriteByte('{')
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return data
		}
		key, ok := keyToken.(string)
		if !ok {
			return data
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return data
		}
		if key == member {
			continue
		}
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return data
		}
		buf.Write(encodedKey)
		buf.WriteByte(':')
		buf.Write(value)
	}
	// dec.More() also stops at a truncated body, so require the closing
	// delimiter and nothing but whitespace after it before rewriting.
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return data
	}
	if _, err := dec.Token(); err != io.EOF {
		return data
	}
	buf.WriteByte('}')
	return buf.Bytes()
}
