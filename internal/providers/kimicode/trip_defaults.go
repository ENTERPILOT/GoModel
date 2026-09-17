package kimicode

import (
	"time"

	"github.com/enterpilot/gomodel/config"
)

// Default trip rules for the Kimi for Coding plan.
//
// Each pattern is anchored against the raw error body (message + code field
// concatenated by llmclient.quotaTripTTL).  The regexes are compiled lazily
// at package init so provider creation is cheap and pattern errors surface at
// startup, not at runtime.
//
// Observed upstream error fragments (from kimicode-weselben audit log):
//
//	"You've reached your weekly (7-day) usage limit"
//	"5-hour usage limit reached"
//	"usage limit reached" / "quota exceeded"
//
// Pin the exact body fragments in unit tests (see kimicode_test.go).

func kimicodeDefaultTripOn() []config.TripRuleConfig {
	return []config.TripRuleConfig{
		// Weekly 7-day plan limit → 4 hour cooldown.
		{Match: `weekly \(7-day\) usage limit`, TTL: 4 * time.Hour},
		// 5-hour sliding-window limit.
		{Match: `5-hour usage limit`, TTL: 30 * time.Minute},
		// Catch-all for usage-limit and quota errors.
		{Match: `usage limit|quota`, TTL: 15 * time.Minute},
	}
}
