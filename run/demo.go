package run

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	envDemoMode             = "GOMODEL_DEMO_MODEL"
	demoModeWarningInterval = 5 * time.Minute
)

func demoModeFromEnv() (bool, error) {
	raw := strings.TrimSpace(os.Getenv(envDemoMode))
	if raw == "" {
		return false, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s %q: expected a boolean", envDemoMode, raw)
	}
	return enabled, nil
}

func startDemoModeWarnings(ctx context.Context) {
	logDemoModeWarning()
	go repeatDemoModeWarnings(ctx, demoModeWarningInterval, logDemoModeWarning)
}

func repeatDemoModeWarnings(ctx context.Context, interval time.Duration, warn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			warn()
		}
	}
}

func logDemoModeWarning() {
	slog.Warn("DEMO MODE: this environment is public; do not enter sensitive or personal data; demo data is reset regularly",
		"demo_mode", true,
		"reminder_interval", demoModeWarningInterval,
	)
}
