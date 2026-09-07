package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/plugins/exchange"
	"github.com/enterpilot/gomodel/pluginapi"
)

// ChatCompleter runs a gateway-internal chat completion (routing, usage and
// budgets apply). The server's InternalChatCompletionExecutor implements it.
type ChatCompleter interface {
	ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error)
}

// MetricsSink receives plugin metrics. Names arrive already prefixed with
// "plugin_<name>_".
type MetricsSink interface {
	Inc(name string, labels map[string]string)
	Observe(name string, value float64, labels map[string]string)
}

// HostDeps are the gateway services a Host exposes to plugins.
type HostDeps struct {
	Logger *slog.Logger
	// Chat may be nil; Inference().Complete then fails with a clear error.
	Chat ChatCompleter
	// Metrics may be nil; a no-op sink is used.
	Metrics MetricsSink
}

// HostInfo identifies the instance a Host serves.
type HostInfo struct {
	PluginName   string
	InstanceName string
	// UserPath, when set, scopes the instance's internal inference to that
	// user path instead of the current request's path.
	UserPath string
}

// ErrInferenceUnavailable is returned when no ChatCompleter is wired.
var ErrInferenceUnavailable = errors.New("plugins: internal inference is not available")

// ErrHistoryUnavailable is returned by History in this host version.
var ErrHistoryUnavailable = errors.New("plugins: history is not available in this host version")

type host struct {
	deps   HostDeps
	info   HostInfo
	logger *slog.Logger
}

// NewHost builds the Host for one instance. The logger is tagged with the
// plugin and instance names.
func NewHost(deps HostDeps, info HostInfo) pluginapi.Host {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("plugin", info.PluginName, "instance", info.InstanceName)
	return &host{deps: deps, info: info, logger: logger}
}

func (h *host) Logger() *slog.Logger { return h.logger }

func (h *host) Inference() pluginapi.Inference { return h }

func (h *host) History(context.Context, pluginapi.Meta) ([]pluginapi.Message, error) {
	return nil, ErrHistoryUnavailable
}

func (h *host) Metrics() pluginapi.Metrics {
	return &metrics{sink: h.deps.Metrics, prefix: "plugin_" + sanitizeMetricName(h.info.PluginName) + "_"}
}

// Complete runs a chat completion through the gateway with origin "plugin".
// The call is scoped to "<user path>/guardrails/<instance>" so budgets and
// audit attribute it to the instance.
func (h *host) Complete(ctx context.Context, req pluginapi.InferenceRequest) (*pluginapi.Completion, error) {
	if h.deps.Chat == nil {
		return nil, ErrInferenceUnavailable
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, errors.New("plugins: inference model is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	userPath, err := h.inferenceUserPath(ctx, req.UserPath)
	if err != nil {
		return nil, err
	}
	ctx = core.WithRequestOrigin(ctx, core.RequestOriginPlugin)
	ctx = core.WithEffectiveUserPath(ctx, userPath)
	chatReq := exchange.ChatRequestFromMessages(req.Model, req.Messages, req.MaxTokens, req.Temperature)
	resp, err := h.deps.Chat.ChatCompletion(ctx, chatReq)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("plugins: inference returned no response")
	}
	return exchange.FromChatResponse(resp)
}

func (h *host) inferenceUserPath(ctx context.Context, override string) (string, error) {
	base := strings.TrimSpace(override)
	if base == "" {
		base = strings.TrimSpace(h.info.UserPath)
	}
	if base == "" {
		base = core.UserPathFromContext(ctx)
	}
	if base == "" {
		base = "/"
	}
	return appendUserPathSegments(base, "guardrails", h.info.InstanceName)
}

func appendUserPathSegments(basePath string, segments ...string) (string, error) {
	basePath, err := core.NormalizeUserPath(basePath)
	if err != nil {
		return "", err
	}
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		switch segment {
		case "", ".", "..":
			return "", fmt.Errorf("plugins: invalid user path segment %q", segment)
		}
		if strings.ContainsAny(segment, "/:") {
			return "", fmt.Errorf("plugins: user path segment %q cannot contain '/' or ':'", segment)
		}
		if basePath == "/" {
			basePath = "/" + segment
			continue
		}
		basePath += "/" + segment
	}
	return basePath, nil
}

type metrics struct {
	sink   MetricsSink
	prefix string
}

func (m *metrics) Inc(name string, labels map[string]string) {
	if m.sink == nil {
		return
	}
	m.sink.Inc(m.prefix+sanitizeMetricName(name), labels)
}

func (m *metrics) Observe(name string, value float64, labels map[string]string) {
	if m.sink == nil {
		return
	}
	m.sink.Observe(m.prefix+sanitizeMetricName(name), value, labels)
}

func sanitizeMetricName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
