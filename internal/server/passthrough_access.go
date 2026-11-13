package server

import (
	"context"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
)

// passthroughInferenceEndpoints are the normalized passthrough endpoints where
// a POST runs inference on the one model its body names. They mirror the
// inference rows of the providers' passthrough semantics tables. The list is
// deliberately short: an endpoint missing from it still works, it only needs a
// provider-wide grant.
var passthroughInferenceEndpoints = map[string]struct{}{
	"chat/completions":   {},
	"completions":        {},
	"responses":          {},
	"embeddings":         {},
	"messages":           {},
	"audio/speech":       {},
	"beta/completions":   {}, // deepseek
	"v2/chat":            {}, // cohere
	"v2/embed":           {}, // cohere
	"systemone":          {}, // jev
	"systemone/permute":  {}, // jev
	"systemone/separate": {}, // jev
}

// passthroughRunsModelInference reports whether the request is an inference
// call on the model its body names, so that granting the model is enough to
// authorize it. Any other method or endpoint can act on resources of the whole
// provider account, whatever its body names. The method comes from the ingress
// snapshot; without one the request is not treated as inference.
func passthroughRunsModelInference(ctx context.Context, info *core.PassthroughRouteInfo) bool {
	snapshot := core.GetRequestSnapshot(ctx)
	if snapshot == nil || snapshot.Method != http.MethodPost || info == nil {
		return false
	}
	_, ok := passthroughInferenceEndpoints[info.NormalizedEndpoint]
	return ok
}
