package usage

import (
	"strings"
	"time"
)

const defaultUsageTimeZone = "UTC"

func usageTimeZone(params UsageQueryParams) string {
	if strings.TrimSpace(params.TimeZone) == "" {
		return defaultUsageTimeZone
	}
	return params.TimeZone
}

func usageLocation(params UsageQueryParams) *time.Location {
	location, err := time.LoadLocation(usageTimeZone(params))
	if err != nil {
		return time.UTC
	}
	return location
}

func usageEndExclusive(params UsageQueryParams) time.Time {
	if params.EndDate.IsZero() {
		return time.Time{}
	}
	return params.EndDate.AddDate(0, 0, 1)
}
