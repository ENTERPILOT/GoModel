package usage

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"gomodel/internal/core"
)

const endpointRealtime = "/v1/realtime"

// realtimeUsage mirrors the usage object carried by a realtime "response.done"
// server event. Realtime bills text and audio tokens separately in both
// directions; the breakdown is preserved in RawData for cost attribution.
type realtimeUsage struct {
	TotalTokens       int `json:"total_tokens"`
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	InputTokenDetails struct {
		TextTokens   int `json:"text_tokens"`
		AudioTokens  int `json:"audio_tokens"`
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_token_details"`
	OutputTokenDetails struct {
		TextTokens  int `json:"text_tokens"`
		AudioTokens int `json:"audio_tokens"`
	} `json:"output_token_details"`
}

// ExtractFromRealtimeResponseDone builds a usage entry from a realtime
// "response.done" event. A realtime session produces one such event per model
// response, each carrying its own usage, so the caller writes one entry per
// event. It returns nil when the payload is not a response.done event or carries
// no usage, so non-billable events (audio deltas, transcripts) are skipped
// cheaply.
func ExtractFromRealtimeResponseDone(payload []byte, requestID, model, provider string, pricing ...*core.ModelPricing) *UsageEntry {
	var event struct {
		Type     string `json:"type"`
		Response struct {
			Usage *realtimeUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return nil
	}
	if event.Type != "response.done" || event.Response.Usage == nil {
		return nil
	}
	u := event.Response.Usage

	entry := &UsageEntry{
		ID:           uuid.New().String(),
		RequestID:    requestID,
		Timestamp:    time.Now().UTC(),
		Model:        model,
		Provider:     provider,
		Endpoint:     endpointRealtime,
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.TotalTokens,
	}
	if entry.TotalTokens == 0 {
		entry.TotalTokens = u.InputTokens + u.OutputTokens
	}

	// Use the canonical "prompt_"/"completion_" rawData keys that cost.go prices
	// (see buildRawUsageFromDetails); audio tokens otherwise fall through to base
	// text rates instead of the configured audio input/output rates.
	raw := map[string]any{}
	if u.InputTokenDetails.TextTokens > 0 {
		raw["prompt_text_tokens"] = u.InputTokenDetails.TextTokens
	}
	if u.InputTokenDetails.AudioTokens > 0 {
		raw["prompt_audio_tokens"] = u.InputTokenDetails.AudioTokens
	}
	if u.InputTokenDetails.CachedTokens > 0 {
		raw["prompt_cached_tokens"] = u.InputTokenDetails.CachedTokens
	}
	if u.OutputTokenDetails.TextTokens > 0 {
		raw["completion_text_tokens"] = u.OutputTokenDetails.TextTokens
	}
	if u.OutputTokenDetails.AudioTokens > 0 {
		raw["completion_audio_tokens"] = u.OutputTokenDetails.AudioTokens
	}
	if len(raw) > 0 {
		entry.RawData = raw
	}

	applyUsageCosts(entry, provider, endpointRealtime, pricing...)

	return entry
}
