package minimax

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

var _ core.AudioProvider = (*Provider)(nil)

type speechRequest struct {
	Model        string             `json:"model"`
	Text         string             `json:"text"`
	Stream       bool               `json:"stream"`
	OutputFormat string             `json:"output_format"`
	VoiceSetting speechVoiceSetting `json:"voice_setting"`
	AudioSetting speechAudioSetting `json:"audio_setting"`
}

type speechVoiceSetting struct {
	VoiceID string  `json:"voice_id"`
	Speed   float64 `json:"speed"`
}

type speechAudioSetting struct {
	Format string `json:"format"`
}

type speechResponse struct {
	Data *struct {
		Audio  string `json:"audio"`
		Status int    `json:"status"`
	} `json:"data"`
	BaseResponse struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

// CreateSpeech translates the OpenAI-compatible request to MiniMax's native
// synchronous text-to-audio endpoint and decodes its hexadecimal audio payload.
func (p *Provider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("audio speech request is required", nil)
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return nil, core.NewInvalidRequestError("model is required", nil)
	}
	if strings.TrimSpace(req.Input) == "" {
		return nil, core.NewInvalidRequestError("input is required", nil)
	}
	voice := strings.TrimSpace(req.Voice)
	if voice == "" {
		return nil, core.NewInvalidRequestError("voice is required", nil)
	}
	if strings.TrimSpace(req.Instructions) != "" {
		return nil, core.NewInvalidRequestError("minimax speech does not support instructions", nil)
	}

	format, err := speechFormat(req.ResponseFormat)
	if err != nil {
		return nil, err
	}
	speed, err := speechSpeed(req.Speed)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(speechRequest{
		Model:        model,
		Text:         req.Input,
		Stream:       false,
		OutputFormat: "hex",
		VoiceSetting: speechVoiceSetting{
			VoiceID: voice,
			Speed:   speed,
		},
		AudioSetting: speechAudioSetting{Format: format},
	})
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to encode minimax speech request", err)
	}

	upstream, err := p.Passthrough(ctx, &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/t2a_v2",
		Body:     io.NopCloser(bytes.NewReader(body)),
		Headers:  http.Header{"Content-Type": {"application/json"}},
	})
	if err != nil {
		return nil, err
	}
	if upstream == nil || upstream.Body == nil {
		return nil, core.NewEmptyProviderResponseError("minimax")
	}
	defer func() { _ = upstream.Body.Close() }()

	responseBody, err := io.ReadAll(upstream.Body)
	if err != nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "failed to read speech response", err)
	}
	if upstream.StatusCode < http.StatusOK || upstream.StatusCode >= http.StatusMultipleChoices {
		return nil, core.ParseProviderError("minimax", upstream.StatusCode, responseBody, nil)
	}

	var response speechResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "failed to parse speech response", err)
	}
	if response.BaseResponse.StatusCode != 0 {
		return nil, speechStatusError(response.BaseResponse.StatusCode, response.BaseResponse.StatusMsg)
	}
	if response.Data == nil || strings.TrimSpace(response.Data.Audio) == "" {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "speech response contains no audio", nil)
	}
	if response.Data.Status != 2 {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "speech response did not complete", nil)
	}

	audio, err := hex.DecodeString(strings.TrimSpace(response.Data.Audio))
	if err != nil {
		return nil, core.NewProviderError("minimax", http.StatusBadGateway, "speech response audio is not valid hexadecimal data", err)
	}
	return &core.AudioResponse{
		ContentType: core.SpeechResponseContentType(format),
		Data:        audio,
	}, nil
}

// speechStatusError maps a MiniMax base_resp status code to a gateway error.
// MiniMax reports failures as HTTP 200 with a non-zero base_resp.status_code,
// so caller mistakes (invalid parameters, auth, balance, rate limits) must be
// surfaced with their real meaning rather than a blanket 502.
func speechStatusError(statusCode int, statusMessage string) error {
	message := fmt.Sprintf("minimax speech request failed (status %d)", statusCode)
	if statusMessage = strings.TrimSpace(statusMessage); statusMessage != "" {
		message += ": " + statusMessage
	}
	switch statusCode {
	case 1002, 1039: // rate limit / token-per-minute limit triggered
		return core.NewRateLimitError("minimax", message)
	case 1004: // authentication failed
		return core.NewAuthenticationError("minimax", message)
	case 1008: // insufficient balance
		return core.NewProviderError("minimax", http.StatusPaymentRequired, message, nil)
	case 1026, 2013: // sensitive input content / invalid request parameters
		return core.NewInvalidRequestError(message, nil)
	default:
		return core.NewProviderError("minimax", http.StatusBadGateway, message, nil)
	}
}

func speechFormat(responseFormat string) (string, error) {
	format := strings.ToLower(strings.TrimSpace(responseFormat))
	if format == "" {
		format = "mp3"
	}
	switch format {
	case "mp3", "wav", "flac", "pcm":
		return format, nil
	default:
		return "", core.NewInvalidRequestError("minimax speech supports mp3, wav, flac, or pcm response formats", nil)
	}
}

func speechSpeed(speed float64) (float64, error) {
	if speed == 0 {
		return 1, nil
	}
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed < 0.5 || speed > 2 {
		return 0, core.NewInvalidRequestError("minimax speech speed must be between 0.5 and 2", nil)
	}
	return speed, nil
}

// CreateTranscription reports the unsupported half of core.AudioProvider.
func (p *Provider) CreateTranscription(_ context.Context, _ *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	return nil, core.NewInvalidRequestError("minimax does not support speech-to-text", nil)
}
