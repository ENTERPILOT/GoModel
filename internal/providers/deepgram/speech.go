package deepgram

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// speechFormat maps an OpenAI-compatible response_format to Deepgram's
// encoding, container and sample_rate query parameters. Every OpenAI format has
// a Deepgram encoding. pcm pins 24 kHz, the rate OpenAI's raw pcm uses and the
// gateway meters it at; the other formats keep Deepgram's own rate.
func speechFormat(responseFormat string) (format string, query url.Values, err error) {
	format = strings.ToLower(strings.TrimSpace(responseFormat))
	if format == "" {
		format = "mp3"
	}
	switch format {
	case "mp3", "opus", "aac", "flac":
		query = url.Values{"encoding": {format}}
	case "wav":
		query = url.Values{"encoding": {"linear16"}, "container": {"wav"}}
	case "pcm":
		query = url.Values{"encoding": {"linear16"}, "container": {"none"}, "sample_rate": {"24000"}}
	default:
		return "", nil, core.NewInvalidRequestError("deepgram speech supports mp3, opus, aac, flac, wav, or pcm response formats", nil).WithParam("response_format")
	}
	return format, query, nil
}

// speechSpeed adapts the OpenAI speed (0.25-4.0) to the 0.7-1.5 range Deepgram
// accepts in 0.05 steps: per Postel's Law the request is fitted to the provider
// rather than rejected. Zero means unset and 1 is Deepgram's default, so
// neither is sent.
func speechSpeed(speed float64) string {
	if speed == 0 {
		return ""
	}
	speed = math.Round(min(max(speed, 0.7), 1.5)*20) / 20
	if speed == 1 {
		return ""
	}
	return strconv.FormatFloat(speed, 'f', -1, 64)
}

// forwardedSpeakOptions are the /speak query parameters a client may send as
// extra request members. encoding and container stay derived from
// response_format, so the audio matches the content type the gateway labels.
var forwardedSpeakOptions = []string{"bit_rate", "sample_rate", "mip_opt_out", "tag"}

// speakOptions copies Deepgram's own /speak options from the request's extra
// members into query. Options that would change the response into something
// other than the audio (a callback, OpenAI's sse stream_format) return 400
// rather than being dropped.
func speakOptions(fields core.UnknownJSONFields, format string, query url.Values) error {
	for _, name := range []string{"callback", "callback_method"} {
		if fields.HasAny(name) {
			return core.NewInvalidRequestError(
				fmt.Sprintf("deepgram speech does not support %s on this endpoint", name), nil).WithParam(name)
		}
	}
	if raw := fields.Lookup("stream_format"); len(raw) > 0 {
		var streamFormat string
		if json.Unmarshal(raw, &streamFormat) != nil || !strings.EqualFold(strings.TrimSpace(streamFormat), "audio") {
			return core.NewInvalidRequestError("deepgram speech supports only the audio stream_format", nil).WithParam("stream_format")
		}
	}
	for _, name := range forwardedSpeakOptions {
		raw := fields.Lookup(name)
		if len(raw) == 0 {
			continue
		}
		values, ok := queryValues(raw)
		if !ok {
			return core.NewInvalidRequestError(
				fmt.Sprintf("deepgram speech option %s must be a string, number, boolean, or list of strings", name), nil).WithParam(name)
		}
		if name == "sample_rate" && format == "pcm" {
			// Raw pcm has no header, so its rate is the one OpenAI defines and
			// the gateway meters the audio at.
			if len(values) != 1 || values[0] != "24000" {
				return core.NewInvalidRequestError("deepgram speech returns pcm at 24000 Hz; use wav for another sample_rate", nil).WithParam("sample_rate")
			}
			continue
		}
		query.Del(name)
		for _, value := range values {
			query.Add(name, value)
		}
	}
	return nil
}

// queryValues renders a JSON scalar, or a list of strings, as query values.
func queryValues(raw json.RawMessage) ([]string, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	switch v := value.(type) {
	case string:
		return []string{v}, true
	case bool:
		return []string{strconv.FormatBool(v)}, true
	case float64:
		return []string{strconv.FormatFloat(v, 'f', -1, 64)}, true
	case []any:
		values := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			values = append(values, s)
		}
		return values, len(values) > 0
	}
	return nil, false
}

// splitVoiceModel splits a Deepgram voice model id into its family and its
// voice: "aura-2-thalia-en" is family "aura-2", voice "thalia-en". A bare
// family has an empty voice, and ok is false for an id outside the Aura
// naming scheme.
func splitVoiceModel(model string) (family, voice string, ok bool) {
	parts := strings.Split(model, "-")
	if parts[0] != "aura" {
		return "", "", false
	}
	n := 1
	if len(parts) > 1 {
		if _, err := strconv.ParseUint(parts[1], 10, 8); err == nil {
			n = 2
		}
	}
	return strings.Join(parts[:n], "-"), strings.Join(parts[n:], "-"), true
}

// isVoiceFamily reports whether model names a Deepgram voice family ("aura",
// "aura-2") rather than one voice of it.
func isVoiceFamily(model string) bool {
	_, voice, ok := splitVoiceModel(model)
	return ok && voice == ""
}

// speechModel resolves the model Deepgram synthesizes with. Deepgram has no
// separate voice parameter: a model id names family, voice and language
// together ("aura-2-thalia-en"). OpenAI clients send both fields, so either
// spelling is accepted: the full id as model, or the family as model with the
// rest ("thalia-en") as voice. A voice that names another voice or family than
// the model is rejected rather than dropped, which also keeps a request on the
// model it was authorized for.
func speechModel(model, voice string) (string, error) {
	model = strings.ToLower(strings.TrimSpace(model))
	voice = strings.ToLower(strings.TrimSpace(voice))
	family, own, ok := splitVoiceModel(model)
	switch {
	case voice == "":
		if ok && own == "" {
			return "", core.NewInvalidRequestError(
				fmt.Sprintf("voice is required with the deepgram model %q, for example \"thalia-en\"", model), nil).WithParam("voice")
		}
		return model, nil
	case ok && own == "":
		// A family takes the voice from the request, short ("thalia-en") or as
		// the full model id, as long as the result stays in the family.
		resolved := voice
		if _, _, isModel := splitVoiceModel(voice); !isModel {
			resolved = family + "-" + voice
		}
		if resolvedFamily, resolvedVoice, _ := splitVoiceModel(resolved); resolvedFamily == family && resolvedVoice != "" {
			return resolved, nil
		}
		return "", core.NewInvalidRequestError(
			fmt.Sprintf("voice %q is not a voice of the deepgram model %q; send it as name and language, for example \"thalia-en\"", voice, model), nil).WithParam("voice")
	case voice == model:
		return model, nil
	case ok && (voice == own || strings.HasPrefix(own, voice+"-")):
		// The voice repeats the one the model id already names, with or
		// without its language.
		return model, nil
	}
	return "", core.NewInvalidRequestError(
		fmt.Sprintf("voice %q does not match the deepgram model %q: a Deepgram model id names its own voice, so omit voice or send the voice family (for example \"aura-2\") as the model", voice, model), nil).WithParam("voice")
}

// CreateSpeech translates the OpenAI-compatible request into Deepgram's
// POST /speak, which takes the text as JSON and every option as a query
// parameter. Deepgram streams the audio as it synthesizes it, so the body is
// relayed as it arrives rather than buffered.
func (p *Provider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio speech request is required", nil)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	if strings.TrimSpace(req.Input) == "" {
		return nil, core.NewInvalidRequestError("input is required", nil)
	}
	if strings.TrimSpace(req.Instructions) != "" {
		return nil, core.NewInvalidRequestError("deepgram speech does not support instructions", nil).WithParam("instructions")
	}
	model, err := speechModel(req.Model, req.Voice)
	if err != nil {
		return nil, err
	}
	format, query, err := speechFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	if err := speakOptions(req.ExtraFields, format, query); err != nil {
		return nil, err
	}
	query.Set("model", model)
	if speed := speechSpeed(req.Speed); speed != "" {
		query.Set("speed", speed)
	}

	raw, err := p.client.DoStreamResponse(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/speak?" + query.Encode(),
		Model:    req.Model,
		Body:     map[string]string{"text": req.Input},
	})
	if err != nil {
		return nil, refineError(err)
	}
	// Labeled from the requested format rather than the upstream header, which
	// spells raw pcm as "audio/l16;rate=24000".
	return &core.AudioResponse{
		ContentType: core.SpeechResponseContentType(format),
		Stream:      raw.Stream,
	}, nil
}
