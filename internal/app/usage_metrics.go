package app

import (
	"log/slog"

	"github.com/enterpilot/gomodel/internal/observability"
	"github.com/enterpilot/gomodel/internal/usagemetrics"
)

// wrapUsageLoggerForMetrics feeds every recorded usage entry into the token
// and cost metrics of each enabled pipeline (Prometheus under
// METRICS_ENABLED, OpenTelemetry under OTEL_ENABLED). With both off the
// logger stays unwrapped, so the request path pays nothing. It must run
// before wrapUsageLoggerForBilling: the markup tap then wraps this one, and
// an entry reaches the metrics with its billed cost set.
func (b *bootstrap) wrapUsageLoggerForMetrics() {
	app := b.app
	metricsCfg := b.appCfg.Metrics

	var recorders []usagemetrics.Recorder
	if metricsCfg.Enabled {
		recorders = append(recorders, observability.UsageRecorder{})
	}
	if app.telemetry != nil {
		recorders = append(recorders, app.telemetry.UsageRecorder())
	}
	if len(recorders) == 0 {
		return
	}

	callers := usagemetrics.NewCallers(metricsCfg.CallerDimensions, app.authKeyName)
	if metricsCfg.Enabled {
		// nil when every dimension is off, which also switches the per-caller
		// request counter off again after a reload.
		observability.SetRequestCallers(callers)
	}
	if !b.appCfg.Usage.Enabled {
		slog.Warn("token and cost metrics need usage tracking, which is disabled; they will stay empty",
			"hint", "set USAGE_ENABLED=true (usage.enabled) to record token and cost metrics")
	}
	app.usage.Logger = usagemetrics.NewTap(app.usage.Logger, callers, recorders...)
}

// authKeyName returns the name of a managed API key for the auth key metric
// dimension, or "" when the key is unknown. The key service is built after
// the usage logger is wrapped, so it is looked up per call.
func (a *App) authKeyName(id string) string {
	if a == nil || a.authKeys == nil || a.authKeys.Service == nil {
		return ""
	}
	view, err := a.authKeys.Service.View(id)
	if err != nil {
		return ""
	}
	return view.Name
}
