package providers

import (
	"context"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// ChatWithinEffortCeiling returns req as an adapter should take it for the
// level it sends upstream to be within the effort ceiling of the caller's
// policy. The gateway lowers a client's effort to the ceiling before an
// adapter runs, but the adapter then maps the gateway's levels onto the ones
// its model takes, and may round up: Z.ai's GLM-5.3 takes "medium" as
// "high", DeepSeek "xhigh" as "max". sent reports the level the adapter
// sends for a request, or "" when it sends none.
//
// When that level is above the ceiling, the result is req with the highest
// effort, from the ceiling down, that the adapter sends within it: the
// highest level the model takes that the ceiling allows. When the adapter
// sends no level within the ceiling for any effort, the model cannot reason
// less, and req is returned as it is. So is a request under no effort
// ceiling, and one whose level the gateway's scale cannot place.
func ChatWithinEffortCeiling(ctx context.Context, req *core.ChatRequest, sent func(*core.ChatRequest) string) *core.ChatRequest {
	ceiling := requestparams.EffortRank(core.RequestCeilingsFromContext(ctx).ReasoningEffort)
	if req == nil || ceiling < 0 || requestparams.EffortRank(sent(req)) <= ceiling {
		return req
	}
	levels := requestparams.ReasoningEffortLevels()
	for rank := ceiling; rank >= 0; rank-- {
		lowered := chatWithEffort(req, levels[rank])
		if within := requestparams.EffortRank(sent(lowered)); within >= 0 && within <= ceiling {
			return lowered
		}
	}
	return req
}

// SentEffort returns the reasoning effort a chat request carries: the nested
// reasoning.effort, which adapters read first, or the flat reasoning_effort
// member. It is "" when the request carries none, or one that is no string.
func SentEffort(req *core.ChatRequest) string {
	if req == nil {
		return ""
	}
	if req.Reasoning != nil {
		if effort := strings.TrimSpace(req.Reasoning.Effort); effort != "" {
			return effort
		}
	}
	var flat string
	if raw := req.ExtraFields.Lookup(flatReasoningEffortField); raw == nil || json.Unmarshal(raw, &flat) != nil {
		return ""
	}
	return strings.TrimSpace(flat)
}

const flatReasoningEffortField = "reasoning_effort"

// chatWithEffort returns a shallow copy of req asking for effort where req
// asks for its own: in the nested reasoning object when that carries one,
// otherwise in the flat reasoning_effort member.
func chatWithEffort(req *core.ChatRequest, effort string) *core.ChatRequest {
	lowered := *req
	if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
		reasoning := *req.Reasoning
		reasoning.Effort = effort
		lowered.Reasoning = &reasoning
		return &lowered
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		flatReasoningEffortField: json.RawMessage(strconv.Quote(effort)),
	})
	if err != nil {
		return req
	}
	lowered.ExtraFields = extra
	return &lowered
}
