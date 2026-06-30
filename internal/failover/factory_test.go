package failover

import (
	"testing"
	"time"

	"gomodel/config"
)

func TestRefreshIntervalUsesWorkflowCadence(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cache.Model.RefreshInterval = 3600
	cfg.Workflows.RefreshInterval = 45 * time.Second

	if got := refreshInterval(cfg); got != 45*time.Second {
		t.Fatalf("refreshInterval() = %s, want 45s", got)
	}
}

func TestRefreshIntervalDefaultsToOneMinute(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cache.Model.RefreshInterval = 3600

	if got := refreshInterval(cfg); got != time.Minute {
		t.Fatalf("refreshInterval() = %s, want 1m", got)
	}
}
