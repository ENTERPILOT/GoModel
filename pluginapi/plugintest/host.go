// Package plugintest helps test pluginapi plugins without GoModel: a fake
// [pluginapi.Host] with scripted inference and recorded metrics, builders
// for prompts, completions, and exchanges, and a stream driver that feeds
// events to a [pluginapi.StreamHook] the way the host does, lookbehind,
// overlap, and chunk coalescing included.
package plugintest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"sync"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Host is a fake [pluginapi.Host]. Its zero value is usable: inference
// answers with an empty completion, metrics are recorded, and HTTP goes
// through [http.DefaultClient].
type Host struct {
	// Replies are the inference replies, consumed in order; when they run
	// out, Complete returns a completion without choices.
	Replies []string
	// Finish is the finish reason of every reply; "" means "stop".
	Finish string
	// Err, when set, is returned by every Complete call.
	Err error
	// Reply, when set, answers every Complete call itself; Replies, Finish,
	// and Err are then ignored.
	Reply func(req pluginapi.InferenceRequest) (*pluginapi.Completion, error)
	// Client is what HTTPClient returns; nil selects http.DefaultClient.
	Client *http.Client
	// Log is what Logger returns; nil selects a logger that discards.
	Log *slog.Logger

	mu       sync.Mutex
	requests []pluginapi.InferenceRequest
	metrics  Metrics
}

var _ pluginapi.Host = (*Host)(nil)

// NewHost returns a Host that answers inference with replies, in order.
func NewHost(replies ...string) *Host {
	return &Host{Replies: replies}
}

// Requests returns every inference request made so far.
func (h *Host) Requests() []pluginapi.InferenceRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]pluginapi.InferenceRequest(nil), h.requests...)
}

// Recorded returns the metrics recorded so far.
func (h *Host) Recorded() Metrics {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.metrics.clone()
}

// Logger implements pluginapi.Host.
func (h *Host) Logger() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Inference implements pluginapi.Host.
func (h *Host) Inference() pluginapi.Inference { return h }

// History implements pluginapi.Host; it reports no history.
func (h *Host) History(context.Context, pluginapi.Meta) ([]pluginapi.Message, error) {
	return nil, errors.New("plugintest: history is not available")
}

// Metrics implements pluginapi.Host.
func (h *Host) Metrics() pluginapi.Metrics { return (*recorder)(h) }

// HTTPClient implements pluginapi.Host.
func (h *Host) HTTPClient() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

// Complete implements pluginapi.Inference with the scripted replies.
func (h *Host) Complete(_ context.Context, req pluginapi.InferenceRequest) (*pluginapi.Completion, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests = append(h.requests, req)
	if h.Reply != nil {
		return h.Reply(req)
	}
	if h.Err != nil {
		return nil, h.Err
	}
	if len(h.Replies) == 0 {
		return &pluginapi.Completion{}, nil
	}
	reply := h.Replies[0]
	h.Replies = h.Replies[1:]
	finish := h.Finish
	if finish == "" {
		finish = "stop"
	}
	return &pluginapi.Completion{Choices: []pluginapi.Choice{{
		Message: pluginapi.TextMessage(pluginapi.RoleAssistant, reply), FinishReason: finish,
	}}}, nil
}

// Metrics is what a plugin recorded through Host.Metrics.
type Metrics struct {
	// Counts holds the sum per counter name.
	Counts map[string]int
	// Values holds every observation per name, in order.
	Values map[string][]float64
	// Labels holds the labels of the last call per name.
	Labels map[string]map[string]string
}

func (m Metrics) clone() Metrics {
	out := Metrics{Counts: map[string]int{}, Values: map[string][]float64{}, Labels: map[string]map[string]string{}}
	maps.Copy(out.Counts, m.Counts)
	for k, v := range m.Values {
		out.Values[k] = append([]float64(nil), v...)
	}
	maps.Copy(out.Labels, m.Labels)
	return out
}

type recorder Host

func (r *recorder) Inc(name string, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics.init()
	r.metrics.Counts[name]++
	r.metrics.Labels[name] = labels
}

func (r *recorder) Observe(name string, value float64, labels map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.metrics.init()
	r.metrics.Values[name] = append(r.metrics.Values[name], value)
	r.metrics.Labels[name] = labels
}

func (m *Metrics) init() {
	if m.Counts == nil {
		m.Counts = map[string]int{}
		m.Values = map[string][]float64{}
		m.Labels = map[string]map[string]string{}
	}
}
