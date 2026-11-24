package ext

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type namedAlertSink struct{ name string }

func (s *namedAlertSink) Name() string { return s.name }

func (s *namedAlertSink) SendAlert(context.Context, AlertEvent) error { return nil }

func TestRegistryCollectsAlertSinks(t *testing.T) {
	reg := &Registry{}
	require.Empty(t, reg.AlertSinks())

	reg.RegisterAlertSink(nil)
	reg.RegisterAlertSink(&namedAlertSink{name: "chat"})
	reg.RegisterAlertSink(&namedAlertSink{name: "mail"})

	sinks := reg.AlertSinks()
	require.Len(t, sinks, 2)
	assert.Equal(t, "chat", sinks[0].Name())
	assert.Equal(t, "mail", sinks[1].Name())

	sinks[0] = nil
	assert.NotNil(t, reg.AlertSinks()[0], "AlertSinks returns a copy")
}

// The JSON form is the webhook body, so its field names are a contract.
func TestAlertEventJSONShape(t *testing.T) {
	projected := 125.5
	exhaustion := time.Date(2026, time.April, 25, 19, 30, 0, 0, time.UTC)
	event := AlertEvent{
		SchemaVersion: AlertSchemaVersion,
		Type:          AlertTypeBudgetThreshold,
		ID:            "bgt_0123",
		Timestamp:     time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC),
		Budget: AlertBudget{
			Scope:            "user_path",
			Subject:          "/customers",
			EffectiveSubject: "/customers/alice",
			PerChild:         true,
			PeriodSeconds:    86400,
			Period:           "daily",
			Amount:           100,
			CostBasis:        "provider",
			Revision:         "1745539200:100",
		},
		ThresholdPercent: 80,
		Spent:            81,
		UsageRatio:       0.81,
		PeriodStart:      time.Date(2026, time.April, 25, 0, 0, 0, 0, time.UTC),
		PeriodEnd:        time.Date(2026, time.April, 26, 0, 0, 0, 0, time.UTC),
		ProjectedSpend:   &projected,

		ProjectedExhaustionAt: &exhaustion,
	}

	body, err := json.Marshal(event)
	require.NoError(t, err)
	assert.Equal(t, `{"schema_version":1,"type":"budget.threshold_reached","id":"bgt_0123","timestamp":"2026-04-25T12:00:00Z",`+
		`"budget":{"scope":"user_path","subject":"/customers","effective_subject":"/customers/alice","per_child":true,`+
		`"period_seconds":86400,"period":"daily","amount":100,"cost_basis":"provider","revision":"1745539200:100"},`+
		`"threshold_percent":80,"spent":81,"usage_ratio":0.81,`+
		`"period_start":"2026-04-25T00:00:00Z","period_end":"2026-04-26T00:00:00Z","projected_spend":125.5,`+
		`"projected_exhaustion_at":"2026-04-25T19:30:00Z"}`, string(body))

	event.ProjectedSpend, event.ProjectedExhaustionAt = nil, nil
	body, err = json.Marshal(event)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "projected_spend")
	assert.NotContains(t, string(body), "projected_exhaustion_at")
}
