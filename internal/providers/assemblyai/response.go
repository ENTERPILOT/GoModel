package assemblyai

import (
	"net/http"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// durationUsage is OpenAI's usage object for duration-billed transcription
// models. Reporting AssemblyAI's own audio duration lets the gateway meter
// the call from the provider's number instead of measuring the upload.
type durationUsage struct {
	Type    string  `json:"type"`
	Seconds float64 `json:"seconds"`
}

func newDurationUsage(job *transcript) *durationUsage {
	if job.AudioDuration <= 0 {
		return nil
	}
	return &durationUsage{Type: "duration", Seconds: job.AudioDuration}
}

// verboseSegment is OpenAI's verbose_json segment. AssemblyAI has no
// equivalent of the Whisper decoding fields (seek, tokens, temperature,
// avg_logprob, compression_ratio, no_speech_prob); they are sent as zero
// values so clients that decode the full OpenAI shape keep working.
type verboseSegment struct {
	ID               int     `json:"id"`
	Seek             int     `json:"seek"`
	Start            float64 `json:"start"`
	End              float64 `json:"end"`
	Text             string  `json:"text"`
	Tokens           []int   `json:"tokens"`
	Temperature      float64 `json:"temperature"`
	AvgLogprob       float64 `json:"avg_logprob"`
	CompressionRatio float64 `json:"compression_ratio"`
	NoSpeechProb     float64 `json:"no_speech_prob"`
}

type verboseWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// seconds converts AssemblyAI's millisecond timestamps to OpenAI's seconds.
func seconds(milliseconds float64) float64 {
	return milliseconds / 1000
}

// transcriptionResponse shapes a completed transcript into the OpenAI text or
// json response format.
func transcriptionResponse(format string, job *transcript) (*core.AudioResponse, error) {
	if format == "text" {
		return &core.AudioResponse{
			ContentType: core.TranscriptionResponseContentType(format),
			Data:        []byte(job.Text),
		}, nil
	}
	return encodeTranscription(format, struct {
		Text  string         `json:"text"`
		Usage *durationUsage `json:"usage,omitempty"`
	}{Text: job.Text, Usage: newDurationUsage(job)})
}

// verboseTranscriptionResponse shapes a completed transcript into OpenAI's
// verbose_json: sentences become segments and words keep their own timing.
func verboseTranscriptionResponse(job *transcript, sentences []transcriptSpan, withSegments, withWords bool) (*core.AudioResponse, error) {
	duration := job.AudioDuration
	if duration <= 0 && len(job.Words) > 0 {
		duration = seconds(job.Words[len(job.Words)-1].End)
	}
	verbose := struct {
		Task     string           `json:"task"`
		Language string           `json:"language"`
		Duration float64          `json:"duration"`
		Text     string           `json:"text"`
		Segments []verboseSegment `json:"segments,omitempty"`
		Words    []verboseWord    `json:"words,omitempty"`
		Usage    *durationUsage   `json:"usage,omitempty"`
	}{
		Task:     "transcribe",
		Language: job.LanguageCode,
		Duration: duration,
		Text:     job.Text,
		Usage:    newDurationUsage(job),
	}
	if withSegments {
		verbose.Segments = make([]verboseSegment, 0, len(sentences))
		for i, sentence := range sentences {
			verbose.Segments = append(verbose.Segments, verboseSegment{
				ID:     i,
				Start:  seconds(sentence.Start),
				End:    seconds(sentence.End),
				Text:   sentence.Text,
				Tokens: []int{},
			})
		}
	}
	if withWords {
		verbose.Words = make([]verboseWord, 0, len(job.Words))
		for _, word := range job.Words {
			verbose.Words = append(verbose.Words, verboseWord{
				Word:  word.Text,
				Start: seconds(word.Start),
				End:   seconds(word.End),
			})
		}
	}
	return encodeTranscription("verbose_json", verbose)
}

func encodeTranscription(format string, payload any) (*core.AudioResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, core.NewProviderError("assemblyai", http.StatusBadGateway, "failed to encode transcription response", err)
	}
	return &core.AudioResponse{ContentType: core.TranscriptionResponseContentType(format), Data: body}, nil
}
