package usage

import (
	"time"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"gomodel/internal/core"
)

const endpointRealtime = "/v1/realtime"

// realtimeTokenDetails is the per-modality token breakdown inside a realtime
// usage object.
type realtimeTokenDetails struct {
	TextTokens   int `json:"text_tokens"`
	AudioTokens  int `json:"audio_tokens"`
	CachedTokens int `json:"cached_tokens"`
}

// realtimeUsage mirrors the usage object carried by a realtime "response.done"
// server event. Realtime bills text and audio tokens separately in both
// directions; the breakdown is preserved in RawData for cost attribution.
//
// Providers disagree on the detail-field spelling: OpenAI uses the singular
// "*_token_details" while Alibaba/Bailian uses the plural "*_tokens_details".
// Both spellings are decoded and merged so audio tokens are priced for either.
type realtimeUsage struct {
	TotalTokens         int                  `json:"total_tokens"`
	InputTokens         int                  `json:"input_tokens"`
	OutputTokens        int                  `json:"output_tokens"`
	InputTokenDetails   realtimeTokenDetails `json:"input_token_details"`
	InputTokensDetails  realtimeTokenDetails `json:"input_tokens_details"`
	OutputTokenDetails  realtimeTokenDetails `json:"output_token_details"`
	OutputTokensDetails realtimeTokenDetails `json:"output_tokens_details"`
}

// inputDetails / outputDetails collapse the singular and plural spellings,
// preferring whichever field a given provider populated.
func (u realtimeUsage) inputDetails() realtimeTokenDetails {
	return mergeRealtimeTokenDetails(u.InputTokenDetails, u.InputTokensDetails)
}

func (u realtimeUsage) outputDetails() realtimeTokenDetails {
	return mergeRealtimeTokenDetails(u.OutputTokenDetails, u.OutputTokensDetails)
}

func mergeRealtimeTokenDetails(a, b realtimeTokenDetails) realtimeTokenDetails {
	return realtimeTokenDetails{
		TextTokens:   firstNonZero(a.TextTokens, b.TextTokens),
		AudioTokens:  firstNonZero(a.AudioTokens, b.AudioTokens),
		CachedTokens: firstNonZero(a.CachedTokens, b.CachedTokens),
	}
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
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
	in, out := u.inputDetails(), u.outputDetails()
	raw := map[string]any{}
	if in.TextTokens > 0 {
		raw["prompt_text_tokens"] = in.TextTokens
	}
	if in.AudioTokens > 0 {
		raw["prompt_audio_tokens"] = in.AudioTokens
	}
	if in.CachedTokens > 0 {
		raw["prompt_cached_tokens"] = in.CachedTokens
	}
	if out.TextTokens > 0 {
		raw["completion_text_tokens"] = out.TextTokens
	}
	if out.AudioTokens > 0 {
		raw["completion_audio_tokens"] = out.AudioTokens
	}
	if len(raw) > 0 {
		entry.RawData = raw
	}

	applyUsageCosts(entry, provider, endpointRealtime, pricing...)

	return entry
}
