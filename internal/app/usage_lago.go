package app

import (
	"log/slog"

	"github.com/enterpilot/gomodel/internal/lago"
)

// wrapUsageLoggerForLago bills every recorded usage entry in Lago when
// LAGO_ENABLED is set. Like the metrics tap it must run before the billing
// taps, which then wrap this one: an entry served by a billable: false
// provider is already marked when it gets here, and is not sent.
func (b *bootstrap) wrapUsageLoggerForLago() {
	cfg := b.appCfg.Lago
	if !cfg.Enabled {
		return
	}
	if !b.appCfg.Usage.Enabled {
		slog.Warn("lago export needs usage tracking, which is disabled; nothing is sent to Lago",
			"hint", "set USAGE_ENABLED=true (usage.enabled) to bill usage in Lago")
		return
	}
	exporter := lago.New(lago.Options{
		APIURL:        cfg.APIURL,
		APIKey:        cfg.APIKey,
		Subscriptions: cfg.Subscriptions,
		BufferSize:    cfg.BufferSize,
	})
	// The usage subsystem closes its logger, and with it the exporter.
	b.app.usage.Logger = lago.NewTap(b.app.usage.Logger, exporter)
	slog.Info("lago export enabled", "subscriptions", len(cfg.Subscriptions))
}
