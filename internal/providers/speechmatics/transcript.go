package speechmatics

import (
	"bytes"
	"net/http"
	"regexp"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// transcript is the part of Speechmatics' json-v2 transcript the gateway reads.
type transcript struct {
	Job struct {
		// Duration is the audio length in whole seconds.
		Duration float64 `json:"duration"`
	} `json:"job"`
	Metadata struct {
		LanguagePackInfo struct {
			// WordDelimiter is a pointer because languages written without
			// spaces report an empty delimiter, which must not read as unset.
			WordDelimiter *string `json:"word_delimiter"`
			// PerLanguageWordDelimiters replaces WordDelimiter in a
			// multilingual transcript.
			PerLanguageWordDelimiters map[string]string `json:"per_language_word_delimiters"`
		} `json:"language_pack_info"`
	} `json:"metadata"`
	Results []struct {
		Type      string  `json:"type"`
		StartTime float64 `json:"start_time"`
		EndTime   float64 `json:"end_time"`
		// IsEOS marks a punctuation mark that ends a sentence.
		IsEOS bool `json:"is_eos"`
		// AttachesTo says which neighbor a punctuation mark joins without a
		// delimiter: "previous", "next", "both", or "none".
		AttachesTo   string `json:"attaches_to"`
		Alternatives []struct {
			Content  string `json:"content"`
			Language string `json:"language"`
		} `json:"alternatives"`
	} `json:"results"`
}

// token is one word or punctuation mark of the transcript, taken from the
// most likely alternative.
type token struct {
	content     string
	language    string
	start, end  float64
	punctuation bool
	endsSegment bool
	// joinsPrevious and joinsNext drop the delimiter on that side.
	joinsPrevious, joinsNext bool
}

// tokens flattens the results into words and punctuation. Speechmatics may add
// other item types (entities, future ones) and asks clients to ignore the ones
// they do not recognize.
func (t *transcript) tokens() []token {
	tokens := make([]token, 0, len(t.Results))
	for _, result := range t.Results {
		if len(result.Alternatives) == 0 || result.Alternatives[0].Content == "" {
			continue
		}
		tok := token{
			content:  result.Alternatives[0].Content,
			language: result.Alternatives[0].Language,
			start:    result.StartTime,
			end:      result.EndTime,
		}
		switch result.Type {
		case "word":
		case "punctuation":
			tok.punctuation = true
			tok.endsSegment = result.IsEOS
			// A mark without attaches_to follows its word, as "." and "," do.
			switch result.AttachesTo {
			case "", "previous":
				tok.joinsPrevious = true
			case "next":
				tok.joinsNext = true
			case "both":
				tok.joinsPrevious, tok.joinsNext = true, true
			}
		default:
			continue
		}
		tokens = append(tokens, tok)
	}
	return tokens
}

// delimiter is what separates a token in the given language from the one
// before it: a space for most languages, nothing for Japanese or Mandarin.
func (t *transcript) delimiter(language string) string {
	info := t.Metadata.LanguagePackInfo
	if delimiter, ok := info.PerLanguageWordDelimiters[language]; ok {
		return delimiter
	}
	if info.WordDelimiter != nil {
		return *info.WordDelimiter
	}
	return " "
}

// text joins tokens into readable text, attaching punctuation to its word.
func (t *transcript) text(tokens []token) string {
	var b strings.Builder
	joined := true // nothing precedes the first token
	for _, tok := range tokens {
		if !joined && !tok.joinsPrevious {
			b.WriteString(t.delimiter(tok.language))
		}
		b.WriteString(tok.content)
		joined = tok.joinsNext
	}
	return b.String()
}

type verboseSegment struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

type verboseWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// segments splits the transcript into sentences: Speechmatics has no segment
// unit of its own, and marks the punctuation that ends a sentence. Only the
// timing and text are reported; Whisper's decoder statistics (tokens,
// avg_logprob, ...) have no Speechmatics counterpart.
func (t *transcript) segments(tokens []token) []verboseSegment {
	segments := []verboseSegment{}
	start := 0
	for i, tok := range tokens {
		if !tok.endsSegment && i != len(tokens)-1 {
			continue
		}
		part := tokens[start : i+1]
		segments = append(segments, verboseSegment{
			ID:    len(segments),
			Start: part[0].start,
			End:   part[len(part)-1].end,
			Text:  t.text(part),
		})
		start = i + 1
	}
	return segments
}

func words(tokens []token) []verboseWord {
	words := make([]verboseWord, 0, len(tokens))
	for _, tok := range tokens {
		if !tok.punctuation {
			words = append(words, verboseWord{Word: tok.content, Start: tok.start, End: tok.end})
		}
	}
	return words
}

// transcriptionResponse shapes a finished job's transcript into the OpenAI
// response_format the caller asked for. body is Speechmatics' SRT for the
// subtitle formats and its json-v2 transcript otherwise.
func transcriptionResponse(provider string, req *core.AudioTranscriptionRequest, format string, body []byte) (*core.AudioResponse, error) {
	contentType := core.TranscriptionResponseContentType(format)
	switch format {
	case "srt":
		return &core.AudioResponse{ContentType: contentType, Data: body}, nil
	case "vtt":
		return &core.AudioResponse{ContentType: contentType, Data: srtToVTT(body)}, nil
	}

	var upstream transcript
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, core.NewProviderError(provider, http.StatusBadGateway, "failed to parse speechmatics transcript", err)
	}
	tokens := upstream.tokens()
	text := upstream.text(tokens)

	var payload any
	switch format {
	case "text":
		return &core.AudioResponse{ContentType: contentType, Data: []byte(text)}, nil
	case "verbose_json":
		payload = verboseTranscription(req, &upstream, tokens, text)
	default:
		payload = map[string]string{"text": text}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, core.NewProviderError(provider, http.StatusBadGateway, "failed to encode transcription response", err)
	}
	return &core.AudioResponse{ContentType: contentType, Data: data}, nil
}

type verboseResponse struct {
	Task     string           `json:"task"`
	Language string           `json:"language"`
	Duration float64          `json:"duration"`
	Text     string           `json:"text"`
	Segments []verboseSegment `json:"segments"`
	Words    []verboseWord    `json:"words,omitempty"`
}

// verboseTranscription builds the OpenAI verbose_json body. Segments are
// always included, as OpenAI's default granularity is the segment; words are
// added when the caller asked for the word granularity.
func verboseTranscription(req *core.AudioTranscriptionRequest, upstream *transcript, tokens []token, text string) verboseResponse {
	resp := verboseResponse{
		Task:     "transcribe",
		Language: strings.TrimSpace(req.Language),
		Duration: upstream.Job.Duration,
		Text:     text,
		Segments: upstream.segments(tokens),
	}
	if len(tokens) > 0 {
		// Each word names the language it was transcribed in, which is how an
		// identified ("auto") language is reported.
		if language := tokens[0].language; language != "" {
			resp.Language = language
		}
		if resp.Duration <= 0 {
			resp.Duration = tokens[len(tokens)-1].end
		}
	}
	for _, granularity := range req.TimestampGranularities {
		if strings.EqualFold(strings.TrimSpace(granularity), "word") {
			resp.Words = words(tokens)
			break
		}
	}
	return resp
}

// srtTiming matches an SRT cue timing line, whose only difference from
// WebVTT's is the comma before the milliseconds.
var srtTiming = regexp.MustCompile(`(?m)^(\d+:\d{2}:\d{2}),(\d{3}) --> (\d+:\d{2}:\d{2}),(\d{3})`)

// srtToVTT converts Speechmatics' SRT subtitles to WebVTT, which it does not
// produce itself. The cues keep their numbers, which WebVTT reads as cue
// identifiers.
func srtToVTT(srt []byte) []byte {
	srt = bytes.TrimPrefix(srt, []byte("\xef\xbb\xbf"))
	srt = bytes.ReplaceAll(srt, []byte("\r\n"), []byte("\n"))
	cues := srtTiming.ReplaceAll(bytes.TrimSpace(srt), []byte("${1}.${2} --> ${3}.${4}"))

	vtt := []byte("WEBVTT\n")
	if len(cues) > 0 {
		vtt = append(vtt, '\n')
		vtt = append(vtt, cues...)
		vtt = append(vtt, '\n')
	}
	return vtt
}
