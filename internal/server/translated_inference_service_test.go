package server

import (
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/usage"
)

type usageCaptureLogger struct {
	config  usage.Config
	entries []*usage.UsageEntry
}

func (l *usageCaptureLogger) Write(entry *usage.UsageEntry) {
	l.entries = append(l.entries, entry)
}

func (l *usageCaptureLogger) Config() usage.Config { return l.config }
func (l *usageCaptureLogger) Close() error         { return nil }

func TestTranslatedInferenceService_LogUsageSkipsWhenExecutionPlanDisablesUsage(t *testing.T) {
	logger := &usageCaptureLogger{
		config: usage.Config{Enabled: true},
	}
	service := &translatedInferenceService{
		usageLogger: logger,
	}

	service.logUsage(&core.ExecutionPlan{
		Policy: &core.ResolvedExecutionPolicy{
			VersionID: "plan-usage-off",
			Features: core.ExecutionFeatures{
				Cache:      true,
				Audit:      true,
				Usage:      false,
				Guardrails: true,
			},
		},
	}, "gpt-5-nano", "openai", func(*core.ModelPricing) *usage.UsageEntry {
		return &usage.UsageEntry{ID: "usage-1"}
	})

	if len(logger.entries) != 0 {
		t.Fatalf("len(entries) = %d, want 0", len(logger.entries))
	}
}
