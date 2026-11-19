package speechmatics

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestTranscriptText_JoinsWordsAndPunctuation(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		results  string
		want     string
	}{
		{
			name: "punctuation without attaches_to follows its word",
			results: `[
				{"type":"word","alternatives":[{"content":"Hello"}]},
				{"type":"word","alternatives":[{"content":"world"}]},
				{"type":"punctuation","alternatives":[{"content":"."}]}
			]`,
			want: "Hello world.",
		},
		{
			name: "attaches_to decides the side",
			results: `[
				{"type":"punctuation","attaches_to":"next","alternatives":[{"content":"¿"}]},
				{"type":"word","alternatives":[{"content":"Qué"}]},
				{"type":"punctuation","attaches_to":"none","alternatives":[{"content":"-"}]},
				{"type":"word","alternatives":[{"content":"tal"}]},
				{"type":"punctuation","attaches_to":"both","alternatives":[{"content":"'"}]},
				{"type":"word","alternatives":[{"content":"s"}]},
				{"type":"punctuation","attaches_to":"previous","alternatives":[{"content":"?"}]}
			]`,
			want: "¿Qué - tal's?",
		},
		{
			name:     "a language written without spaces has an empty delimiter",
			metadata: `{"language_pack_info":{"word_delimiter":""}}`,
			results: `[
				{"type":"word","alternatives":[{"content":"こんにちは"}]},
				{"type":"word","alternatives":[{"content":"世界"}]},
				{"type":"punctuation","alternatives":[{"content":"。"}]}
			]`,
			want: "こんにちは世界。",
		},
		{
			name:     "a multilingual transcript delimits per language",
			metadata: `{"language_pack_info":{"per_language_word_delimiters":{"en":" ","ja":""}}}`,
			results: `[
				{"type":"word","alternatives":[{"content":"Hello","language":"en"}]},
				{"type":"word","alternatives":[{"content":"there","language":"en"}]},
				{"type":"word","alternatives":[{"content":"世界","language":"ja"}]}
			]`,
			want: "Hello there世界",
		},
		{
			name: "unrecognized items and empty alternatives are skipped",
			results: `[
				{"type":"word","alternatives":[{"content":"Call"}]},
				{"type":"entity","alternatives":[{"content":"$100 million"}]},
				{"type":"word","alternatives":[]},
				{"type":"word","alternatives":[{"content":"me"},{"content":"knee"}]}
			]`,
			want: "Call me",
		},
		{name: "no speech", results: `[]`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := tt.metadata
			if metadata == "" {
				metadata = `{}`
			}
			var upstream transcript
			require.NoError(t, json.Unmarshal([]byte(`{"metadata":`+metadata+`,"results":`+tt.results+`}`), &upstream))

			assert.Equal(t, tt.want, upstream.text(upstream.tokens()))
		})
	}
}

func TestTranscriptionResponse_VerboseJSONFallbacks(t *testing.T) {
	// No job duration, no sentence-ending punctuation, and an identified language.
	const body = `{"results":[
		{"type":"word","start_time":0.2,"end_time":0.6,"alternatives":[{"content":"Guten","language":"de"}]},
		{"type":"word","start_time":0.7,"end_time":1.25,"alternatives":[{"content":"Tag","language":"de"}]}
	]}`
	req := &core.AudioTranscriptionRequest{ResponseFormat: "verbose_json"}

	resp, err := transcriptionResponse("speechmatics", req, "verbose_json", []byte(body))
	require.NoError(t, err)

	assert.JSONEq(t, `{
		"task": "transcribe",
		"language": "de",
		"duration": 1.25,
		"text": "Guten Tag",
		"segments": [{"id": 0, "start": 0.2, "end": 1.25, "text": "Guten Tag"}]
	}`, string(resp.Data))
}

func TestTranscriptionResponse_VerboseJSONWithoutSpeech(t *testing.T) {
	req := &core.AudioTranscriptionRequest{Language: "en", TimestampGranularities: []string{"word"}}

	resp, err := transcriptionResponse("speechmatics", req, "verbose_json", []byte(`{"job":{"duration":3},"results":[]}`))
	require.NoError(t, err)

	assert.JSONEq(t, `{"task":"transcribe","language":"en","duration":3,"text":"","segments":[]}`, string(resp.Data))
}

func TestTranscriptionResponse_RejectsAnUnreadableTranscript(t *testing.T) {
	_, err := transcriptionResponse("speechmatics", &core.AudioTranscriptionRequest{}, "json", []byte(`<html>`))

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, core.ErrorTypeProvider, gatewayErr.Type)
}

func TestSRTToVTT(t *testing.T) {
	tests := []struct {
		name string
		srt  string
		want string
	}{
		{
			name: "rewrites the timing lines only",
			srt:  "1\n00:00:01,000 --> 00:00:02,500\nIt costs 1,000 --> a lot\n\n2\n01:02:03,004 --> 01:02:05,006\nSecond line\none more\n",
			want: "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\nIt costs 1,000 --> a lot\n\n2\n01:02:03.004 --> 01:02:05.006\nSecond line\none more\n",
		},
		{
			name: "normalizes a byte order mark and CRLF line endings",
			srt:  "\xef\xbb\xbf1\r\n00:00:00,000 --> 00:00:01,000\r\nHi\r\n\r\n",
			want: "WEBVTT\n\n1\n00:00:00.000 --> 00:00:01.000\nHi\n",
		},
		{name: "no speech", srt: "", want: "WEBVTT\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, string(srtToVTT([]byte(tt.srt))))
		})
	}
}
