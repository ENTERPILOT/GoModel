package pluginapi

import (
	"context"
	"log/slog"
)

// Host is what GoModel offers a plugin instance. It is passed to
// [Plugin.Init] and stays valid until [Plugin.Close].
type Host interface {
	// Logger returns a logger pre-tagged with the plugin and instance name.
	Logger() *slog.Logger
	// Inference runs internal chat completions through the gateway. Routing,
	// usage accounting, and budgets apply; requests carry origin "plugin".
	Inference() Inference
	// History loads earlier turns that are not in the request body: Responses
	// requests referencing previous_response_id or a conversation. Returns
	// nil, nil when there is nothing stored.
	History(ctx context.Context, meta Meta) ([]Message, error)
	// Metrics registers and updates counters and histograms under the
	// plugin's own metric namespace.
	Metrics() Metrics
}

// Inference runs a chat completion through the gateway on behalf of a plugin.
type Inference interface {
	Complete(ctx context.Context, req InferenceRequest) (*Completion, error)
}

// InferenceRequest describes an internal chat completion.
type InferenceRequest struct {
	// Model is a "provider/model" reference, an alias, or a virtual model.
	Model string
	// UserPath optionally overrides the user path the internal call is scoped
	// to (for budgets and audit). Empty means the current request's path.
	UserPath string
	// Messages is the conversation to send.
	Messages []Message
	// MaxTokens caps the completion length; zero leaves it to the model.
	MaxTokens int
	// Temperature is the sampling temperature; nil leaves it to the model.
	Temperature *float64
}

// Metrics records plugin metrics. Names are prefixed by the host with the
// plugin name; labels become metric labels.
type Metrics interface {
	// Inc increments a counter by one.
	Inc(name string, labels map[string]string)
	// Observe records a histogram sample.
	Observe(name string, value float64, labels map[string]string)
}
