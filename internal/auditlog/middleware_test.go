package auditlog

import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

func TestApplyAuthenticationRefreshesLabelsFromContext(t *testing.T) {
	entry := &LogEntry{Data: &LogData{Labels: []string{"from-header"}}}
	ctx := core.WithRequestLabels(t.Context(), []string{"from-header", "from-key"})

	applyAuthentication(entry, ctx)

	require.Equal(t, []string{"from-header", "from-key"}, entry.Data.Labels)
}

func TestApplyAuthenticationKeepsLabelsWhenContextHasNone(t *testing.T) {
	entry := &LogEntry{Data: &LogData{Labels: []string{"from-header"}}}

	applyAuthentication(entry, t.Context())

	require.Equal(t, []string{"from-header"}, entry.Data.Labels)
}

func TestApplyAuthenticationPersistsExtensionPrincipal(t *testing.T) {
	entry := &LogEntry{}
	ctx := ext.WithAuthentication(t.Context(), ext.Authentication{PrincipalID: "oidc:principal-1", Method: "oidc"})

	applyAuthentication(entry, ctx)

	require.Equal(t, "oidc:principal-1", entry.PrincipalID)
	require.Equal(t, "oidc", entry.AuthMethod)
}

func TestApplyAuthenticationDoesNotReplacePrincipalWithBlank(t *testing.T) {
	entry := &LogEntry{PrincipalID: "existing-principal"}
	ctx := ext.WithAuthentication(t.Context(), ext.Authentication{PrincipalID: "  "})

	applyAuthentication(entry, ctx)

	require.Equal(t, "existing-principal", entry.PrincipalID)
}

func TestEnrichEntryWithWorkflow_PrefersProviderNameForResolvedModel(t *testing.T) {
	c, _ := echotest.Get(t, "/")

	entry := &LogEntry{ID: "provider-name-prefill"}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithWorkflow(c, &core.Workflow{
		ProviderType: "openai",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{
				Provider: "openai",
				Model:    "gpt-5-nano",
			},
			ProviderName: "openai_test",
		},
	})
	require.Equal(t, "openai", entry.Provider)
	require.Equal(t, "openai_test", entry.ProviderName)
	require.Equal(t, "openai_test/gpt-5-nano", entry.ResolvedModel)
}

func TestEnrichEntryWithWorkflow_PreservesExecutedFailoverRoute(t *testing.T) {
	c, _ := echotest.Get(t, "/")

	// The handler already recorded the actual executed failover route and the
	// failover snapshot before the middleware re-applies the workflow.
	entry := &LogEntry{
		ID:            "failover-route",
		Provider:      "openai",
		ProviderName:  "openai",
		ResolvedModel: "openai/gpt-5.5",
		Data:          &LogData{Failover: &FailoverSnapshot{TargetModel: "openai/gpt-5.5"}},
	}
	c.Set(string(LogEntryKey), entry)

	// The workflow still carries only the planned primary resolution.
	EnrichEntryWithWorkflow(c, &core.Workflow{
		ProviderType: "anthropic",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "anthropic", Model: "claude-fable-5"},
			ProviderName:     "anthropic",
		},
	})
	require.Equal(t, "openai/gpt-5.5", entry.ResolvedModel)
	require.Equal(t, "openai", entry.Provider)
	require.Equal(t, "openai", entry.ProviderName)
}

func TestEnrichEntryWithWorkflow_FailoverSnapshotDoesNotSuppressMissingRouteFields(t *testing.T) {
	c, _ := echotest.Get(t, "/")

	// A failover snapshot exists, but the executed route only populated the
	// resolved model — provider/provider_name came back empty. The snapshot
	// alone must not suppress the remaining fields; the workflow's planned
	// values should fill the gaps instead of leaving them blank.
	entry := &LogEntry{
		ID:            "failover-partial-route",
		ResolvedModel: "openai/gpt-5.5",
		Data:          &LogData{Failover: &FailoverSnapshot{TargetModel: "openai/gpt-5.5"}},
	}
	c.Set(string(LogEntryKey), entry)

	EnrichEntryWithWorkflow(c, &core.Workflow{
		ProviderType: "anthropic",
		Resolution: &core.RequestModelResolution{
			ResolvedSelector: core.ModelSelector{Provider: "anthropic", Model: "claude-fable-5"},
			ProviderName:     "anthropic",
		},
	})
	require.Equal(t, "openai/gpt-5.5", entry.ResolvedModel)
	require.Equal(t, "anthropic", entry.Provider)
	require.Equal(t, "anthropic", entry.ProviderName)
}

func TestMiddlewarePublishesStartedEventWithRedactedRequestHeaders(t *testing.T) {
	logger := &captureLiveLogger{
		cfg: Config{
			Enabled:    true,
			LogHeaders: true,
		},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", nil, echotest.WithHeader("X-Request-ID", "req-started"), echotest.WithHeader("Authorization", "Bearer secret"), echotest.WithHeader("X-Test", "visible"))

	handler := Middleware(logger)(func(c *echo.Context) error {
		require.Len(t, logger.events, 1)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.NotEmpty(t, logger.events)

	started := logger.events[0]
	require.Equal(t, LiveEventAuditStarted, started.eventType)
	require.Equal(t, "[REDACTED]", started.requestHeaders["Authorization"])
	require.Equal(t, "visible", started.requestHeaders["X-Test"])
}

func TestMiddlewarePublishesWorkflowUpdateWithCapturedRequestBody(t *testing.T) {
	logger := &captureLiveLogger{
		cfg: Config{
			Enabled:   true,
			LogBodies: true,
		},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	trackedBody := &readCountCloser{reader: strings.NewReader(`{"model":"from-stream"}`)}
	c.Request().Body = trackedBody
	c.SetRequest(c.Request().WithContext(core.WithRequestSnapshot(c.Request().Context(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"from-snapshot"}`),
		false,
		"req-body",
		nil,
	))))

	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntryWithWorkflow(c, &core.Workflow{
			Policy: &core.ResolvedWorkflowPolicy{
				VersionID: "audit-enabled",
				Features: core.WorkflowFeatures{
					Audit: true,
				},
			},
		})
		require.Len(t, logger.events, 2)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.Equal(t, 0, trackedBody.readCalls)

	updated := logger.events[1]
	require.Equal(t, LiveEventAuditUpdated, updated.eventType)

	body, ok := BodyDocument(updated.requestBody).(map[string]any)
	require.True(t, ok, "request body = %T, want JSON object", updated.requestBody)
	require.Equal(t, "from-snapshot", body["model"])
}

func TestMiddlewareDoesNotPublishRequestBodyForAuditDisabledWorkflow(t *testing.T) {
	logger := &captureLiveLogger{
		cfg: Config{
			Enabled:   true,
			LogBodies: true,
		},
	}

	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	c.SetRequest(c.Request().WithContext(core.WithRequestSnapshot(c.Request().Context(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		nil,
		"application/json",
		[]byte(`{"model":"hidden"}`),
		false,
		"req-hidden",
		nil,
	))))

	handler := Middleware(logger)(func(c *echo.Context) error {
		workflow := &core.Workflow{
			Policy: &core.ResolvedWorkflowPolicy{
				VersionID: "audit-disabled",
				Features: core.WorkflowFeatures{
					Audit: false,
				},
			},
		}
		EnrichEntryWithWorkflow(c, workflow)
		require.Equal(t, workflow, core.GetWorkflow(c.Request().Context()))
		require.Len(t, logger.events, 2)

		return nil
	})
	err := handler(c)
	require.NoError(t, err)
	require.Len(t, logger.events, 3)
	body := logger.events[1].requestBody
	require.Nil(t, body)

	removed := logger.events[2]
	require.Equal(t, LiveEventAuditRemoved, removed.eventType)
	require.Nil(t, removed.requestBody)
	require.Equal(t, 0, logger.writes)
}

type capturedLiveEvent struct {
	eventType      string
	requestHeaders map[string]string
	requestBody    any
}

type captureLiveLogger struct {
	cfg    Config
	events []capturedLiveEvent
	writes int
}

func (l *captureLiveLogger) Write(_ *LogEntry) {
	l.writes++
}

func (l *captureLiveLogger) Config() Config {
	return l.cfg
}

func (l *captureLiveLogger) Close() error {
	return nil
}

func (l *captureLiveLogger) PublishLiveEvent(eventType string, entry *LogEntry) {
	headers := map[string]string(nil)
	if entry != nil && entry.Data != nil && entry.Data.RequestHeaders != nil {
		headers = make(map[string]string, len(entry.Data.RequestHeaders))
		maps.Copy(headers, entry.Data.RequestHeaders)
	}
	var requestBody any
	if entry != nil && entry.Data != nil {
		requestBody = entry.Data.RequestBody
	}
	l.events = append(l.events, capturedLiveEvent{
		eventType:      eventType,
		requestHeaders: headers,
		requestBody:    requestBody,
	})
}

func TestMiddlewarePublishesRemovalWhenHandlerPanics(t *testing.T) {
	logger := &captureLiveLogger{cfg: Config{Enabled: true}}

	c, _ := echotest.Post(t, "/v1/chat/completions", nil, echotest.WithHeader("X-Request-ID", "req-panic"))

	handler := Middleware(logger)(func(c *echo.Context) error {
		panic("handler exploded")
	})

	// The panic must keep propagating to the outer recover middleware; the
	// audit middleware only publishes the terminal live event on the way out.
	func() {
		defer func() {
			r := recover()
			require.NotNil(t, r)

		}()
		_ = handler(c)
	}()

	require.Len(t, logger.events, 2)
	require.Equal(t, LiveEventAuditStarted, logger.events[0].eventType)
	require.Equal(t, LiveEventAuditRemoved, logger.events[1].eventType)
}

// TestMiddlewareClientIPTagging checks the audit entry carries whatever address
// the server resolved for the request, on both a directly exposed gateway and
// one behind a trusted proxy network.
func TestMiddlewareClientIPTagging(t *testing.T) {
	tests := []struct {
		name    string
		proxies []string
		remote  string
		xff     string
		wantIP  string
	}{
		{
			name:   "direct exposure records the socket peer",
			remote: "203.0.113.7:4321",
			xff:    "198.51.100.8",
			wantIP: "203.0.113.7",
		},
		{
			name:    "behind a trusted proxy records the forwarded client",
			proxies: []string{"127.0.0.0/8"},
			remote:  "127.0.0.1:4321",
			xff:     "203.0.113.9, 198.51.100.7",
			wantIP:  "198.51.100.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverCfg := config.ServerConfig{TrustedProxies: tt.proxies}
			require.NoError(t, config.ResolveClientIPPolicy(&serverCfg))

			logger := &capturingLogger{cfg: Config{Enabled: true, OnlyModelInteractions: true}}
			handler := Middleware(logger)(func(*echo.Context) error { return nil })

			c, _ := echotest.Post(t, "/v1/chat/completions", nil,
				echotest.WithRemoteAddr(tt.remote),
				echotest.WithHeader("X-Forwarded-For", tt.xff),
				echotest.WithIPExtractor(serverCfg.ClientIP.Resolve))

			require.NoError(t, handler(c))
			require.Len(t, logger.entries, 1)
			assert.Equal(t, tt.wantIP, logger.entries[0].ClientIP)
		})
	}
}
